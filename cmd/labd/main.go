package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/TroutSoftware/labomatic"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"github.com/landlock-lsm/go-landlock/landlock"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func main() {
	verbose := flag.Bool("v", false, "show debug logs")
	flag.StringVar(&labomatic.ImagesDefaultLocation, "images-dir", labomatic.ImagesDefaultLocation, "Default image location")
	flag.Parse()

	if *verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	conn, err := dbus.SystemBus()
	if err != nil {
		log.Fatal(err)
	}

	var lab LabServer

	lab.dbus = conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")

	conn.Export(&lab, "/software/trout/labomatic", "software.trout.labomatic.Lab")
	conn.Export(introspect.Introspectable(intro), "/software/trout/labomatic", "org.freedesktop.DBus.Introspectable")

	reply, err := conn.RequestName("software.trout.labomatic", dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Fatal("cannot request name:", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Fatal("a process is already running at that name")
	}

	landlock.V5.BestEffort().RestrictPaths(
		// access to lab and self
		landlock.RODirs("/usr/lib/labomatic", "/home"),
		landlock.RWDirs("/tmp"),
		landlock.RWDirs("/run/dbus/system_bus_socket"),

		// manage network namespaces
		landlock.RWDirs("/run/netns"),
		landlock.RWDirs(fmt.Sprintf("/proc/%d", os.Getpid())),
		landlock.ROFiles("/usr/sbin/nft", "/usr/bin/resolvectl"),
		landlock.RWFiles("/proc/sys/net/ipv4/ip_forward"),
	)

	wait := make(chan os.Signal, 1)
	signal.Notify(wait, os.Interrupt)
	<-wait
}

type LabServer struct {
	ctrl chan labomatic.Controller

	dbus dbus.BusObject

	once sync.Mutex
}

func (l *LabServer) Start(sdr dbus.Sender, labdir, workdir string) *dbus.Error {
	l.once.Lock()
	defer l.once.Unlock()

	var runas user.User
	{
		c := l.dbus.Call("GetConnectionUnixUser", 0, sdr)
		if c.Err != nil {
			return dbus.MakeFailedError(fmt.Errorf("cannot identify calling user: %w", c.Err))
		}
		uid := c.Body[0].(uint32)
		found, err := user.LookupId(strconv.Itoa(int(uid)))
		if err != nil {
			return dbus.MakeFailedError(fmt.Errorf("invalid user %d: %w", uid, err))
		}
		runas = *found
	}

	full := filepath.Join(labdir, "conf.star")

	var th starlark.Thread
	th.SetLocal("workdir", workdir)

	cnf, err := starlark.ExecFileOptions(&syntax.FileOptions{
		TopLevelControl: true,
		Set:             true,
		GlobalReassign:  true,
	}, &th, full, nil, labomatic.NetBlocks)

	if err != nil {
		return dbus.MakeFailedError(fmt.Errorf("cannot parse %s: %w", full, err))
	}

	msg := make(chan string)
	go func() {
		for msg := range msg {
			fmt.Println("message ", msg)
		}
	}()

	ready := make(chan chan labomatic.Controller)
	if err := labomatic.Build(cnf, runas, msg, ready); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("cannot build %s: %w", full, err))
	}
	l.ctrl = <-ready

	return nil
}

func (l *LabServer) Status() (string, *dbus.Error) {
	l.once.Lock()
	defer l.once.Unlock()

	if l.ctrl == nil {
		return "", nil
	}

	var view strings.Builder
	done := make(chan struct{})
	l.ctrl <- labomatic.FormatTable(&view, done)
	<-done
	return view.String(), nil
}

func (l *LabServer) Stop() *dbus.Error {
	l.once.Lock()
	defer l.once.Unlock()

	if l.ctrl != nil {
		l.ctrl <- labomatic.TermLab
		close(l.ctrl)
		l.ctrl = nil
	}
	return nil
}

const intro = `
<node>
	<interface name="software.trout.labomatic.Lab">
		<method name="Start">
			<arg direction="in" type="s"/>
			<arg direction="in" type="s"/>
		</method>
		<method name="Stop">
		</method>
	</interface>` + introspect.IntrospectDataString + `</node> `
