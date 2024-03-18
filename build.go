package labomatic

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

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
	for _, node := range nodes {
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
			if err := RunVM(node, taps); err != nil {
				return fmt.Errorf("cannot create vm %s: %w", node.name, err)
			}
		}

	}

	time.Sleep(100 * time.Second)
	return nil
}

var (
	UbuntuImage = "/home/romain/Téléchargements/ubuntu.qcow"

	TmpDir string
)

func init() {
	TmpDir, _ = os.MkdirTemp("", "labomatic_")
}

func RunVM(node *netnode, taps map[string]*os.File) error {
	vst := filepath.Join(TmpDir, node.name+".qcow2")
	err := exec.Command("/usr/bin/qemu-img", "create",
		"-f", "qcow2", "-F", "qcow2",
		"-b", UbuntuImage,
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
		"-drive", fmt.Sprintf("if=virtio,format=qcow2,file=%s", vst),
	}
	const fdtap = 3 // since stderr / stdout / stdin are passed
	for _, iface := range node.ifcs {
		args = append(args,
			"-nic", fmt.Sprintf("tap,id=%s,fd=%d,model=virtio", iface.name, fdtap),
		)
	}
	cm := exec.Command("/usr/bin/qemu-system-x86_64", args...)
	cm.Stderr = os.Stderr
	cm.Stdout = os.Stdout
	cm.Stdin = os.Stdin
	for _, iface := range node.ifcs {
		cm.ExtraFiles = append(cm.ExtraFiles, taps[iface.name])
	}

	return cm.Start()
}
