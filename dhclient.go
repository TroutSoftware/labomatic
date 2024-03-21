package labomatic

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
	"github.com/vishvananda/netlink"
)

func lease4(ctx context.Context, iface netlink.Link) error {
	mods := []nclient4.ClientOpt{
		nclient4.WithTimeout(3 * time.Second),
		nclient4.WithRetry(20),
	}

	cl, err := nclient4.New(iface.Attrs().Name, mods...)
	if err != nil {
		return fmt.Errorf("cannot start DHCP client: %w", err)
	}
	defer cl.Close()

	// https://www.rfc-editor.org/rfc/rfc2132#section-9.14
	lease, err := cl.Request(ctx,
		dhcpv4.WithRequestedOptions(dhcpv4.OptionSubnetMask),
		dhcpv4.WithOption(dhcpv4.OptClientIdentifier([]byte{0xff, 0xac, 0x4d, 0x1d, 0x0e, 0xbe, 0x5b, 0x44, 0x34})),
	)
	if err != nil {
		return fmt.Errorf("cannot obtain lease: %w", err)
	}

	netmask := lease.ACK.SubnetMask()
	if netmask == nil {
		netmask = []byte{255, 255, 255, 255} // /32 usually safe
	}

	dst := &netlink.Addr{IPNet: &net.IPNet{IP: lease.ACK.YourIPAddr, Mask: netmask}}
	if err := netlink.AddrReplace(iface, dst); err != nil {
		return fmt.Errorf("cannot set new address: %w", err)
	}

	// TODO multiple default routes?
	if gw := lease.ACK.Router(); len(gw) > 0 {
		r := &netlink.Route{
			LinkIndex: iface.Attrs().Index,
			Gw:        gw[0],
		}

		if err := netlink.RouteReplace(r); err != nil {
			return fmt.Errorf("cannot set DHCP route: %w", err)
		}
	}

	var ds []string
	if lease.ACK.DomainSearch() != nil {
		ds = lease.ACK.DomainSearch().Labels
	}

	return WriteDNSSettings(
		lease.ACK.DNS(),
		ds,
		lease.ACK.DomainName(),
	)
}

func WriteDNSSettings(ns []net.IP, sl []string, domain string) error {
	rc := new(bytes.Buffer)
	if domain != "" {
		rc.WriteString(fmt.Sprintf("domain %s\n", domain))
	}
	for _, ip := range ns {
		rc.WriteString(fmt.Sprintf("nameserver %s\n", ip))
	}
	if sl != nil {
		rc.WriteString("search ")
		rc.WriteString(strings.Join(sl, " "))
		rc.WriteString("\n")
	}
	return os.WriteFile("/etc/resolv.conf", rc.Bytes(), 0o644)
}
