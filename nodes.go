package labomatic

import (
	"errors"
	"fmt"
	"hash/maphash"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"go.starlark.net/starlark"
)

var NetBlocks = starlark.StringDict{
	"Router": starlark.NewBuiltin("Router", NewRouter),
	"Host":   starlark.NewBuiltin("Host", NewHost),
	"Subnet": starlark.NewBuiltin("Subnet", NewSubnet),
	"Outnet": starlark.NewBuiltin("Outnet", NewIPVLAN),
}

func NewRouter(th *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name string
	)
	if err := starlark.UnpackArgs("Router", args, kwargs,
		"name?", &name); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor argument: %w", err)
	}

	if len(name) > 8 {
		return starlark.None, fmt.Errorf("node names must be <8 characters")
	}

	if name == "" {
		name = fmt.Sprintf("r%d", routerCount)
		routerCount++
	}

	return &netnode{
		name: name,
		typ:  nodeRouter,
	}, nil
}

func NewHost(th *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name string
	)
	if err := starlark.UnpackArgs("Host", args, kwargs,
		"name?", &name); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor argument: %w", err)
	}

	if len(name) > 8 {
		return starlark.None, fmt.Errorf("node names must be <8 characters")
	}

	if name == "" {
		name = fmt.Sprintf("h%d", hostCount)
		hostCount++
	}

	return &netnode{
		name: name,
		typ:  nodeHost,
	}, nil
}

const (
	nodeRouter = iota
	nodeHost
)

// TODO check name conflict with user inputs or other modules (starlark threads)
var (
	routerCount = 1
	hostCount   = 1
)

type netnode struct {
	name   string
	typ    int
	frozen bool

	init string

	ifcs []*netiface
}

var hseed = maphash.MakeSeed()

func (r *netnode) Freeze()              { r.frozen = true }
func (r netnode) Hash() (uint32, error) { return uint32(maphash.String(hseed, r.name)), nil }
func (r netnode) String() string {
	switch r.typ {
	default:
		panic("invalid host")
	case nodeRouter:
		return "<router> " + r.name
	case nodeHost:
		return "<host> " + r.name
	}
}
func (netnode) Truth() starlark.Bool { return true }
func (netnode) Type() string         { return "netnode" }

func (r *netnode) Attr(name string) (starlark.Value, error) {
	switch name {
	case "attach_nic":
		return attach_iface.BindReceiver(r), nil
	case "name":
		return starlark.String(r.name), nil
	}

	if idx := slices.IndexFunc(r.ifcs, func(iface *netiface) bool { return iface.name == name }); idx != -1 {
		return r.ifcs[idx], nil
	}

	return nil, starlark.NoSuchAttrError(name)
}

func (r netnode) AttrNames() (attrs []string) {
	for i := range len(r.ifcs) {
		attrs = append(attrs, r.ifcs[i].name)
	}

	return append(attrs,
		"name",
		"init_script",
		"attach_iface",
	)
}

var attach_iface = starlark.NewBuiltin("attach_nic", func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	nd, ok := fn.Receiver().(*netnode)
	if !ok {
		return starlark.None, fmt.Errorf("attach method called on wrong object")
	}

	var (
		net  *subnet
		addr Addr
	)

	if err := starlark.UnpackArgs("attach_nic", args, kwargs,
		"net", &net,
		"addr?", &addr,
	); err != nil {
		return starlark.None, err
	}

	if len(nd.ifcs) == 9 {
		return starlark.None, errors.New("only 9 interfaces can be added")
	}

	// numbering: interfaces start at 1 for localhost
	var ifname string
	switch nd.typ {
	case nodeRouter:
		ifname = fmt.Sprintf("ether%d", len(nd.ifcs)+2)
	case nodeHost:
		ifname = fmt.Sprintf("enp0s%d", len(nd.ifcs)+2)
	}

	ifc := &netiface{name: ifname, host: nd, net: net, addr: addr}
	nd.ifcs = append(nd.ifcs, ifc)
	net.mbs = append(net.mbs, ifc)
	return ifc, nil
})

func parseEther(s string) (int, bool) {
	if !strings.HasPrefix(s, "ether") {
		return 0, false
	}

	v, err := strconv.Atoi(s[len("ether"):])
	return v, err == nil
}

func (r *netnode) SetField(name string, val starlark.Value) error {
	if r.frozen {
		return errors.New("modified frozen data")
	}

	switch name {
	default:
		return starlark.NoSuchAttrError(name)
	case "name":
		r.name = val.String()
	case "init_script":
		ss, ok := val.(starlark.String)
		if !ok {
			return errors.New("invalid type for init script (want string)")
		}
		r.init = ss.GoString()
	}
	return nil
}

func (r *netnode) agent() GuestAgent {
	switch r.typ {
	case nodeRouter:
		return chr{}
	case nodeHost:
		return ubuntu{}
	default:
		panic("unknown node type")
	}
}

type netiface struct {
	name   string
	frozen bool
	host   *netnode
	net    *subnet
	addr   Addr
}

func (r *netiface) Freeze()              { r.frozen = true }
func (r netiface) Hash() (uint32, error) { return uint32(maphash.String(hseed, r.name)), nil }
func (r netiface) String() string        { return r.name }
func (netiface) Truth() starlark.Bool    { return true }
func (netiface) Type() string            { return "netiface" }

func (r netiface) AttrNames() []string {
	attrs := []string{"host", "name", "net"}
	if netip.Addr(r.addr).IsValid() {
		attrs = append(attrs, "addr")
	}
	return attrs
}
func (r netiface) Attr(name string) (starlark.Value, error) {
	switch name {
	default:
		return starlark.None, starlark.NoSuchAttrError(name)
	case "addr":
		return r.addr, nil
	case "host":
		return r.host, nil
	case "name":
		return starlark.String(r.name), nil
	case "net":
		return r.net, nil
	}
}

// TemplateNode is the data structure passed to node init templates.
// Fields are populated from the initial Starlark configuration.
type TemplateNode struct {
	// Name is the fully qualified name of the
	Name  string
	Image string

	// List of network interfaces
	Interfaces []struct {
		Name    string
		Address netip.Addr
		Network netip.Prefix
	}

	Host struct {
		PubKey string
	}
}

func (n *netnode) ToTemplate() TemplateNode {
	pub, err := gensshkeypair()
	if err != nil {
		panic("cannot generate key pair: " + err.Error())
	}

	t := TemplateNode{
		Name: n.name,
		Host: struct{ PubKey string }{string(pub)},
	}
	for _, iface := range n.ifcs {
		t.Interfaces = append(t.Interfaces, struct {
			Name    string
			Address netip.Addr
			Network netip.Prefix
		}{
			Name:    iface.name,
			Address: netip.Addr(iface.addr),
			Network: iface.net.network,
		})
	}
	return t
}
