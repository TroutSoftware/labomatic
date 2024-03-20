package labomatic

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
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
		switch net := node.(type) {
		case *subnet:
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
						na := netip.PrefixFrom(last(net.network), net.network.Bits()) // last address always assigned to host
						addr, _ := netlink.ParseAddr(na.String())
						return netlink.AddrAdd(l, addr)
					},
				)
				if err != nil {
					return fmt.Errorf("cannot create host handle: %w", err)
				}
			}
		}
	}

	fmt.Println("\033[38;5;2m- Internal networks created\033[0m")

	// second pass: virtual connectors outside
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
	// second pass: the taps
	for sname, node := range nodes {
		switch node := node.(type) {
		case *netnode:
			taps := make(map[string]*os.File)
			for _, iface := range node.ifcs {
				br, err := parent.LinkByName(iface.net.name)
				if err != nil {
					return fmt.Errorf("cannot find parent bridge %s: %w", iface.net.name, err)
				}
				ifname := fmt.Sprintf("%s_%s", node.name, iface.name)
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
			if err := RunVM(sname, node, taps); err != nil {
				errc++
				fmt.Printf("\033[38;9;2m ⚠️   cannot create vm %s: %s\033[0m\n", node.name, err)
			}
		}
	}

	fmt.Printf("\033[38;5;2m- Virtual machines started (%d fail) \033[0m\n", errc)
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

var (
	UbuntuImage   = "/var/lib/labomatic/ubuntu-22.04.qcow"
	MikrotikImage = "/var/lib/labomatic/chr-7.14.qcow"

	UbuntuGuestAgent   = "org.qemu.guest_agent.0"
	MikrotikGuestAgent = "chr.provision_agent"

	TmpDir string

	// global variables, the script is not persistent…
	VMS      []*exec.Cmd
	KillChan = make(chan os.Signal)

	TelnetNum     = 23 // standard telnet
	LastMac   Mac = 1
)

func init() {
	TmpDir, _ = os.MkdirTemp("", "labomatic_")

	signal.Notify(KillChan, os.Interrupt)
}

func RunVM(vname string, node *netnode, taps map[string]*os.File) error {
	var base, guestagent string
	switch node.typ {
	default:
		panic("unknown node type")
	case nodeRouter:
		base = MikrotikImage
		guestagent = MikrotikGuestAgent
	case nodeHost:
		base = UbuntuImage
		guestagent = UbuntuGuestAgent
	}

	vst := filepath.Join(TmpDir, vname+".qcow2")
	err := exec.Command("/usr/bin/qemu-img", "create",
		"-f", "qcow2", "-F", "qcow2",
		"-b", base,
		vst).Run()
	if err != nil {
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
		"-device", fmt.Sprintf("virtserialport,chardev=ga0,name=%s", guestagent),
		"-serial", fmt.Sprintf("telnet:169.254.169.2:%d,server,wait=off,nodelay=on", TelnetNum),
		"-drive", fmt.Sprintf("if=virtio,format=qcow2,file=%s", vst),
	}
	fmt.Printf("	Connect to \033[38;5;7m%s\033[0m using:    telnet 169.254.169.2 %d\n", node.name, TelnetNum)

	const fdtap = 3 // since stderr / stdout / stdin are passed
	for i, iface := range node.ifcs {
		args = append(args,
			"-nic", fmt.Sprintf("tap,id=%s,fd=%d,model=virtio,mac=52:54:00:%s", iface.name, fdtap+i, LastMac),
		)
		LastMac++
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

	return ExecGuest(TelnetNum, vname+"_init", node.ToTemplate())
}

func ExecGuest(portnum int, tpl string, dt TemplateNode) error {
	time.Sleep(2 * time.Second) // give the VM some time to start

	qmp, err := OpenQMP("lab", "tcp", fmt.Sprintf("127.0.10.1:%d", portnum))
	if err != nil {
		return fmt.Errorf("cannot contact qmp: %w", err)
	}

	if err := qmp.Do("guest-ping", nil, nil); err != nil {
		return fmt.Errorf("cannot ping: %w", err)
	}

	exp, err := template.ParseFiles(tpl)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("invalid template %s: %w", tpl, err)
	}

	buf := new(bytes.Buffer)
	if err := exp.Execute(buf, dt); err != nil {
		return fmt.Errorf("invalid template %s: %w", tpl, err)
	}

	var execresult struct {
		PID int `json:"pid"`
	}
	err = qmp.Do("guest-exec", struct {
		InputData     []byte `json:"input-data"`
		CaptureOutput bool   `json:"capture-output"`
	}{buf.Bytes(), true}, &execresult)
	if err != nil {
		return fmt.Errorf("running provisioning script: %w", err)
	}

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

	for {
		var GuestExecStatus struct {
			Exited   bool   `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  []byte `json:"out-data"`
		}
		err := qmp.Do("guest-exec-status", struct {
			PID int `json:"pid"`
		}{execresult.PID}, &GuestExecStatus)
		if err != nil {
			return fmt.Errorf("canot exec status")
		}

		fmt.Println("results", string(GuestExecStatus.OutData))

		if GuestExecStatus.Exited {
			if GuestExecStatus.ExitCode == 0 {
				return nil
			}
			return fmt.Errorf("Error running script: %s", GuestExecStatus.OutData)
		}
		time.Sleep(2 * time.Second)
	}
}
