package labomatic

import (
	"fmt"
	"hash/maphash"
	"net/netip"

	"go.starlark.net/starlark"
)

var netCount = 1

func NewSubnet(th *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name    string
		tag     int
		network string
		host    bool
	)
	if err := starlark.UnpackArgs("Host", args, kwargs,
		"name?", &name,
		"tag?", &tag,
		"network?", &network,
		"host?", &host); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor argument: %w", err)
	}

	if name == "" {
		name = fmt.Sprintf("br%d", netCount)
		netCount++
	}

	sub, err := netip.ParsePrefix(network)
	if err != nil {
		return starlark.None, fmt.Errorf("invalid network specification %s: %w", network, err)
	}

	return &subnet{
		name:    name,
		tag:     tag,
		network: sub,
		host:    host,
	}, nil
}

type subnet struct {
	name    string
	frozen  bool
	tag     int
	host    bool
	network netip.Prefix
	mbs     []*netiface
}

func (r *subnet) Freeze()               { r.frozen = true }
func (r *subnet) Hash() (uint32, error) { return uint32(maphash.String(hseed, r.name)), nil }
func (r subnet) String() string         { return "<subnet> " + r.name }
func (subnet) Truth() starlark.Bool     { return true }
func (subnet) Type() string             { return "subnet" }

func (r subnet) Index(i int) starlark.Value { return r.mbs[i] }
func (r subnet) Len() int                   { return len(r.mbs) }

func (subnet) AttrNames() []string {
	return []string{
		"addr",
		"host",
	}
}

func (r *subnet) Attr(name string) (starlark.Value, error) {
	switch name {
	case "addr":
		return getaddr.BindReceiver(r), nil
	case "host":
		return starlark.Bool(r.host), nil
	}

	return nil, nil
}

var getaddr = starlark.NewBuiltin("addr", func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	nn := fn.Receiver().(*subnet)
	var num int
	if err := starlark.UnpackArgs("addr", args, kwargs, "num", &num); err != nil {
		return starlark.None, err
	}
	addr := nn.network.Addr()
	for range num {
		addr = addr.Next()
	}
	if !nn.network.Contains(addr) {
		return starlark.None, fmt.Errorf("address %s not in subnet %s", addr, nn.network)
	}

	return Addr(addr), nil
})
