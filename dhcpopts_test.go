package labomatic

import (
	"fmt"
	"testing"
)

func TestEncodeRoutes(t *testing.T) {
	cases := []struct {
		routes []string
		enc    string
	}{
		{[]string{"default", "192.0.2.1"}, "00C0000201"},
		{[]string{"10.0.0.0/8", "10.0.0.1"}, "080A0A000001"},
		{[]string{"192.0.2.0/24", "192.0.2.1"}, "18C00002C0000201"},
		{[]string{"default", "192.0.2.1", "192.0.2.0/24", "192.0.2.110"}, "00C000020118C00002C000026E"},
	}

	for _, c := range cases {
		got, err := encodeRoutes(c.routes)
		if err != nil {
			t.Errorf("invalid routes %v: %s", c.routes, err)
		}
		if fmt.Sprintf("%X", got) != c.enc {
			t.Errorf("invalid encoding: want %s, got %X", c.enc, got)
		}
	}
}
