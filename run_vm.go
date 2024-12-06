package labomatic

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
	"text/template"
	"time"

	"golang.org/x/sys/unix"
)

// RunVM starts the given node as virtual machine.
// If an error is returned, but a non-nil command is returned, the command must be properly terminated.
func RunVM(node *netnode, taps map[string]*os.File, runas user.User) (*exec.Cmd, error) {
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

	uid, gid, err := UserNumID(runas)
	if err != nil {
		return nil, fmt.Errorf("invalid user id %s: %w", runas.Uid, err)
	}

	serialpipe := filepath.Join(TmpDir, "serial_"+node.name)
	{
		// chown allow qemu access to the image
		// TODO check if we need the out/in, or if qemu can create it on its own
		err := errors.Join(
			unix.Chown(TmpDir, int(uid), int(gid)),
			unix.Chown(vst, int(uid), int(gid)),
			unix.Mkfifo(serialpipe+".in", 0700),
			unix.Chown(serialpipe+".in", int(uid), int(gid)),
			unix.Mkfifo(serialpipe+".out", 0700),
			unix.Chown(serialpipe+".out", int(uid), int(gid)),
		)
		if err != nil {
			return nil, fmt.Errorf("creating VM communication structures: %w", err)
		}
	}

	// TODO move to unix socket for guest agent
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
		"-serial", fmt.Sprintf("pty"),
		"-drive", fmt.Sprintf("format=qcow2,file=%s", vst),
	}
	if node.uefi {
		args = append(args, "-drive", "if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/ovmf/OVMF.fd")
	}
	if node.media != "" {
		args = append(args, "-drive", fmt.Sprintf("if=none,id=backup,format=raw,file=%s", node.media))
	}

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
	cm.Stdout = os.Stdout

	cm.SysProcAttr = &syscall.SysProcAttr{
		Credential:  &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
		AmbientCaps: []uintptr{unix.CAP_NET_BIND_SERVICE}, // telnet connector
	}
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
