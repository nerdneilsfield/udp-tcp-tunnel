package server_test

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/capture"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/server"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/tunnel"
)

// fakeCapture lets tests inject packets.
type fakeCapture struct {
	packets chan capture.Packet
	stats   capture.Stats
}

func newFakeCapture() *fakeCapture { return &fakeCapture{packets: make(chan capture.Packet, 1024)} }

func (f *fakeCapture) Run(ctx context.Context, handle func(capture.Packet)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case p := <-f.packets:
			handle(p)
		}
	}
}
func (f *fakeCapture) Stats() capture.Stats { return f.stats }
func (f *fakeCapture) Close() error         { return nil }

// blockingSession accepts frames but never lets Send return until released,
// emulating a client behind a stalled TCP connection.
type blockingSession struct {
	release chan struct{}
	sent    chan struct{}
	once    sync.Once
}

func (b *blockingSession) Send(uint16, []byte) error {
	select {
	case b.sent <- struct{}{}:
	default:
	}
	<-b.release
	return nil
}

func (b *blockingSession) Receive() (uint16, []byte, error) {
	<-b.release
	return 0, nil, net.ErrClosed
}
func (b *blockingSession) RemoteAddr() string { return "test" }
func (b *blockingSession) Close() error {
	b.once.Do(func() { close(b.release) })
	return nil
}

// oneShotListener hands out a single session.
type oneShotListener struct {
	sess chan tunnel.Session
	done chan struct{}
}

func (l *oneShotListener) Accept(ctx context.Context) (tunnel.Session, error) {
	select {
	case s := <-l.sess:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *oneShotListener) Addr() string { return "127.0.0.1:0" }
func (l *oneShotListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func testServerConfig() config.Server {
	cfg := config.Server{
		Capture:  config.CaptureSocket,
		Channels: []config.ServerChannel{{ID: 1, Name: "one", SourceIP: "192.168.1.201", DestinationPort: 2368}},
	}
	cfg.SetDefaults()
	cfg.StatsInterval = time.Hour
	return cfg
}

func packet(src string, dstPort uint16, payload string) capture.Packet {
	return capture.Packet{Src: netip.MustParseAddr(src), SrcPort: 5000, DstPort: dstPort, Payload: []byte(payload)}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQueueFullDropsNewestPerChannel(t *testing.T) {
	cfg := testServerConfig()
	cfg.QueueSize = 4

	sess := &blockingSession{release: make(chan struct{}), sent: make(chan struct{}, 1)}
	listener := &oneShotListener{sess: make(chan tunnel.Session, 1), done: make(chan struct{})}
	listener.sess <- sess
	fake := newFakeCapture()

	srv := server.New(cfg, zap.NewNop(), server.WithCapture(fake), server.WithListener(listener))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Wait for the client to attach (packets are dropped as no_client before).
	counters := srv.Stats().Channel(1)
	eventually(t, "client attach", func() bool {
		fake.packets <- packet("192.168.1.201", 2368, "probe")
		time.Sleep(10 * time.Millisecond)
		return len(sess.sent) > 0
	})

	// The writer is now stuck in Send, so the queue (4) fills up.
	const burst = 20
	before := counters.Drops(stats.QueueFull)
	for range burst {
		fake.packets <- packet("192.168.1.201", 2368, "x")
	}
	eventually(t, "queue-full drops", func() bool {
		return counters.Drops(stats.QueueFull)-before >= burst-4-2
	})
	if drops := counters.Drops(stats.QueueFull) - before; drops > burst {
		t.Fatalf("dropped %d of %d", drops, burst)
	}
	if srv.Stats().Global().Drops(stats.QueueFull) != 0 {
		t.Fatal("queue-full drop counted globally instead of per channel")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestDropReasons(t *testing.T) {
	cfg := testServerConfig()
	cfg.MaxPayload = 8
	cfg.StatsInterval = 20 * time.Millisecond

	fake := newFakeCapture()
	fake.stats = capture.Stats{KernelDrops: 11, Fragments: 3}
	listener := &oneShotListener{sess: make(chan tunnel.Session), done: make(chan struct{})}
	srv := server.New(cfg, zap.NewNop(), server.WithCapture(fake), server.WithListener(listener))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	counters := srv.Stats().Channel(1)
	fake.packets <- packet("10.9.9.9", 2368, "wrong source")    // unknown channel
	fake.packets <- packet("192.168.1.201", 9999, "wrong port") // unknown channel
	fake.packets <- packet("192.168.1.201", 2368, "this payload is too large")
	fake.packets <- packet("192.168.1.201", 2368, "ok")

	eventually(t, "drops", func() bool {
		return srv.Stats().Global().Drops(stats.UnknownChannel) == 2 &&
			counters.Drops(stats.Oversized) == 1 &&
			counters.Drops(stats.NoClient) == 1 &&
			srv.Stats().Global().Drops(stats.CaptureDrop) == 11 &&
			srv.Stats().Global().Drops(stats.Fragment) == 3
	})

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
