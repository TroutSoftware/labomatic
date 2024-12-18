package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	fstab := []mount{
		{"proc", "/proc", "proc", 0},
		{"sys", "/sys", "sysfs", 0},
		{"udev", "/dev", "devtmpfs", 0},
		{"tmpfs", "/tmp", "tmpfs", 0},
	}

	for _, m := range fstab {
		err := unix.Mount(m.src, m.target, m.fstype, uintptr(m.options), "")
		if err != nil {
			log.Fatal("cannout mount ", m.src, ":", err)
		}
	}

	if err := exec.Command("/bin/busybox",
		"--install", "-s").Run(); err != nil {
		log.Panic("cannot install busybox", err)
	}

	// busyloop to wait for ttyS0 to be present
	for i := range 5 {
		if _, err := os.Stat("/dev/ttyS0"); err == nil {
			break
		}
		time.Sleep((2 + 2<<i) * time.Second)
	}

	go func() {
		c := exec.Command("/sbin/kragent")

		aglog, err := os.Create("/tmp/kragent_log")
		if err == nil {
			c.Stdout = aglog
			c.Stderr = aglog
		}
		if err := c.Run(); err != nil {
			log.Println("agent failure")
		}
		if aglog != nil {
			aglog.Close()
		}
	}()

	cmd := exec.Command("/bin/busybox", "getty", "115200", "/dev/ttyS0")
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	cmd.Run()

	fmt.Println("Goodbye!")

	unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

type mount struct {
	src, target string
	fstype      string
	options     int
}
