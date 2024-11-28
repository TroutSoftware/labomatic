package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/godbus/dbus/v5"
)

func main() {
	basedir := flag.String("w", "", "name of the base directory for images (working directory by default)")
	flag.Parse()

	action := flag.Arg(0)
	labdir := flag.Arg(1)
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal("cannot get working directory", err)
	}

	if *basedir == "" {
		*basedir = wd
	}

	bus, err := dbus.SystemBus()
	if err != nil {
		log.Fatal("cannot connect to DBus:", err)
	}

	lab := bus.Object("software.trout.labomatic", "/software/trout/labomatic")
	switch action {
	default:
		fmt.Println("unknown action: use \"start\" or \"stop\"")
		os.Exit(1)
	case "start":
		if labdir == "" {
			fmt.Println("invalid usage: want \"start\" <lab>")
			os.Exit(1)
		}
		if !filepath.IsAbs(labdir) {
			labdir = filepath.Join(wd, labdir)
		}

		call := lab.CallWithContext(context.TODO(), "Start", dbus.FlagAllowInteractiveAuthorization,
			labdir, *basedir)
		if call.Err != nil {
			fmt.Println("error starting the lab:", call.Err)
			os.Exit(1)
		}
	case "status":
		call := lab.CallWithContext(context.TODO(), "Status", dbus.FlagAllowInteractiveAuthorization)
		if call.Err != nil {
			fmt.Println("cannot read lab status:", call.Err)
			os.Exit(1)
		}
		fmt.Println(call.Body[0].(string))
	case "stop":
		call := lab.CallWithContext(context.TODO(), "Stop", dbus.FlagAllowInteractiveAuthorization)
		if call.Err != nil {
			fmt.Println("error stopping the lab:", call.Err)
			os.Exit(1)
		}
	}
}
