package labomatic

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
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

var masquerade_rule = template.Must(template.New("nft_masquerade").Parse(`
table inet labomatic
delete table inet labomatic

table inet labomatic {
	chain forward {
		type filter hook forward priority filter; policy accept;
		{{ range .Interfaces }}
		iifname "{{.}}" counter meta mark set mark and 0xff00ffff xor 0x80000
		{{ end }}
	}
	chain nat {
		type nat hook postrouting priority srcnat; policy accept;
		meta mark & 0x00ff0000 == 0x80000 counter masquerade
	}
}
`))

// Build creates the full virtual lab from the Starlark definitions.
// Read status from msg to follow progress (or have a goroutine ignore all messages if not intersted).
// The term channel can be closed to terminate all current instances.
func Build(nodes starlark.StringDict, msg chan<- string, ready chan chan Controller) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	nsdefault, err := netns.Get()
	if err != nil {
		return fmt.Errorf("cannot get handle to existing namespace: %w", err)
	}

	nslab, err := netns.NewNamed("lab")
	if err != nil {
		return fmt.Errorf("cannot create namespace: %w", err)
	}

	msg <- "<I>Building the Lab"

	{
		lk, err := netlink.NewHandleAt(nslab)
		if err != nil {
			return fmt.Errorf("obtaining netlink handle: %w", err)
		}

		lo, err := lk.LinkByName("lo")
		if err != nil {
			return fmt.Errorf("no local interface in netns: %w", err)
		}
		addr, _ := netlink.ParseAddr("127.0.0.1/8")
		if err := lk.AddrAdd(lo, addr); err != nil {
			return fmt.Errorf("cannot set loopback interface: %w", err)
		}

		if err := netlink.LinkSetUp(lo); err != nil {
			return fmt.Errorf("cannot start lo: %w", err)
		}
	}

	// first pass: the bridges
	var nated []string
	for net := range netsof(nodes) {
		br := &netlink.Bridge{
			LinkAttrs: netlink.LinkAttrs{
				Name:   net.name,
				TxQLen: -1,
			},
		}
		if err := addup(nslab, br); err != nil {
			return fmt.Errorf("creating bridge: %w", err)
		}
		if !net.host {
			continue
		}

		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{
				NetNsID:     1,
				Name:        "veth_" + net.name,
				TxQLen:      -1,
				MasterIndex: br.Attrs().Index,
			},
			PeerName: "lab_" + net.name,
		}
		err := addveth(nslab, nsdefault, veth,
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
		if net.nat {
			nated = append(nated, "lab_"+net.name)
		}
	}

	if len(nated) > 0 {
		revert, err := switchns(nsdefault)
		if err != nil {
			return fmt.Errorf("cannot switch to main ns: %w", err)
		}
		if err := writeSysctl("/proc/sys/net/ipv4/ip_forward", "1"); err != nil {
			return fmt.Errorf("cannot enable IP forwarding: %w", err)
		}

		fh, err := os.CreateTemp("", "nft_add")
		if err != nil {
			return fmt.Errorf("cannot create temp file: %w", err)
		}
		if err := masquerade_rule.Execute(fh, struct{ Interfaces []string }{nated}); err != nil {
			return fmt.Errorf("cannot execute rule, %w", err)
		}
		fh.Close()

		if err := exec.Command("/usr/sbin/nft", "-f", fh.Name()).Run(); err != nil {
			return fmt.Errorf("cannot configure masquerade: %w", err)
		}
		revert()
	}

	msg <- "<I>Internal networks created"

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
		err := addveth(nslab, nsdefault, veth, func(peer netlink.Link) error {
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
	msg <- "<I>Serial over Telnet available"

	// third pass: the VMs
	var errc int
	var VMS []VMNode

	for node := range nodesof(nodes, OfType(nodeRouter), OfType(nodeSwitch)) {
		taps := make(map[string]*os.File)
		for i, iface := range node.ifcs {
			lk, err := netlink.NewHandleAt(nslab)
			if err != nil {
				return fmt.Errorf("obtaining netlink handle: %w", err)
			}

			br, err := lk.LinkByName(iface.net.name)
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
			if err := addup(nslab, tt); err != nil {
				return fmt.Errorf("creating tap device %w", err)
			}
			taps[iface.name] = tt.Fds[0] // one queue
		}

		// note this run in the same LockOSThread so that network namespace is kept
		cm, err := RunVM(node, taps)
		if cm != nil {
			VMS = append(VMS, VMNode{node: node, cmd: cm})
		}
		if err != nil {
			errc++
			msg <- fmt.Sprintf("<E>cannot create vm %s: %s", node.name, err)
		}
	}
	msg <- fmt.Sprintf("<I>Virtual machines started (%d failed)", errc)

	// fourth pass: the assets
	for node := range nodesof(nodes, OfType(nodeAsset)) {
		ns, err := netns.NewNamed(node.name)
		if err != nil {
			return fmt.Errorf("cannot create namespace: %w", err)
		}

		for i, iface := range node.ifcs {
			veth := &netlink.Veth{
				LinkAttrs: netlink.LinkAttrs{
					NetNsID: 1,
					Name:    fmt.Sprintf("eth%d", i),
					TxQLen:  -1,
				},
				PeerName: "ve_" + node.name,
			}
			err := addveth(ns, nslab, veth, func(peer netlink.Link) error {
				mstr, err := netlink.LinkByName(iface.net.name)
				if err != nil {
					return fmt.Errorf("joining network %s: %w", iface.net.name, err)
				}
				return netlink.LinkSetMaster(peer, mstr)
			})
			if err != nil {
				return fmt.Errorf("cannot joing network: %w", err)
			}

			if iface.addr.IsValid() {
				addr, err := netlink.ParseAddr(netip.PrefixFrom(iface.addr.Addr(), iface.net.network.Bits()).String())
				if err != nil {
					return fmt.Errorf("invalid interface address %s: %w", iface.addr, err)
				}
				if err := netlink.AddrAdd(veth, addr); err != nil {
					return fmt.Errorf("assigning address %s: %w", iface.addr, err)
				}
			}

			msg <- fmt.Sprintf("<I>Connect to %s using: ip netns exec %s bash", node.name, node.name)

		}
	}
	msg <- ("<I>Assets namespaces started")

	go func() {

		term := make(chan Controller)
		ready <- term
		nodes := func(yield func(n RunningNode) bool) {
			for _, nn := range VMS {
				if !yield(nn) {
					return
				}
			}
			for nn := range nodesof(nodes, OfType(nodeAsset)) {
				if !yield((*AssetNode)(nn)) {
					return
				}
			}
		}

		for f := range term {
			f(nodes)
		}
		if err := netns.DeleteNamed("lab"); err != nil {
			slog.Warn("cannot delete lab netns", "errors", err)
		}

		TelnetNum = 23 // need a stricter write barrier, this is racy
	}()
	return nil
}

// add and set up
func addup(parent netns.NsHandle, lk netlink.Link) error {
	link, err := netlink.NewHandleAt(parent)
	if err != nil {
		return fmt.Errorf("opening namespace: %w", err)
	}
	if err := link.LinkAdd(lk); err != nil {
		return fmt.Errorf("cannot create device %s: %w", lk.Attrs().Name, err)
	}
	if err := link.LinkSetUp(lk); err != nil {
		return fmt.Errorf("cannot start interface %s: %w", lk.Attrs().Name, err)
	}
	return nil
}

// addveth sets up a veth device in ns1, with lk.Peer in ns2.
// configurations applied to the link after it gets moved to the new namespace.
func addveth(ns1, ns2 netns.NsHandle, lk *netlink.Veth, cfg ...func(netlink.Link) error) error {
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

	revert, err := switchns(ns2)
	if err != nil {
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

	return revert()
}

func switchns(ns netns.NsHandle) (revert func() error, err error) {
	cns, err := netns.Get()
	if err != nil {
		return nil, fmt.Errorf("cannot get current handle")
	}

	if err := netns.Set(ns); err != nil {
		return nil, fmt.Errorf("cannot switch to host handle: %w", err)
	}

	return func() error { return netns.Set(cns) }, nil
}

func writeSysctl(path string, value string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("could not open the sysctl file %s: %s",
			path, err)
	}
	defer f.Close()
	if _, err := io.WriteString(f, value); err != nil {
		return fmt.Errorf("could not write to the systctl file %s: %s",
			path, err)
	}
	return nil
}

var (
	ImagesDefaultLocation = "/usr/lib/labomatic"
	MikrotikImage         = "routeros.img"
	CyberOSImage          = "csw-2407.qcow"

	TmpDir string

	TelnetNum = 23 // standard telnet
)

func init() {
	TmpDir, _ = os.MkdirTemp("", "labomatic_")
}

// RunVM starts the given node as virtual machine.
// If an error is returned, but a non-nil command is returned, the command must be properly terminated.
func RunVM(node *netnode, taps map[string]*os.File) (*exec.Cmd, error) {
	base := node.image
	if base == "" {
		switch node.typ {
		default:
			panic("unknown node type")
		case nodeRouter:
			base = MikrotikImage
		case nodeSwitch:
			base = CyberOSImage
		}
	}
	if !filepath.IsAbs(base) {
		// TODO(rdo) take this from command-line via DBUS instead
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot get current working dir: %w", err)
		}
		if _, err := os.Stat(filepath.Join(ImagesDefaultLocation, base)); err == nil {
			base = filepath.Join(ImagesDefaultLocation, base)
		} else if _, err := os.Stat(filepath.Join(wd, base)); err == nil {
			base = filepath.Join(wd, base)
		} else {
			return nil, fmt.Errorf("image %s cannot be found in default location [%s,%s]", base, ImagesDefaultLocation, wd)
		}
	}

	vst := filepath.Join(TmpDir, node.name+".qcow2")
	_, err := exec.Command("/usr/bin/qemu-img", "create",
		"-f", "qcow2", "-F", "qcow2",
		"-b", base,
		vst).Output()
	if err != nil {
		var perr *exec.ExitError
		if errors.As(err, &perr) {
			os.Stderr.Write(perr.Stderr)
		}
		return nil, fmt.Errorf("creating disk: %w", err)
	}

	TelnetNum++
	args := []string{
		"-machine", "accel=kvm,type=q35",
		"-cpu", "host",
		"-m", "512",
		"-nographic",
		"-monitor", "none",
		"-device", "virtio-rng-pci",
		"-chardev", fmt.Sprintf("socket,id=ga0,host=127.0.10.1,port=%d,server=on,wait=off", TelnetNum),
		"-device", "virtio-serial",
		"-device", fmt.Sprintf("virtserialport,chardev=ga0,name=%s", node.agent().Path()),
		"-serial", fmt.Sprintf("telnet:169.254.169.2:%d,server,wait=off,nodelay=on", TelnetNum),
		"-drive", fmt.Sprintf("format=qcow2,file=%s", vst),
	}
	if node.uefi {
		args = append(args, "-drive", "if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/ovmf/OVMF.fd")
	}
	if node.media != "" {
		args = append(args, "-drive", fmt.Sprintf("if=none,id=backup,format=raw,file=%s", node.media))
	}

	fmt.Printf("	Connect to \033[38;5;7m%s\033[0m using:    telnet 169.254.169.2 %d\n", node.name, TelnetNum)

	const fdtap = 3 // since stderr / stdout / stdin are passed
	if len(node.ifcs) == 0 {
		args = append(args, "-nic", "none")
	}
	for i := range node.ifcs {
		args = append(args,
			"-nic", fmt.Sprintf("tap,fd=%d,model=e1000,mac=52:54:00:%s", fdtap+i, rndmac()),
		)
	}
	cm := exec.Command("/usr/bin/qemu-system-x86_64", args...)
	cm.Stderr = os.Stderr
	for _, iface := range node.ifcs {
		cm.ExtraFiles = append(cm.ExtraFiles, taps[iface.name])
	}

	if err := cm.Start(); err != nil {
		return nil, fmt.Errorf("running qemu: %w", err)
	}

	if err := ExecGuest(TelnetNum, node); err != nil {
		return cm, err
	}

	return cm, nil
}

func ExecGuest(portnum int, node *netnode) error {
	// we need to wait for QEMU to set up the agent socket before asking
	// to early in the boot, and we never get an answer
	// later, but still before the agent respond, and we need to spin sending messages, but the agent will replay them
	// boot time show that the device is usually up in ~2 seconds
	time.Sleep(1 * time.Second)
	if node.typ == nodeSwitch {
		return nil // TODO(rdo) build better
	}

	qemuAgent, err := OpenQMP("lab", "tcp", fmt.Sprintf("127.0.10.1:%d", portnum))
	if err != nil {
		return fmt.Errorf("cannot contact qmp: %w", err)
	}
	defer qemuAgent.Close()

	dt := node.ToTemplate()

	// wait for interfaces to be up.
	// note we expect the VM to have possibly more interfaces than the template (e.g lo)
	tries := 5
waitUp:
	slog.Debug("wait for interfaces to be up",
		"node", node.name,
		"attempt", 6-tries)
	wantnames := make(map[string]bool)
	for _, iface := range dt.Interfaces {
		wantnames[iface.Name] = true
	}
	var GuestNetworkInterface []struct {
		Name            string `json:"name"`
		HardwareAddress string `json:"hardware-address"`
	}
	if err := qemuAgent.Do("guest-network-get-interfaces", nil, &GuestNetworkInterface); errors.Is(err, os.ErrDeadlineExceeded) {
		if tries--; tries == 0 {
			return fmt.Errorf("timeout waiting for interfaces")
		}
		time.Sleep(2 * time.Second << (5 - tries))
		goto waitUp
	} else if err != nil {
		return fmt.Errorf("listing interfaces: %w", err)
	}

	for _, iface := range GuestNetworkInterface {
		delete(wantnames, iface.Name)
	}

	if len(wantnames) > 0 {
		if tries--; tries == 0 {
			return fmt.Errorf("timeout waiting for interfaces")
		}
		time.Sleep(2 * time.Second << (5 - tries))
		goto waitUp
	}

	iniscript := node.agent().defaultInit() + node.init
	exp, err := template.New("init").Funcs(template.FuncMap{
		"last_address": last,
	}).Parse(iniscript)
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
	err = qemuAgent.Do("guest-exec", node.agent().Execute(buf.Bytes()), &execresult)
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
		err := qemuAgent.Do("guest-exec-status", struct {
			PID int `json:"pid"`
		}{execresult.PID}, &GuestExecStatus)
		if err != nil {
			return fmt.Errorf("cannot read exec status: %w", err)
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
{{- if .NATed}}
/ip/route/add dst-address=0.0.0.0/0 gateway={{ last_address .Network }}
/ip/dns/set servers=9.9.9.9,149.112.112.112
{{ end }}
{{ else if not .LinkOnly}}
/ip/dhcp-client/add interface={{.Name}}
{{ end }}
{{ end }}
/system/identity/set name="{{.Name}}"
`
}

type csw struct{}

func (csw) Path() string { return "cyberos.provision_agent" }
func (csw) Execute(data []byte) any {
	return struct {
		Path    string `json:"path"`
		Content string `json:"input-data"`
	}{"<script>", string(data)}
}

func (csw) defaultInit() string {
	return `{{ range .Interfaces }}
{{ if .Address.IsValid }}
PUT netcfg /ip/address/add interface={{.Name}} address={{.Address}}/{{.Network.Bits}}
{{ end }}
{{ end }}
`
}
