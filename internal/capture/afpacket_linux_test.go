//go:build linux

package capture

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestAFPacketLive sniffs real loopback traffic. It needs CAP_NET_RAW and
// is skipped otherwise.
func TestAFPacketLive(t *testing.T) {
	const port = 23681
	rules := []FilterRule{{Src: netip.MustParseAddr("127.0.0.1"), DstPort: port}}
	ap, err := NewAFPacket("lo", rules)
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || os.Geteuid() != 0 {
			t.Skipf("AF_PACKET unavailable (needs root/CAP_NET_RAW): %v", err)
		}
		t.Fatal(err)
	}
	defer ap.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := make(chan Packet, 4)
	done := make(chan error, 1)
	go func() { done <- ap.Run(ctx, func(p Packet) { got <- p }) }()

	conn, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", "23681"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	want := []byte("afpacket-live-payload")

	deadline := time.After(5 * time.Second)
	for {
		if _, err := conn.Write(want); err != nil {
			t.Fatal(err)
		}
		select {
		case p := <-got:
			if string(p.Payload) != string(want) || p.DstPort != port {
				t.Fatalf("unexpected packet %+v", p)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run: %v", err)
			}
			return
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatal("no packet captured")
		}
	}
}
