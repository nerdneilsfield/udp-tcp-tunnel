package tunnel_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"runtime"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/tunnel"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func udpListener(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readUDP(t *testing.T, conn *net.UDPConn) []byte {
	t.Helper()
	buf := make([]byte, 70000)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("waiting for datagram: %v", err)
	}
	return buf[:n]
}

func acceptSession(t *testing.T, l *tunnel.TCPListener) tunnel.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := l.Accept(ctx)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return sess
}

func clientConfig(addr string, target *net.UDPConn) config.Client {
	return config.Client{
		Server:        addr,
		ReconnectMin:  20 * time.Millisecond,
		ReconnectMax:  100 * time.Millisecond,
		StatsInterval: time.Hour,
		Token:         "tok",
		Channels:      []config.ClientChannel{{ID: 1, Name: "one", Target: target.LocalAddr().String()}},
	}
}

func TestClientReconnects(t *testing.T) {
	target := udpListener(t)
	opts := tunnel.TCPServerOptions{Token: "tok", Channels: []uint16{1}}

	l1, err := tunnel.ListenTCP("127.0.0.1:0", opts)
	if err != nil {
		t.Fatal(err)
	}
	addr := l1.Addr()

	client, err := tunnel.NewClient(clientConfig(addr, target), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	sess := acceptSession(t, l1)
	must(t, sess.Send(1, []byte("before")))
	if got := readUDP(t, target); string(got) != "before" {
		t.Fatalf("got %q", got)
	}

	// Kill the server completely; the client must keep retrying.
	sess.Close()
	l1.Close()
	time.Sleep(150 * time.Millisecond)

	var l2 *tunnel.TCPListener
	for i := 0; ; i++ {
		l2, err = tunnel.ListenTCP(addr, opts)
		if err == nil {
			break
		}
		if i > 50 {
			t.Fatalf("rebind %s: %v", addr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer l2.Close()

	sess = acceptSession(t, l2)
	must(t, sess.Send(1, []byte("after")))
	if got := readUDP(t, target); string(got) != "after" {
		t.Fatalf("got %q", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop on cancel")
	}
}

func TestClientPreservesBoundariesAndDropsUnknownChannel(t *testing.T) {
	target := udpListener(t)
	l, err := tunnel.ListenTCP("127.0.0.1:0", tunnel.TCPServerOptions{Token: "tok", Channels: []uint16{1}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	client, err := tunnel.NewClient(clientConfig(l.Addr(), target), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	sess := acceptSession(t, l)
	defer sess.Close()

	// macOS limits a UDP datagram to net.inet.udp.maxdgram (9216 bytes by
	// default), so a larger payload cannot be re-emitted there.
	big := 60000
	if runtime.GOOS == "darwin" {
		big = 9000
	}
	payloads := [][]byte{[]byte("a"), {}, bytes.Repeat([]byte{7}, 1206), bytes.Repeat([]byte{9}, big)}
	if err := sess.Send(42, []byte("nobody listens")); err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		if err := sess.Send(1, p); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range payloads {
		if got := readUDP(t, target); !bytes.Equal(got, want) {
			t.Fatalf("datagram %d: got %d bytes, want %d", i, len(got), len(want))
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for client.Stats().UpstreamGlobal(config.DefaultUpstreamName).Drops(stats.UnknownChannel) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if client.Stats().UpstreamGlobal(config.DefaultUpstreamName).Drops(stats.UnknownChannel) != 1 {
		t.Fatalf("unknown channel drops = %d", client.Stats().UpstreamGlobal(config.DefaultUpstreamName).Drops(stats.UnknownChannel))
	}
}

func TestHandshakeRejections(t *testing.T) {
	l, err := tunnel.ListenTCP("127.0.0.1:0", tunnel.TCPServerOptions{Token: "tok", Channels: []uint16{1}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	tests := []struct {
		name     string
		token    string
		channels []uint16
		want     error
	}{
		{"wrong token", "bad", []uint16{1}, tunnel.ErrAuthFailed},
		{"unknown channel", "tok", []uint16{1, 5}, tunnel.ErrUnknownChannel},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := tunnel.NewTCPDialer(tunnel.TCPDialerOptions{Addr: l.Addr(), Token: tc.token, Channels: tc.channels})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := d.Dial(ctx); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestClientStatsCountDelivered(t *testing.T) {
	target := udpListener(t)
	l, err := tunnel.ListenTCP("127.0.0.1:0", tunnel.TCPServerOptions{Token: "tok", Channels: []uint16{1}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := tunnel.NewClient(clientConfig(l.Addr(), target), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	sess := acceptSession(t, l)
	defer sess.Close()
	for range 3 {
		if err := sess.Send(1, []byte("12345")); err != nil {
			t.Fatal(err)
		}
		readUDP(t, target)
	}
	counters := client.Stats().UpstreamChannel(config.DefaultUpstreamName, 1)
	deadline := time.Now().Add(2 * time.Second)
	for counters.Packets() != 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond) // the counter is bumped just after the write
	}
	if counters.Packets() != 3 || counters.Bytes() != 15 {
		t.Fatalf("packets=%d bytes=%d", counters.Packets(), counters.Bytes())
	}
}

func TestClientMultipleUpstreams(t *testing.T) {
	targetEast := udpListener(t)
	targetWest := udpListener(t)

	east, err := tunnel.ListenTCP("127.0.0.1:0", tunnel.TCPServerOptions{Token: "tok", Channels: []uint16{1}})
	if err != nil {
		t.Fatal(err)
	}
	defer east.Close()
	west, err := tunnel.ListenTCP("127.0.0.1:0", tunnel.TCPServerOptions{Token: "tok", Channels: []uint16{1}})
	if err != nil {
		t.Fatal(err)
	}
	defer west.Close()

	cfg := config.Client{
		ReconnectMin:  20 * time.Millisecond,
		ReconnectMax:  100 * time.Millisecond,
		StatsInterval: time.Hour,
		Token:         "tok",
		Upstreams: []config.Upstream{
			{
				Name:   "east",
				Server: east.Addr(),
				Channels: []config.ClientChannel{
					{ID: 1, Name: "from-east", Target: targetEast.LocalAddr().String()},
				},
			},
			{
				Name:   "west",
				Server: west.Addr(),
				Channels: []config.ClientChannel{
					{ID: 1, Name: "from-west", Target: targetWest.LocalAddr().String()},
				},
			},
		},
	}
	client, err := tunnel.NewClient(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	sessEast := acceptSession(t, east)
	sessWest := acceptSession(t, west)
	must(t, sessEast.Send(1, []byte("east-payload")))
	must(t, sessWest.Send(1, []byte("west-payload")))
	if got := readUDP(t, targetEast); string(got) != "east-payload" {
		t.Fatalf("east target: %q", got)
	}
	if got := readUDP(t, targetWest); string(got) != "west-payload" {
		t.Fatalf("west target: %q", got)
	}

	// Killing one upstream must not stop the other.
	sessEast.Close()
	east.Close()
	time.Sleep(100 * time.Millisecond)
	must(t, sessWest.Send(1, []byte("west-still-alive")))
	if got := readUDP(t, targetWest); string(got) != "west-still-alive" {
		t.Fatalf("west after east death: %q", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop on cancel")
	}
}
