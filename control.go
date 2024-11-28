package labomatic

import (
	"fmt"
	"io"
	"iter"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/vishvananda/netns"
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
type RunningNode interface {
	Node() *netnode
	Close() error
}

type VMNode struct {
	node *netnode
	cmd  *exec.Cmd
}

func (n VMNode) Node() *netnode { return n.node }
func (n VMNode) Close() error {
	n.cmd.Process.Kill()
	return n.cmd.Wait()
}

type AssetNode netnode

func (n *AssetNode) Node() *netnode { return (*netnode)(n) }
func (n *AssetNode) Close() error   { return netns.DeleteNamed(n.name) }

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
		hdr := "+" + strings.Repeat("-", sizes[colName]) +
			"+" + strings.Repeat("-", sizes[colType]) +
			"+" + strings.Repeat("-", sizes[colAddrs]) +
			"+"

		fmt.Fprintln(into, hdr)
		fmt.Fprintln(into, "+   name   |   type   |    addresses   +")
		fmt.Fprintln(into, hdr)

		for n := range s {
			name := n.Node().name
			typ := prettyType(n.Node().typ)
			fmt.Fprint(into, "+ ", name+strings.Repeat(" ", sizes[colName]-len(name)-1), "|")
			fmt.Fprint(into, " ", typ+strings.Repeat(" ", sizes[colType]-len(typ)-1), "|")
			if len(n.Node().ifcs) > 0 {
				addr := n.Node().ifcs[0].addr.Addr().String()
				fmt.Fprintln(into, " "+addr+strings.Repeat(" ", sizes[colAddrs]-len(addr)-1)+"|")
				for i := 1; i < len(n.Node().ifcs); i++ {
					addr := n.Node().ifcs[i].addr.Addr().String()
					fmt.Fprintln(into, "+          |          |",
						addr+strings.Repeat(" ", sizes[colAddrs]-len(addr)-1)+"|")
				}
			} else {
				fmt.Fprintln(into, "                |")
			}
		}
		fmt.Fprintln(into, hdr)
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
