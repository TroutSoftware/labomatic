package labomatic

import (
	"net/netip"
	"testing"
)

func TestLast(t *testing.T) {
	cases := []struct {
		net string
		add string
	}{
		{"192.168.0.0/24", "192.168.0.254"},
		{"10.10.0.0/16", "10.10.255.254"},
	}

	for _, c := range cases {
		pf, err := netip.ParsePrefix(c.net)
		if err != nil {
			t.Fatalf("invalid network %s: %s", c.net, err)
		}
		want, err := netip.ParseAddr(c.add)
		if err != nil {
			t.Fatalf("invalid address %s: %s", c.add, err)
		}
		if last(pf) != want {
			t.Errorf("last(%s): want %s, got %s", pf, want, last(pf))
		}
	}
}
