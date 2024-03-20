package main

import (
	"flag"
	"log"
	"os"

	"github.com/TroutSoftware/labomatic"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func main() {
	labdir := flag.String("d", "lab", "name of the lab setup directory")
	flag.Parse()

	if *labdir != "" {
		if err := os.Chdir(*labdir); err != nil {
			log.Fatalf("cannot chdir to %s: %s", *labdir, err)
		}
	}

	var th starlark.Thread
	cnf, err := starlark.ExecFileOptions(&syntax.FileOptions{}, &th, "conf.star", nil, labomatic.NetBlocks)
	if err != nil {
		log.Fatal(err)
	}
	if err := labomatic.Build(cnf); err != nil {
		log.Println(err)
	}
}
