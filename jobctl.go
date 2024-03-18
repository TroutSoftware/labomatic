package labomatic

import (
	"fmt"
	"os"
	"os/exec"
)

func StartUbuntuVM() error {
	fh, err := os.OpenFile("/dev/tap20", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("cannot open tab device!")
	}
	cm := exec.Command("/usr/bin/qemu-system-x86_64",
		"-machine", "accel=kvm,type=q35",
		"-cpu", "host",
		"-m", "2G",
		"-nographic",
		"-device", "virtio-net-pci,netdev=eno1,bus=pci.1,addr=0x0,mac=52:54:00:17:c0:74",
		"-netdev", fmt.Sprintf("tap,id=eno1,fd=%d", fh.Fd()),
		"-drive", "if=virtio,format=qcow2,file=/home/romain/Téléchargements/ubuntu-server.qcow")
	cm.Stderr = os.Stderr
	cm.Stdout = os.Stdout
	cm.Stdin = os.Stdin
	cm.ExtraFiles = append(cm.ExtraFiles, fh)

	return cm.Run()
}
