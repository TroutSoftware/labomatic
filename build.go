package labomatic

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"iter"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"go.starlark.net/starlark"
)

// TODO multi-labs:
//  - set unique ns
//  - persistence with names

func Build(nodes starlark.StringDict) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origns, err := netns.Get()
	if err != nil {
		return fmt.Errorf("cannot get handle to existing namespace: %w", err)
	}

	ns, err := netns.NewNamed("lab")
	if err != nil {
		return fmt.Errorf("cannot create namespace: %w", err)
	}
	parent, err := netlink.NewHandleAt(ns)
	if err != nil {
		return fmt.Errorf("cannot get ns handle: %w", err)
	}

	fmt.Println("\033[38;5;2mBuilding the Lab\033[0m")

	{
		lk, err := parent.LinkByName("lo")
		if err != nil {
			return fmt.Errorf("no local interface in netns: %w", err)
		}
		addr, _ := netlink.ParseAddr("127.0.0.1/8")
		if err := parent.AddrAdd(lk, addr); err != nil {
			return fmt.Errorf("cannot set loopback interface: %w", err)
		}

		if err := netlink.LinkSetUp(lk); err != nil {
			return fmt.Errorf("cannot start lo: %w", err)
		}
	}

	// first pass: the bridges
	for _, node := range nodes {
		net, ok := node.(*subnet)
		if !ok {
			continue
		}

		if net.user {
			ipv := &netlink.IPVlan{
				LinkAttrs: netlink.LinkAttrs{
					Name:   net.name,
					TxQLen: -1,
				},
				Mode: netlink.IPVLAN_MODE_L2,
			}
			if err := addipvlan(parent, origns, net.link, ipv); err != nil {
				return fmt.Errorf("creating network %s: %w", net.name, err)
			}
			if err := lease4(context.Background(), ipv); err != nil {
				return fmt.Errorf("creating network %s: %w", net.name, err)
			}
			continue
		}

		br := &netlink.Bridge{
			LinkAttrs: netlink.LinkAttrs{
				Name:   net.name,
				TxQLen: -1,
			},
		}
		if err := addup(parent, br); err != nil {
			return fmt.Errorf("creating bridge: %w", err)
		}
		if net.host {
			veth := &netlink.Veth{
				LinkAttrs: netlink.LinkAttrs{
					NetNsID:     1,
					Name:        "veth_" + net.name,
					TxQLen:      -1,
					MasterIndex: br.Attrs().Index,
				},
				PeerName: "lab_" + net.name,
			}
			err := addveth(parent, origns, veth,
				func(l netlink.Link) error {
					if net.linkonly {
						return nil
					}

					na := netip.PrefixFrom(last(net.network), net.network.Bits()) // last address always assigned to host
					addr, _ := netlink.ParseAddr(na.String())
					return netlink.AddrAdd(l, addr)
				},
				func(l netlink.Link) error {
					if net.dns.Server == "" {
						return nil
					}
					if err := exec.Command("/usr/bin/resolvectl", "dns", l.Attrs().Name, net.dns.Server).Run(); err != nil {
						return fmt.Errorf("cannot configure dns server: %w", err)
					}
					if net.dns.Domain == "" {
						return nil
					}
					if err := exec.Command("/usr/bin/resolvectl", "domain", l.Attrs().Name, net.dns.Domain).Run(); err != nil {
						return fmt.Errorf("cannot configure dns domain: %w", err)
					}
					return nil
				},
			)
			if err != nil {
				return fmt.Errorf("cannot create host handle: %w", err)
			}
		}
	}

	fmt.Println("\033[38;5;2m- Internal networks created\033[0m")

	// second pass: serial access
	{
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{
				NetNsID: 1,
				Name:    "telnet",
				TxQLen:  -1,
			},
			PeerName: "admin",
		}
		err := addveth(parent, origns, veth, func(peer netlink.Link) error {
			addr, _ := netlink.ParseAddr("169.254.169.1/30")
			return netlink.AddrAdd(peer, addr)
		})
		if err != nil {
			return fmt.Errorf("cannot create host handle: %w", err)
		}

		addr, _ := netlink.ParseAddr("169.254.169.2/30")
		if err := netlink.AddrAdd(veth, addr); err != nil {
			return fmt.Errorf("creating admin handle: %w", err)
		}

	}
	fmt.Println("\033[38;5;2m- Serial over Telnet available\033[0m")

	var errc int
	// third pass: the VMs

	for node := range launchorder(nodes) {
		taps := make(map[string]*os.File)
		for i, iface := range node.ifcs {
			if iface.net.user {
				continue // not creating taps for those
			}

			br, err := parent.LinkByName(iface.net.name)
			if err != nil {
				return fmt.Errorf("cannot find parent bridge %s: %w", iface.net.name, err)
			}
			ifname := fmt.Sprintf("%s_e%d", node.name, i)
			tt := &netlink.Tuntap{
				LinkAttrs: netlink.LinkAttrs{
					Name:        ifname,
					MasterIndex: br.Attrs().Index,
					TxQLen:      -1,
				},
				Mode:   netlink.TUNTAP_MODE_TAP,
				Queues: 1,
			}
			if err := addup(parent, tt); err != nil {
				return fmt.Errorf("creating tap device %w", err)
			}
			taps[iface.name] = tt.Fds[0] // one queue
		}

		// note this run in the same LockOSThread so that network namespace is kept
		if err := RunVM(node, taps); err != nil {
			errc++
			fmt.Printf("\033[38;9;2m ⚠️   cannot create vm %s: %s\033[0m\n", node.name, err)
		}
	}
	fmt.Printf("\033[38;5;2m- Virtual machines started (%d fail) \033[0m\n", errc)

	// forth pass: ansible playbooks
	{
		playbooks, _ := filepath.Glob("playbook_*.yaml")
		if len(playbooks) > 0 {
			fmt.Printf("- Starting %d playbooks\n", len(playbooks))
		}

		var errc atomic.Int32

		var waitbooks sync.WaitGroup

		for _, pb := range playbooks {
			waitbooks.Add(1)
			var bufout, buferr bytes.Buffer
			ans := exec.Command("/usr/bin/ansible-playbook", "-i", "inventory", "--key-file", identity, pb)
			ans.Stderr = &bufout
			ans.Stdout = &buferr

			pb := pb // linter not happy, despite Go 1.22…
			go func() {
				time.Sleep(2 * time.Second) // wait for SSH to start
				if err := ans.Run(); err != nil {
					fmt.Printf("\033[38;9;2m ⚠️   cannot run playbook %s: %s\033[0m\n", pb, err)
					fmt.Println("[stdout]")
					fmt.Println(bufout.String())
					fmt.Println("[stderr]")
					fmt.Println(buferr.String())
					errc.Add(1)
				}
				waitbooks.Done()
			}()
		}
		waitbooks.Wait()
		if len(playbooks) > 0 {
			fmt.Printf("- Playbooks executed (%d fail)\n", errc.Load())
		}
	}

	<-KillChan
	for _, p := range VMS {
		p.Process.Kill()
	}

	return netns.DeleteNamed("lab")
}

// add and set up
func addup(parent *netlink.Handle, lk netlink.Link) error {
	if err := parent.LinkAdd(lk); err != nil {
		return fmt.Errorf("cannot create device %s: %w", lk.Attrs().Name, err)
	}
	if err := parent.LinkSetUp(lk); err != nil {
		return fmt.Errorf("cannot start interface %s: %w", lk.Attrs().Name, err)
	}
	return nil
}

// addveth sets up a veth device in ns1, with lk.Peer in ns2.
// configurations applied to the link after it gets moved to the new namespace.
func addveth(ns1 *netlink.Handle, ns2 netns.NsHandle, lk *netlink.Veth, cfg ...func(netlink.Link) error) error {
	if err := addup(ns1, lk); err != nil {
		return fmt.Errorf("creating host handle: %w", err)
	}
	peer, err := netlink.LinkByName(lk.PeerName)
	if err != nil {
		return fmt.Errorf("creating host handle: cannot find peer: %w", err)
	}

	if err := netlink.LinkSetNsFd(peer, int(ns2)); err != nil {
		return fmt.Errorf("cannot port host handle: %w", err)
	}
	ch, err := netns.Get()
	if err != nil {
		return fmt.Errorf("cannot get current handle")
	}

	if err := netns.Set(ns2); err != nil {
		return fmt.Errorf("cannot switch to host handle: %w", err)
	}
	peer, err = netlink.LinkByName(lk.PeerName)
	if err != nil {
		return fmt.Errorf("cannot find peer after port: %w", err)
	}

	if err := netlink.LinkSetUp(peer); err != nil {
		return fmt.Errorf("creating admin handle: %w", err)
	}
	for _, cfg := range cfg {
		if err := cfg(peer); err != nil {
			return fmt.Errorf("configuring peer handle: %w", err)
		}
	}

	return netns.Set(ch)
}

// addipvlan creates an IPVLAN device in ns1, based on parent from ns2
func addipvlan(ns1 *netlink.Handle, ns2 netns.NsHandle, parent string, vl *netlink.IPVlan, cfg ...func(netlink.Link) error) error {
	ch, err := netns.Get()
	if err != nil {
		return fmt.Errorf("cannot get current handle")
	}

	if err := netns.Set(ns2); err != nil {
		return fmt.Errorf("cannot switch to host handle: %w", err)
	}
	pi, err := netlink.LinkByName(parent)
	if err != nil {
		return fmt.Errorf("invalid parent %s: %w", parent, err)
	}

	vl.ParentIndex = pi.Attrs().Index
	if err := netlink.LinkAdd(vl); err != nil {
		return fmt.Errorf("cannot create IPVLAN: %w", err)
	}

	if err := netlink.LinkSetNsFd(vl, int(ch)); err != nil {
		return fmt.Errorf("cannot port host handle: %w", err)
	}

	if err := netns.Set(ch); err != nil {
		return fmt.Errorf("cannot revert to original namespace: %w", err)
	}

	if err := netlink.LinkSetUp(vl); err != nil {
		return fmt.Errorf("creating admin handle: %w", err)
	}

	return nil
}

var (
	UbuntuImage   = "/var/lib/labomatic/ubuntu-22.04.qcow"
	MikrotikImage = "/var/lib/labomatic/chr-7.14.qcow"

	TmpDir string

	// global variables, the script is not persistent…
	VMS      []*exec.Cmd
	KillChan = make(chan os.Signal)

	TelnetNum = 23 // standard telnet
)

func init() {
	TmpDir, _ = os.MkdirTemp("", "labomatic_")

	signal.Notify(KillChan, os.Interrupt)
}

func RunVM(node *netnode, taps map[string]*os.File) error {
	var base string
	switch node.typ {
	default:
		panic("unknown node type")
	case nodeRouter:
		base = MikrotikImage
	case nodeHost:
		base = UbuntuImage
	}

	vst := filepath.Join(TmpDir, node.name+".qcow2")
	out, err := exec.Command("/usr/bin/qemu-img", "create",
		"-f", "qcow2", "-F", "qcow2",
		"-b", base,
		vst).Output()
	if err != nil {
		slog.Debug("runnig command qemu-img",
			"output", string(out))
		return fmt.Errorf("creating disk: %w", err)
	}

	TelnetNum++
	// TODO initialize cloudinit from templates
	args := []string{
		"-machine", "accel=kvm,type=q35",
		"-cpu", "host",
		"-m", "2G",
		"-nographic",
		"-monitor", "none",
		"-chardev", fmt.Sprintf("socket,id=ga0,host=127.0.10.1,port=%d,server=on,wait=off", TelnetNum),
		"-device", "virtio-serial",
		"-device", fmt.Sprintf("virtserialport,chardev=ga0,name=%s", node.agent().Path()),
		"-serial", fmt.Sprintf("telnet:169.254.169.2:%d,server,wait=off,nodelay=on", TelnetNum),
		"-drive", fmt.Sprintf("if=virtio,format=qcow2,file=%s", vst),
	}
	fmt.Printf("	Connect to \033[38;5;7m%s\033[0m using:    telnet 169.254.169.2 %d\n", node.name, TelnetNum)

	const fdtap = 3 // since stderr / stdout / stdin are passed
	for i, iface := range node.ifcs {
		if iface.net.user {
			args = append(args,
				"-nic", fmt.Sprintf("user,ipv6=off,net=%s,model=virtio,mac=52:54:98:%s", iface.net.network, rndmac()),
			)
		} else {
			args = append(args,
				"-nic", fmt.Sprintf("tap,fd=%d,model=virtio,mac=52:54:00:%s", fdtap+i, rndmac()),
			)
		}
	}
	cm := exec.Command("/usr/bin/qemu-system-x86_64", args...)
	cm.Stderr = os.Stderr
	for _, iface := range node.ifcs {
		cm.ExtraFiles = append(cm.ExtraFiles, taps[iface.name])
	}

	VMS = append(VMS, cm)

	if err := cm.Start(); err != nil {
		return fmt.Errorf("running qemu: %w", err)
	}

	return ExecGuest(TelnetNum, node)
}

func ExecGuest(portnum int, node *netnode) error {
	time.Sleep(2 * time.Second) // give the VM some time to start

	qmp, err := OpenQMP("lab", "tcp", fmt.Sprintf("127.0.10.1:%d", portnum))
	if err != nil {
		return fmt.Errorf("cannot contact qmp: %w", err)
	}

	if err := qmp.Do("guest-ping", nil, nil); err != nil {
		return fmt.Errorf("cannot ping: %w", err)
	}

	dt := node.ToTemplate()

	// wait for interfaces to be up.
	// note we expect the VM to have possibly more interfaces than the template (e.g lo)
waitUp:
	wantnames := make(map[string]bool)
	for _, iface := range dt.Interfaces {
		wantnames[iface.Name] = true
	}
	var GuestNetworkInterface []struct {
		Name string `json:"name"`
	}
	if err := qmp.Do("guest-network-get-interfaces", nil, &GuestNetworkInterface); err != nil {
		return fmt.Errorf("listing interfaces: %w", err)
	}

	for _, iface := range GuestNetworkInterface {
		delete(wantnames, iface.Name)
	}

	if len(wantnames) > 0 {
		time.Sleep(2 * time.Second)
		goto waitUp
	}

	iniscript := node.agent().defaultInit() + node.init
	exp, err := template.New("init").Parse(iniscript)
	if err != nil {
		return fmt.Errorf("invalid init script: %w", err)
	}

	buf := new(bytes.Buffer)
	if err := exp.Execute(buf, dt); err != nil {
		return fmt.Errorf("invalid init script: %w", err)
	}
	slog.Debug("execute on guest", "cmd", buf.String())

	var execresult struct {
		PID int `json:"pid"`
	}
	err = qmp.Do("guest-exec", node.agent().Execute(buf.Bytes()), &execresult)
	if err != nil {
		return fmt.Errorf("running provisioning script: %w", err)
	}
	slog.Debug("exec script returns", "pid", execresult.PID)

	for range 10 {
		var GuestExecStatus struct {
			Exited   bool   `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  []byte `json:"out-data"`
			ErrData  []byte `json:"err-data"`
		}
		err := qmp.Do("guest-exec-status", struct {
			PID int `json:"pid"`
		}{execresult.PID}, &GuestExecStatus)
		if err != nil {
			return fmt.Errorf("canot exec status")
		}

		if GuestExecStatus.Exited {
			if GuestExecStatus.ExitCode == 0 {
				if len(GuestExecStatus.OutData) > 0 {
					fmt.Println("--- script results ---")
					fmt.Println(string(GuestExecStatus.OutData))
					fmt.Println("-----------------")
				}
				return nil
			}
			errdt := GuestExecStatus.ErrData
			if len(errdt) == 0 {
				errdt = GuestExecStatus.OutData
			}
			return fmt.Errorf("Error running script: %s", errdt)
		}
		slog.Debug("exec script did not terminate, continue")
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("could not properly seed machine")
}

func launchorder(nodes starlark.StringDict) iter.Seq[*netnode] {
	return func(yield func(*netnode) bool) {
		order, ok := nodes["boot_order"]
		if ok {
			lo, ok := order.(*starlark.List)
			if !ok {
				panic("boot order must be a starlark list")
			}
			for n := range lo.Elements {
				n, ok := n.(*netnode)
				if !ok {
					continue
				}
				if !yield(n) {
					return
				}
			}
		} else {
			for _, node := range nodes {
				node, ok := node.(*netnode)
				if !ok {
					continue
				}
				if !yield(node) {
					return
				}
			}
		}

	}

}

func rndmac() string {
	mc := make([]byte, 3)
	rand.Read(mc)
	return fmt.Sprintf("%x:%x:%x", mc[0], mc[1], mc[2])
}

// works around different implementations of the agent
type GuestAgent interface {
	Execute(data []byte) any
	Path() string
	defaultInit() string
}

type ubuntu struct{}

func (q ubuntu) Execute(data []byte) any {
	return struct {
		Path          string `json:"path"`
		InputData     []byte `json:"input-data"`
		CaptureOutput bool   `json:"capture-output"`
	}{"/bin/bash", data, true}
}
func (ubuntu) Path() string { return "org.qemu.guest_agent.0" }
func (ubuntu) defaultInit() string {
	return `{{ range .Interfaces }}
{{- if .Address.IsValid }}
sudo ip addr add {{.Address}}/{{.Network.Bits}} dev {{.Name}}
sudo ip link set {{.Name}} up
{{- else if not .LinkOnly }}
sudo dhclient {{.Name}}
{{ end }}
{{ end }}
sudo hostnamectl set-hostname {{.Name}}
echo "{{.Host.PubKey}}" >> /home/ubuntu/.ssh/authorized_keys
`
}

type chr struct{}

func (chr) Path() string { return "chr.provision_agent" }

func (chr) Execute(data []byte) any {
	return struct {
		InputData     []byte `json:"input-data"`
		CaptureOutput bool   `json:"capture-output"`
	}{data, true}
}

func (chr) defaultInit() string {
	return `{{ range .Interfaces }}
{{ if .Address.IsValid }}
/ip/address/add interface={{.Name}} address={{.Address}}/{{.Network.Bits}}
{{ else if not .LinkOnly}}
/ip/dhcp-client/add interface={{.Name}}
{{ end }}
{{ end }}
/system/identity/set name="{{.Name}}"
`
}
