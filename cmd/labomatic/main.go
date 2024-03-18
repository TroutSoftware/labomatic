package main

import (
	"flag"
	"log"
	"path/filepath"

	"github.com/TroutSoftware/labomatic"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func main() {
	labdir := flag.String("d", "lab", "name of the lab setup directory")
	flag.Parse()

	var th starlark.Thread
	cnf, err := starlark.ExecFileOptions(&syntax.FileOptions{}, &th, filepath.Join(*labdir, "conf.star"), nil, labomatic.NetBlocks)
	if err != nil {
		log.Fatal(err)
	}
	if err := labomatic.Build(cnf); err != nil {
		log.Fatal(err)
	}
}
