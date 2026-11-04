package routing

import (
	"net/netip"
	"testing"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
)

func TestMatch(t *testing.T) {
	router := New([]config.ServerChannel{
		{ID: 1, SourceIP: "192.168.1.201", DestinationPort: 2368},
		{ID: 2, SourceIP: "192.168.1.202", DestinationPort: 2368},
		{ID: 3, DestinationPort: 2368}, // catch-all for the port
		{ID: 4, SourceIP: "192.168.1.201", SourcePort: 7000, DestinationPort: 2368},
		{ID: 5, SourcePort: 8000, DestinationPort: 2369},
		{ID: 6, SourceIP: "fe80::1", DestinationPort: 2370},
	})

	tests := []struct {
		name    string
		src     string
		srcPort uint16
		dstPort uint16
		want    uint16
		ok      bool
	}{
		{"source ip 1", "192.168.1.201", 1234, 2368, 1, true},
		{"source ip 2", "192.168.1.202", 1234, 2368, 2, true},
		{"catch-all", "10.0.0.5", 1234, 2368, 3, true},
		{"ip and port beats ip only", "192.168.1.201", 7000, 2368, 4, true},
		{"source port only", "10.0.0.5", 8000, 2369, 5, true},
		{"source port mismatch", "10.0.0.5", 8001, 2369, 0, false},
		{"unknown destination port", "192.168.1.201", 1234, 9999, 0, false},
		{"ipv6", "fe80::1", 1, 2370, 6, true},
		{"ipv6 other host", "fe80::2", 1, 2370, 0, false},
		{"ipv4-mapped ipv6 source", "::ffff:192.168.1.202", 1, 2368, 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := router.Match(netip.MustParseAddr(tc.src), tc.srcPort, tc.dstPort)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("Match = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
