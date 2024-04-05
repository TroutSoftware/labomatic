package labomatic

import (
	"fmt"
	"net/netip"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// encodeRoutes returns an RFC3442-compliant encoding of routes,
// which is an alternation of netmasked network addresses and gateways.
// as a special case, "default" is accepted as a network address
func encodeRoutes(routes []string) ([]byte, error) {
	if len(routes)%2 != 0 {
		panic("invalid call: routes should alternate networks and gateways")
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("empty route list")
	}

	var enc []byte

	for i := 0; i < len(routes); i += 2 {
		net := routes[i]
		if net == "default" {
			net = "0.0.0.0/0"
		}

		npf, err := netip.ParsePrefix(net)
		if err != nil {
			return nil, fmt.Errorf("invalid gateway: %w", err)
		}

		gw, err := netip.ParseAddr(routes[i+1])
		if err != nil {
			return nil, fmt.Errorf("invalid address: %w", err)
		}

		enc = append(enc, byte(npf.Bits()))
		enc = append(enc, npf.Addr().AsSlice()[:(npf.Bits()+7)/8]...)
		enc = append(enc, gw.AsSlice()...)
	}
	return enc, nil
}

var dhcpOptions = &starlarkstruct.Module{
	Name: "dhcp_options",
	Members: starlark.StringDict{
		"classless_routes": calcopt121,
	},
}

var calcopt121 = starlark.NewBuiltin("classless_routes", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	routes := starlark.NewDict(1)
	err := starlark.UnpackArgs("classless_routes", args, kwargs,
		"routes", &routes)
	if err != nil {
		return starlark.None, err
	}

	var goroutes []string
	for net, gw := range routes.Entries {
		snet, ok := net.(starlark.String)
		if !ok {
			return starlark.None, fmt.Errorf("invalid subnet %s: it must be network/mask or default", net)
		}

		sgw, ok := gw.(starlark.String)
		if !ok {
			return starlark.None, fmt.Errorf("invalid gateway %s: it must be a unicast address", gw)
		}
		goroutes = append(goroutes, string(snet), string(sgw))
	}
	enc, err := encodeRoutes(goroutes)
	if err != nil {
		return starlark.None, err
	}
	return starlark.String(fmt.Sprintf("%X", enc)), nil
})
