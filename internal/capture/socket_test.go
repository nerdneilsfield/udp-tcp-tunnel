package capture

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestSocketCapture(t *testing.T) {
	// Find a free port, release it, and let the capture bind it.
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(probe.LocalAddr().(*net.UDPAddr).Port)
	probe.Close()

	sock, err := NewSocket("127.0.0.1", []uint16{port})
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Packet, 4)
	done := make(chan error, 1)
	go func() { done <- sock.Run(ctx, func(p Packet) { got <- p }) }()

	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	want := bytes.Repeat([]byte{0xab}, 1206)
	if _, err := conn.Write(want); err != nil {
		t.Fatal(err)
	}

	select {
	case p := <-got:
		if !bytes.Equal(p.Payload, want) || p.DstPort != port || p.Src != netip.MustParseAddr("127.0.0.1") {
			t.Fatalf("unexpected packet: src=%v dst=%d len=%d", p.Src, p.DstPort, len(p.Payload))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no packet")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestSocketBindError(t *testing.T) {
	if _, err := NewSocket("not-an-ip", []uint16{1}); err == nil {
		t.Fatal("invalid bind accepted")
	}
}
