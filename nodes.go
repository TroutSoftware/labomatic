package labomatic

import (
	"errors"
	"fmt"
	"hash/maphash"
	"iter"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.starlark.net/starlark"
)

var NetBlocks = starlark.StringDict{
	"Router":       starlark.NewBuiltin("Router", NewRouter),
	"CyberSwitch":  starlark.NewBuiltin("CyberSwitch", NewSwitch),
	"Asset":        starlark.NewBuiltin("CyberSwitch", NewAsset),
	"Subnet":       starlark.NewBuiltin("Subnet", NewSubnet),
	"Outnet":       starlark.NewBuiltin("Outnet", NewNATLAN),
	"dhcp_options": dhcpOptions,
	"Addr":         starlark.NewBuiltin("Addr", NewAddr),
}

func NewRouter(th *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name string
	)
	if err := starlark.UnpackArgs("Router", args, kwargs,
		"name?", &name,
	); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor argument: %w", err)
	}

	switch {
	case len(name) > 8:
		return starlark.None, fmt.Errorf("node names must be <8 characters")
	case name == "":
		name = fmt.Sprintf("r%d", routerCount)
		routerCount++
	}

	return &netnode{
		name: name,
		typ:  nodeRouter,
	}, nil
}

func NewSwitch(th *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name  string
		image string
		media string
	)
	if err := starlark.UnpackArgs("CyberSwitch", args, kwargs,
		"name?", &name,
		"image?", &image,
		"media?", &media); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor argument: %w", err)
	}

	switch {
	case len(name) > 8:
		return starlark.None, fmt.Errorf("node names must be <8 characters")
	case name == "":
		name = fmt.Sprintf("r%d", routerCount)
		routerCount++
	}

	if !filepath.IsAbs(image) {
		wd := th.Local("workdir").(string)
		image = filepath.Join(wd, image)
	}

	return &netnode{
		name:  name,
		typ:   nodeSwitch,
		uefi:  true,
		image: image,
		media: media,
	}, nil
}

func NewAsset(th *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name string
	)
	if err := starlark.UnpackArgs("CyberSwitch", args, kwargs,
		"name?", &name); err != nil {
		return starlark.None, fmt.Errorf("invalid constructor argument: %w", err)
	}

	if len(name) > 8 {
		return starlark.None, fmt.Errorf("node names must be <8 characters")
	}

	if name == "" {
		name = fmt.Sprintf("a%d", assetCount)
		assetCount++
	}

	return &netnode{
		name: name,
		typ:  nodeAsset,
		uefi: true,
	}, nil
}

const (
	nodeRouter = iota
	nodeSwitch
	nodeAsset
)

// TODO check name conflict with user inputs or other modules (starlark threads)
var (
	routerCount = 1
	assetCount  = 1
)

type netnode struct {
	name   string
	typ    int
	frozen bool

	image string // image on disk
	uefi  bool
	media string // additional disk

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
	case nodeSwitch:
		return "<switch>" + r.name
	case nodeAsset:
		return "<asset>" + r.name
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
	if net.nat && !netip.Addr(addr).IsValid() {
		return starlark.None, errors.New("Outnet links must be statically addressed")
	}

	// TODO use MAC address instead
	var ifname string
	switch nd.typ {
	case nodeSwitch, nodeAsset:
		const pciOffset = 0
		ifname = fmt.Sprintf("eth%d", len(nd.ifcs))
	case nodeRouter:
		const pciOffset = 2 // but it might differ between laptops ???
		ifname = fmt.Sprintf("ether%d", len(nd.ifcs)+pciOffset)
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
	case nodeSwitch:
		return csw{}
	case nodeAsset:
		return csw{}
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

func OfType(t int) func(n *netnode) bool { return func(n *netnode) bool { return n.typ == t } }

// nodeof returns an iterator over the exported nodes in the configuration script.
// if the well-known boot_order variable is set, then nodes are those in the list, in order.
// if not, all nodes are provided, in random order
func nodesof(globals starlark.StringDict, filters ...func(*netnode) bool) iter.Seq[*netnode] {
	order, ok := globals["boot_order"]
	if ok {
		return func(yield func(*netnode) bool) {
			lo, ok := order.(*starlark.List)
			if !ok {

			}
			for n := range lo.Elements() {
				n, ok := n.(*netnode)
				if !ok || nomatch(filters, n) {
					continue
				}
				if !yield(n) {
					return
				}
			}
		}
	} else {
		return func(yield func(*netnode) bool) {
			for _, node := range globals {
				n, ok := node.(*netnode)
				if !ok || nomatch(filters, n) {
					continue
				}
				if !yield(n) {
					return
				}
			}
		}
	}
}

func nomatch[T any](fs []func(T) bool, v T) bool {
	for _, f := range fs {
		if f(v) {
			return false
		}
	}

	return true
}

// TemplateNode is the data structure passed to node init templates.
// Fields are populated from the initial Starlark configuration.
type TemplateNode struct {
	// Name is the fully qualified name of the
	Name  string
	Image string

	// List of network interfaces
	Interfaces []struct {
		Name     string
		Address  netip.Addr
		Network  netip.Prefix
		LinkOnly bool
		NATed    bool
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
			Name     string
			Address  netip.Addr
			Network  netip.Prefix
			LinkOnly bool
			NATed    bool
		}{
			Name:     iface.name,
			Address:  netip.Addr(iface.addr),
			Network:  iface.net.network,
			LinkOnly: iface.net.linkonly,
			NATed:    iface.net.nat,
		})
	}
	return t
}
