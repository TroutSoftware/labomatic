package labomatic

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"

	"github.com/creack/pty/v2"
	"golang.org/x/sys/unix"
)

func UserNumID(u user.User) (uid, gid uint64, err error) {
	uid, err = strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid user id %s: %w", u.Uid, err)
	}
	gid, err = strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid group id %s: %w", u.Gid, err)
	}

	return
}

func RunAsset(runas user.User) (*exec.Cmd, error) {
	uid, gid, err := UserNumID(runas)
	if err != nil {
		return nil, fmt.Errorf("finding unix user %s: %w", runas, err)
	}

	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "sh"
	}

	cmd := exec.Command(sh)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// TODO: chroot to wd or home
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	pty1, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("starting pty: %w", err)
	}

	pty2, tty2, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("starting pty: %w", err)
	}

	if err := unix.Chown(tty2.Name(), int(uid), int(gid)); err != nil {
		return nil, fmt.Errorf("changing tty owner: %w", err)
	}

	fmt.Println("listen on", tty2.Name())

	go io.Copy(pty2, pty1)
	go io.Copy(pty1, pty2)

	return cmd, nil
}
