// evolution of labomatic: run VMs on dedicated hardware
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
)

func main() {
	install := flag.String("iso", "", "iso image to boot from")
	blockdev := flag.String("hda", "", "block device")
	nic := flag.String("nic", "eno1", "")
	flag.Parse()

	// create network interfaces

	args := []string{
		"-machine", "accel=kvm,type=q35",
		"-cpu", "host",
		"-smp", "4",
		"-m", "8G",
		"-nographic",
		"-vnc", "display",
		"-object", "rng-random,id=rng0,filename=/dev/urandom", "-device", "virtio-rng-pci,rng=rng0",

		// hard-drive
		"-device", "virtio-scsi-pci,id=scsi0",
		"-drive", fmt.Sprintf("file=%s,if=none,format=raw,discard=unmap,aio=native,cache=none,id=sda", *blockdev),
		"-device", "scsi-hd,drive=sda,bus=scsi0.0",

		// cdrom
		// "-cdrom", *install,
		// "-boot", "d",

		// nic
		"-nic", "tap,model=virtio,mac=42:70:cb:41:9a:e4,fd=3",
	}

	thetap, err := os.OpenFile("/dev/tap17", os.O_RDWR, 0600)
	if err != nil {
		log.Fatal("opening tap device", err)
	}

	cm := exec.Command("/usr/bin/qemu-system-x86_64", args...)
	cm.Stderr = os.Stderr
	cm.ExtraFiles = append(cm.ExtraFiles, thetap)

	if err := cm.Run(); err != nil {
		log.Fatal(err)
	}
}
