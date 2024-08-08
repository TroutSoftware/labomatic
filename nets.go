package labomatic

import (
	"errors"
	"fmt"
	"hash/maphash"
	"iter"
	"net/netip"
	"slices"

	"go.starlark.net/starlark"
)

var netCount = 1

func NewSubnet(th *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name     string
		network  string
		host     bool
		linkonly bool

		dns dnsConfig
	)
	if err := starlark.UnpackArgs("Subnet", args, kwargs,
		"network?", &network,
		"link_only?", &linkonly,
		"name?", &name,
		"host?", &host,
		"dns_server?", &dns.Server, "dns_domain?", &dns.Domain); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor argument: %w", err)
	}
	if network == "" && !linkonly {
		return starlark.None, fmt.Errorf("no network address provided")
	}

	if name == "" {
		name = fmt.Sprintf("br%d", netCount)
		netCount++
	}

	sub, err := netip.ParsePrefix(network)
	if err != nil && !linkonly {
		return starlark.None, fmt.Errorf("invalid network specification %s: %w", network, err)
	}

	switch {
	case dns.Domain != "" && dns.Server == "":
		return starlark.None, fmt.Errorf("DNS domain can only be set if dns_server is also provided")
	case dns.Server != "" && !host:
		return starlark.None, fmt.Errorf("DNS configuration only makes sense in host networks")
	}

	return &subnet{
		name:     name,
		network:  sub,
		host:     host,
		dns:      dns,
		linkonly: linkonly,
	}, nil
}

// config as seen from the host
type dnsConfig struct {
	Server string
	Domain string
}

var defaultUserNet, _ = netip.ParsePrefix("169.254.254.0/24")

func NewNATLAN(th *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name    string
		network string
	)

	if err := starlark.UnpackArgs("Outnet", args, kwargs,
		"name?", &name,
		"network?", &network,
	); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor: %w", err)
	}

	if name == "" {
		name = fmt.Sprintf("dx%d", netCount)
		netCount++
	}

	sub := defaultUserNet
	if network != "" {
		var err error
		sub, err = netip.ParsePrefix(network)
		if err != nil {
			return starlark.None, fmt.Errorf("invalid network specification %s: %w", network, err)
		}
	}

	return &subnet{
		name:    name,
		nat:     true,
		network: sub,
		host:    true,
	}, nil
}

type subnet struct {
	name   string
	frozen bool
	// host is made available to the bridge, at the last network address
	host bool

	// NAT outbound queries for this subnet
	nat bool

	// no addressing performed during set-up
	linkonly bool

	dns dnsConfig

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
		"dns_domain",
		"dns_server",
		"host",
		"link_only",
		"network",
	}
}

func (r *subnet) Attr(name string) (starlark.Value, error) {
	switch name {
	case "addr":
		return getaddr.BindReceiver(r), nil
	case "dns_domain":
		return starlark.String(r.dns.Domain), nil
	case "dns_server":
		return starlark.String(r.dns.Server), nil
	case "host":
		return starlark.Bool(r.host), nil
	case "link_only":
		return starlark.Bool(r.linkonly), nil
	case "network":
		return Prefix(r.network), nil
	}

	return nil, starlark.NoSuchAttrError(name)
}

var getaddr = starlark.NewBuiltin("addr", func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	nn := fn.Receiver().(*subnet)
	var num int
	if err := starlark.UnpackArgs("addr", args, kwargs, "num", &num); err != nil {
		return starlark.None, err
	}
	if nn.linkonly {
		return starlark.None, fmt.Errorf("network is link_only (does not allow addressing)")
	}

	if nn.host && num == 1<<(32-nn.network.Bits())-2 {
		return starlark.None, errors.New("last address in host networks is always the host")
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

// last returns the last assignable address in pf (so network broadcast - 1)
func last(pf netip.Prefix) netip.Addr {
	bits := pf.Addr().As4()
	ui := uint32(bits[0])<<24 | uint32(bits[1])<<16 | uint32(bits[2])<<8 | uint32(bits[3])
	ui |= ^uint32(0) >> uint32(pf.Bits())
	ui--
	return netip.AddrFrom4([...]byte{byte(ui >> 24), byte(ui >> 16), byte(ui >> 8), byte(ui)})
}

// netsof returns an iterator over all networks attached to at least one configured VM
func netsof(globals starlark.StringDict) iter.Seq[*subnet] {
	var linkednets []*subnet
	for n := range nodesof(globals) {
		for _, ifc := range n.ifcs {
			if !slices.Contains(linkednets, ifc.net) {
				linkednets = append(linkednets, ifc.net)
			}
		}
	}
	return func(yield func(*subnet) bool) {
		for _, n := range linkednets {
			if !yield(n) {
				return
			}
		}
	}
}
