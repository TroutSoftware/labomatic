package labomatic

import (
	"fmt"
	"io"
	"iter"
	"log/slog"
	"os/exec"
	"strings"
)

// Controllers are used to define what commands to run on the lab
type Controller func(iter.Seq[RunningNode])

func TermLab(nss iter.Seq[RunningNode]) {
	for node := range nss {
		if err := node.Close(); err != nil {
			slog.Warn("cannot terminate instance", "name", node, "error", err)
		}
	}
}

// Nodes are VMs or light namespaces in the current lab
type RunningNode struct {
	node *netnode
	cmd  *exec.Cmd
}

func (n RunningNode) Node() *netnode { return n.node }
func (n RunningNode) Close() error {
	n.cmd.Process.Kill()
	n.cmd.Wait()
	return nil
}

func FormatTable(into io.Writer, done chan struct{}) Controller {
	const (
		colName = iota
		colType
		colAddrs

		ncols
	)

	return func(s iter.Seq[RunningNode]) {
		sizes := []int{
			colName:  10,
			colType:  10,
			colAddrs: 16,
		}

		fmt.Fprintln(into, "\033[1mname       type       addresses\033[0m")

		for n := range s {
			name := n.Node().name
			typ := prettyType(n.Node().typ)
			fmt.Fprint(into, name+strings.Repeat(" ", sizes[colName]-len(name)-1), " ")
			fmt.Fprint(into, " ", typ+strings.Repeat(" ", sizes[colType]-len(typ)-1), " ")
			if len(n.Node().ifcs) > 0 {
				addr := n.Node().ifcs[0].addr.Addr().String()
				fmt.Fprintln(into, " "+addr)
				for i := 1; i < len(n.Node().ifcs); i++ {
					addr := n.Node().ifcs[i].addr.Addr().String()
					fmt.Fprintln(into, "                     ", addr)
				}
			} else {
				fmt.Fprintln(into, "")
			}
		}
		close(done)
	}
}

func prettyType(t int) string {
	switch t {
	default:
		panic("unknown type")
	case nodeRouter:
		return "router"
	case nodeSwitch:
		return "switch"
	case nodeAsset:
		return "asset"
	}
}
