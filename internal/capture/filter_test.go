package capture

import (
	"net/netip"
	"testing"

	"golang.org/x/net/bpf"
)

func TestFilter(t *testing.T) {
	lidar1 := netip.MustParseAddr("192.168.1.201")
	lidar2 := netip.MustParseAddr("192.168.1.202")
	v6 := netip.MustParseAddr("fe80::1")
	other := netip.MustParseAddr("10.9.9.9")

	prog, err := buildFilter([]FilterRule{
		{Src: lidar1, DstPort: 2368},
		{Src: lidar2, SrcPort: 7000, DstPort: 2368},
		{Src: v6, DstPort: 2370},
		{DstPort: 2400},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := bpf.NewVM(prog)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		spec   frameSpec
		accept bool
	}{
		{"matching src host", frameSpec{src: lidar1, dstPort: 2368}, true},
		{"matching src host vlan", frameSpec{src: lidar1, dstPort: 2368, vlanTags: 1}, true},
		{"wrong src host", frameSpec{src: other, dstPort: 2368}, false},
		{"wrong dst port", frameSpec{src: lidar1, dstPort: 2369}, false},
		{"src host and port", frameSpec{src: lidar2, srcPort: 7000, dstPort: 2368}, true},
		{"src host wrong port", frameSpec{src: lidar2, srcPort: 7001, dstPort: 2368}, false},
		{"any host port rule", frameSpec{src: other, dstPort: 2400}, true},
		{"ipv6 match", frameSpec{src: v6, v6: true, dstPort: 2370}, true},
		{"ipv6 vlan match", frameSpec{src: v6, v6: true, vlanTags: 1, dstPort: 2370}, true},
		{"ipv6 other host", frameSpec{src: netip.MustParseAddr("fe80::2"), v6: true, dstPort: 2370}, false},
		{"ipv6 any host rule", frameSpec{src: v6, v6: true, dstPort: 2400}, true},
		{"ipv4 rule not applied to ipv6", frameSpec{src: v6, v6: true, dstPort: 2368}, false},
		{"tcp", frameSpec{src: lidar1, dstPort: 2368, proto: 6}, false},
		{"first fragment passes", frameSpec{src: lidar1, dstPort: 2368, fragFlags: 0x2000}, true},
		{"later fragment rejected", frameSpec{src: lidar1, dstPort: 2368, fragFlags: 0x0010}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, err := vm.Run(craftFrame(tc.spec))
			if err != nil {
				t.Fatal(err)
			}
			if got := n > 0; got != tc.accept {
				t.Fatalf("accepted=%v, want %v", got, tc.accept)
			}
		})
	}
}

func TestFilterIgnoresNonIP(t *testing.T) {
	prog, err := buildFilter([]FilterRule{{DstPort: 2368}}, false)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := bpf.NewVM(prog)
	if err != nil {
		t.Fatal(err)
	}
	arp := make([]byte, 60)
	arp[12], arp[13] = 0x08, 0x06
	if n, err := vm.Run(arp); err != nil || n != 0 {
		t.Fatalf("arp accepted: n=%d err=%v", n, err)
	}
}

func TestCompileFilterAssembles(t *testing.T) {
	raw, err := CompileFilter([]FilterRule{{Src: netip.MustParseAddr("192.168.1.201"), DstPort: 2368}})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || len(raw) > 4096 {
		t.Fatalf("unexpected program length %d", len(raw))
	}
}

func TestCompileFilterErrors(t *testing.T) {
	if _, err := CompileFilter(nil); err == nil {
		t.Fatal("empty rule set accepted")
	}
	many := make([]FilterRule, 60)
	for i := range many {
		many[i] = FilterRule{Src: netip.MustParseAddr("10.0.0.1"), SrcPort: 1, DstPort: uint16(i + 1)}
	}
	if _, err := CompileFilter(many); err == nil {
		t.Fatal("oversized rule set accepted")
	}
}
