package labomatic

import (
	"fmt"
	"hash/maphash"
	"net/netip"

	"go.starlark.net/starlark"
)

// Addr exposes IP addresses to Starlark
type Addr netip.Addr

func (Addr) Freeze()                 {}
func (r Addr) Hash() (uint32, error) { return uint32(maphash.String(hseed, r.String())), nil }
func (r Addr) String() string        { return r.String() }
func (r Addr) Truth() starlark.Bool  { return starlark.Bool(netip.Addr(r).IsValid()) }
func (Addr) Type() string            { return "Addr" }

type Mac uint32 // last 3 bytes of self-assigned range

func (m Mac) String() string {
	return fmt.Sprintf("%x:%x:%x", byte(m>>16), byte(m>>8), byte(m))
}
