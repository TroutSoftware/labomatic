package labomatic

import (
	"fmt"
	"hash/maphash"
	"net/netip"

	"go.starlark.net/starlark"
)

// Addr exposes IP addresses to Starlark
type Addr netip.Addr

func NewAddr(th *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		addr string
	)
	if err := starlark.UnpackArgs("Router", args, kwargs,
		"addr?", &addr); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor argument: %w", err)
	}
	rs, err := netip.ParseAddr(addr)
	if err != nil {
		return starlark.None, fmt.Errorf("invalid address %s: %w", addr, err)
	}
	return Addr(rs), nil
}

func (Addr) Freeze()                 {}
func (r Addr) Hash() (uint32, error) { return uint32(maphash.String(hseed, r.String())), nil }
func (r Addr) String() string        { return netip.Addr(r).String() }
func (r Addr) Truth() starlark.Bool  { return starlark.Bool(netip.Addr(r).IsValid()) }
func (Addr) Type() string            { return "Addr" }

func (a Addr) IsValid() bool { return netip.Addr(a).IsValid() }

type Mac uint32 // last 3 bytes of self-assigned range

func (m Mac) String() string {
	return fmt.Sprintf("%x:%x:%x", byte(m>>16), byte(m>>8), byte(m))
}

type Prefix netip.Prefix

func (Prefix) Freeze()                 {}
func (r Prefix) Hash() (uint32, error) { return uint32(maphash.String(hseed, r.String())), nil }
func (r Prefix) String() string        { return netip.Prefix(r).String() }
func (r Prefix) Truth() starlark.Bool  { return starlark.Bool(netip.Prefix(r).IsValid()) }
func (Prefix) Type() string            { return "Prefix" }
