package labomatic

import (
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"slices"
	"text/template"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"go.starlark.net/starlark"
	"golang.org/x/sys/unix"
)

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
func Build(nodes starlark.StringDict, runas user.User, msg chan<- string, ready chan chan Controller) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	nsdefault, err := netns.Get()
	if err != nil {
		return fmt.Errorf("cannot get handle to existing namespace: %w", err)
	}

	nslab, err := netns.NewNamed("lab")
	if err != nil {
		return fmt.Errorf("cannot create lab namespace: %w", err)
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

		fh, err := os.CreateTemp(TmpDir, "nft_add")
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

	// second pass: the VMs

	{
		user, group, err := UserNumID(runas)
		if err != nil {
			return fmt.Errorf("cannot read user id %s: %w", runas, err)
		}
		unix.Chown(TmpDir, int(user), int(group))
	}
	var errc int
	var VMS []RunningNode

	for node := range nodesof(nodes,
		OfType(nodeAsset), OfType(nodeSwitch), OfType(nodeRouter)) {
		msg <- fmt.Sprintf("<D> starting VM %s", node.name)
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
		cm, err := RunVM(node, taps, runas)
		if cm != nil {
			VMS = append(VMS, RunningNode{node: node, cmd: cm})
		}
		if err != nil {
			errc++
			msg <- fmt.Sprintf("<E>cannot create vm %s: %s", node.name, err)
		}
	}
	msg <- fmt.Sprintf("<I>Virtual machines started (%d failed)", errc)

	go func() {
		term := make(chan Controller)
		ready <- term

		for f := range term {
			f(slices.Values(VMS))
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
	CyberOSImage          = "csw.img"

	TmpDir string

	TelnetNum = 23 // standard telnet
)

func init() {
	TmpDir, _ = os.MkdirTemp("", "labomatic_")
}
