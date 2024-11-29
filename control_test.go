package labomatic

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTableRender(t *testing.T) {
	nodes := []RunningNode{
		VMNode{node: &netnode{name: "r1", typ: nodeRouter, ifcs: []*netiface{
			{addr: Addr(netip.MustParseAddr("192.0.2.1"))},
		}}},
		VMNode{node: &netnode{name: "r2", typ: nodeRouter, ifcs: []*netiface{
			{addr: Addr(netip.MustParseAddr("192.0.2.2"))},
			{addr: Addr(netip.MustParseAddr("192.0.2.3"))},
		}}},
		VMNode{node: &netnode{name: "sw1", typ: nodeSwitch, ifcs: []*netiface{
			{addr: Addr(netip.MustParseAddr("192.0.2.10"))},
			{addr: Addr(netip.MustParseAddr("192.0.2.11"))},
			{addr: Addr(netip.MustParseAddr("192.0.2.12"))},
		}}},
		(*AssetNode)(&netnode{name: "plc1", typ: nodeAsset}),
	}

	want := "\x1b[1mname       type       addresses\x1b[0m" + `
r1         router     192.0.2.1
r2         router     192.0.2.2
                      192.0.2.3
sw1        switch     192.0.2.10
                      192.0.2.11
                      192.0.2.12
plc1       asset     
`

	var buf strings.Builder
	done := make(chan struct{})
	FormatTable(&buf, done)(slices.Values(nodes))

	t.Log("got table:\n" + buf.String())

	if !cmp.Equal(want, buf.String()) {
		t.Error(cmp.Diff(want, buf.String()))
	}
}
