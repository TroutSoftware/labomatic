package main

import (
	"flag"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/TroutSoftware/labomatic"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func main() {
	labdir := flag.String("d", "", "name of the lab setup directory")
	verbose := flag.Bool("v", false, "show debug logs")
	flag.StringVar(&labomatic.ImagesDefaultLocation, "images-dir", labomatic.ImagesDefaultLocation, "Default image location")
	flag.Parse()

	if *verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	var th starlark.Thread
	cnf, err := starlark.ExecFileOptions(&syntax.FileOptions{
		TopLevelControl: true,
		Set:             true,
		GlobalReassign:  true,
	}, &th, filepath.Join(*labdir, "conf.star"), nil, labomatic.NetBlocks)
	if err != nil {
		log.Fatal(err)
	}
	if err := labomatic.Build(cnf); err != nil {
		log.Println(err)
	}
}
