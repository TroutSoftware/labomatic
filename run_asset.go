package labomatic

import (
	"context"
	"fmt"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"syscall"

	"github.com/creack/pty/v2"
	"github.com/vishvananda/netns"
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

func RunAsset(ctx context.Context, name string, runas user.User) (int32, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	uid, gid, err := UserNumID(runas)
	if err != nil {
		return -1, fmt.Errorf("finding unix user %s: %w", runas, err)
	}

	hdl, err := netns.GetFromName(name)
	if err != nil {
		return -1, fmt.Errorf("no such namespace %s: %w", name, err)
	}
	netns.Set(hdl)

	cmd := exec.Command("/bin/bash")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	pty, err := pty.Start(cmd)
	if err != nil {
		return -1, err
	}

	return int32(pty.Fd()), nil
}
