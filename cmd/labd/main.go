package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"github.com/TroutSoftware/labomatic"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func main() {
	// labdir := flag.String("d", "", "name of the lab setup directory")
	verbose := flag.Bool("v", false, "show debug logs")
	flag.StringVar(&labomatic.ImagesDefaultLocation, "images-dir", labomatic.ImagesDefaultLocation, "Default image location")
	flag.Parse()

	if *verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	conn, err := dbus.SessionBus()
	if err != nil {
		log.Fatal(err)
	}

	var lab LabServer
	conn.Export(&lab, "/software/trout/labomatic/Lab", "software.trout.labomatic.Lab")
	conn.Export(introspect.Introspectable(intro), "/software/trout/labomatic/Lab", "org.freedesktop.DBus.Introspectable")

	reply, err := conn.RequestName("software.trout.labomatic.Lab", dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Fatal("cannot request name:", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Fatal("a process is already running at that name")
	}

	log.Println("name requested, all good")

	// do we want watchdog??

	wait := make(chan os.Signal, 1)
	signal.Notify(wait, os.Interrupt)
	<-wait
}

type LabServer struct {
	th   starlark.Thread
	kill chan struct{}

	once sync.Mutex
}

func (l *LabServer) Start(dir string) *dbus.Error {
	l.once.Lock()
	defer l.once.Unlock()

	full := filepath.Join(dir, "conf.star")

	cnf, err := starlark.ExecFileOptions(&syntax.FileOptions{
		TopLevelControl: true,
		Set:             true,
		GlobalReassign:  true,
	}, &l.th, full, nil, labomatic.NetBlocks)

	if err != nil {
		return dbus.MakeFailedError(fmt.Errorf("cannot parse %s: %w", full, err))
	}

	msg := make(chan string)
	go func() {
		for msg := range msg {
			fmt.Println("message ", msg)
		}
	}()

	l.kill = make(chan struct{})
	if err := labomatic.Build(cnf, msg, l.kill); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("cannot build %s: %w", full, err))
	}

	return nil
}

func (l *LabServer) Stop() *dbus.Error {
	l.once.Lock()
	defer l.once.Unlock()

	if l.kill != nil {
		close(l.kill)
		l.kill = nil
	}
	return nil
}

const intro = `
<node>
	<interface name="software.trout.labomatic.Lab">
		<method name="Start">
			<arg direction="in" type="s"/>
		</method>
		<method name="Stop">
		</method>
	</interface>` + introspect.IntrospectDataString + `</node> `
