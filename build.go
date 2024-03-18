package labomatic

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"go.starlark.net/starlark"
)

func Build(nodes starlark.StringDict) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
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
			if err := parent.LinkAdd(br); err != nil {
				return fmt.Errorf("cannot create bridge %s: %w", net.name, err)
			}
			if err := parent.LinkSetUp(br); err != nil {
				return fmt.Errorf("cannot start the bridge %s: %w", net.name, err)
			}
		}
	}

	fmt.Println("\033[38;5;2m- Nets are up and running!\033[0m")

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
				if err = parent.LinkAdd(tt); err != nil {
					return fmt.Errorf("cannot create tap device %s: %w", ifname, err)
				}
				if err := parent.LinkSetUp(tt); err != nil {
					return fmt.Errorf("cannot start the bridge %s: %w", ifname, err)
				}
				taps[iface.name] = tt.Fds[0] // one queue
			}

			// note this run in the same LockOSThread so that network namespace is kept
			if err := RunVM(sname, node, taps); err != nil {
				return fmt.Errorf("cannot create vm %s: %w", node.name, err)
			}
		}
	}
	<-KillChan
	for _, p := range VMS {
		p.Process.Kill()
	}

	return netns.DeleteNamed("lab")
}

var (
	UbuntuImage   = "/var/lib/labomatic/ubuntu-22.04.qcow"
	MikrotikImage = "/var/lib/labomatic/chr-7.14.qcow"

	TmpDir string

	VMS      []*exec.Cmd
	KillChan = make(chan os.Signal)

	TelNum = 4321 // gross
)

func init() {
	TmpDir, _ = os.MkdirTemp("", "labomatic_")

	signal.Notify(KillChan, os.Interrupt)
}

func RunVM(vname string, node *netnode, taps map[string]*os.File) error {
	var base string
	switch node.typ {
	default:
		panic("unknown node type")
	case nodeRouter:
		base = MikrotikImage
	case nodeHost:
		base = UbuntuImage
	}

	vst := filepath.Join(TmpDir, vname+".qcow2")
	err := exec.Command("/usr/bin/qemu-img", "create",
		"-f", "qcow2", "-F", "qcow2",
		"-b", base,
		vst).Run()
	if err != nil {
		return fmt.Errorf("creating disk: %w", err)
	}

	// TODO initialize cloudinit from templates
	args := []string{
		"-machine", "accel=kvm,type=q35",
		"-cpu", "host",
		"-m", "2G",
		"-nographic",
		"-monitor", "none",
		"-chardev", fmt.Sprintf("socket,id=ga0,path=/tmp/%s,server=on,wait=off", vname),
		"-serial", fmt.Sprintf("telnet:127.0.0.1:%d,server", TelNum),
		"-drive", fmt.Sprintf("if=virtio,format=qcow2,file=%s", vst),
	}
	TelNum++
	const fdtap = 3 // since stderr / stdout / stdin are passed
	for i, iface := range node.ifcs {
		args = append(args,
			"-nic", fmt.Sprintf("tap,id=%s,fd=%d,model=virtio", iface.name, fdtap+i),
		)
	}
	cm := exec.Command("/usr/bin/qemu-system-x86_64", args...)
	cm.Stderr = os.Stderr
	cm.Stdout = os.Stdout
	cm.Stdin = os.Stdin
	for _, iface := range node.ifcs {
		cm.ExtraFiles = append(cm.ExtraFiles, taps[iface.name])
	}

	VMS = append(VMS, cm)

	return cm.Start()
}
