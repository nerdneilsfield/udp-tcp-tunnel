package tunnel_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/tunnel"
)

var codecs = []config.Compression{
	{Algorithm: "zstd", Level: 1},
	{Algorithm: "zstd", Level: 2},
	{Algorithm: "zstd", Level: 3},
	{Algorithm: "zstd", Level: 4},
	{Algorithm: "s2", Level: 1},
	{Algorithm: "s2", Level: 2},
	{Algorithm: "s2", Level: 3},
}

func newComp(t *testing.T, c config.Compression) (*tunnel.Compressor, *stats.CompressionCounters) {
	t.Helper()
	cs := &stats.CompressionCounters{}
	comp, err := tunnel.NewCompressor(c, cs)
	must(t, err)
	if comp == nil {
		t.Fatalf("NewCompressor(%+v) returned nil", c)
	}
	return comp, cs
}

func TestCompressorRoundTrip(t *testing.T) {
	text := bytes.Repeat([]byte("lidar packet 0123456789 "), 50)
	for _, c := range codecs {
		t.Run(c.Algorithm+string(rune('0'+c.Level)), func(t *testing.T) {
			comp, cs := newComp(t, c)
			var scratch []byte
			for _, size := range []int{100, len(text), 1200} {
				src := bytes.Repeat(text, 2)[:size]
				body, compressed := comp.Compress(scratch, src)
				if !compressed || len(body) >= len(src) {
					t.Fatalf("size %d: compressed=%v len=%d", size, compressed, len(body))
				}
				got, err := comp.Receive(true, append([]byte(nil), body...))
				must(t, err)
				if !bytes.Equal(got, src) {
					t.Fatalf("size %d: round trip differs", size)
				}
			}
			s := cs.Snapshot()
			if s.TxIn <= s.TxOut || s.RxOut != s.TxIn || s.RxIn != s.TxOut || s.TxRaw != 0 {
				t.Fatalf("unexpected counters: %+v", s)
			}
		})
	}
}

func TestCompressorMaxSizePayload(t *testing.T) {
	src := bytes.Repeat([]byte{7}, tunnel.MaxPayload)
	for _, c := range codecs[:1] {
		comp, _ := newComp(t, c)
		body, compressed := comp.Compress(nil, src)
		if !compressed {
			t.Fatal("a zero-filled payload should compress")
		}
		got, err := comp.Receive(true, body)
		must(t, err)
		if !bytes.Equal(got, src) {
			t.Fatal("max-size round trip differs")
		}
	}
}

func TestCompressorIncompressibleAndTinySentRaw(t *testing.T) {
	for _, c := range []config.Compression{{Algorithm: "zstd"}, {Algorithm: "s2"}} {
		t.Run(c.Algorithm, func(t *testing.T) {
			comp, cs := newComp(t, c)
			noise := make([]byte, 1000)
			_, _ = rand.Read(noise)
			body, compressed := comp.Compress(nil, noise)
			if compressed || !bytes.Equal(body, noise) {
				t.Fatalf("random payload: compressed=%v", compressed)
			}
			body, compressed = comp.Compress(nil, []byte("tiny"))
			if compressed || string(body) != "tiny" {
				t.Fatalf("tiny payload: compressed=%v", compressed)
			}
			s := cs.Snapshot()
			if s.TxRaw != 2 || s.TxIn != 1004 || s.TxOut != 1004 {
				t.Fatalf("counters: %+v", s)
			}
			// Raw frames pass through Receive unchanged and are counted.
			got, err := comp.Receive(false, noise)
			must(t, err)
			if !bytes.Equal(got, noise) || cs.Snapshot().RxIn != 1000 {
				t.Fatal("raw receive")
			}
		})
	}
}

func TestCompressorFramesAreIndependent(t *testing.T) {
	for _, c := range []config.Compression{{Algorithm: "zstd"}, {Algorithm: "s2"}} {
		t.Run(c.Algorithm, func(t *testing.T) {
			comp, _ := newComp(t, c)
			var bodies, srcs [][]byte
			for i := range 5 {
				src := bytes.Repeat([]byte{byte('a' + i)}, 400+i)
				body, ok := comp.Compress(nil, src)
				if !ok {
					t.Fatal("not compressed")
				}
				bodies, srcs = append(bodies, append([]byte(nil), body...)), append(srcs, src)
			}
			// Lose some frames and reorder the rest: each still decodes.
			for _, i := range []int{4, 0, 2} {
				got, err := comp.Receive(true, bodies[i])
				must(t, err)
				if !bytes.Equal(got, srcs[i]) {
					t.Fatalf("frame %d differs", i)
				}
			}
		})
	}
}

func TestCompressorRejectsBombsAndGarbage(t *testing.T) {
	big := make([]byte, 4<<20) // 4 MiB of zeros compresses to a few bytes

	var zbuf bytes.Buffer
	w, err := zstd.NewWriter(&zbuf)
	must(t, err)
	_, err = w.Write(big)
	must(t, err)
	must(t, w.Close()) // streaming frame: no declared content size
	streamFrame := append([]byte(nil), zbuf.Bytes()...)

	// A much larger streaming bomb must be stopped at the limit, not decoded.
	var hbuf bytes.Buffer
	hw, err := zstd.NewWriter(&hbuf, zstd.WithWindowSize(64<<10)) // small window: only the size limit can stop it
	must(t, err)
	_, err = hw.Write(make([]byte, 64<<20))
	must(t, err)
	must(t, hw.Close())
	hugeFrame := append([]byte(nil), hbuf.Bytes()...)

	zenc, err := zstd.NewWriter(nil)
	must(t, err)
	sizedFrame := zenc.EncodeAll(big, nil) // declares 4 MiB

	cases := []struct {
		name      string
		c         config.Compression
		body      []byte
		oversized bool
	}{
		{"zstd declared size", config.Compression{Algorithm: "zstd"}, sizedFrame, true},
		{"zstd unknown size", config.Compression{Algorithm: "zstd"}, streamFrame, true},
		{"zstd 64 MiB stream, small window", config.Compression{Algorithm: "zstd"}, hugeFrame, true},
		{"s2 declared size", config.Compression{Algorithm: "s2"}, s2.Encode(nil, big), true},
		{"zstd garbage", config.Compression{Algorithm: "zstd"}, bytes.Repeat([]byte{0xff}, 64), false},
		{"s2 garbage", config.Compression{Algorithm: "s2"}, bytes.Repeat([]byte{0xff}, 64), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.body) > tunnel.MaxPayload {
				t.Fatalf("test frame too large for the wire: %d", len(tc.body))
			}
			comp, cs := newComp(t, tc.c)
			_, err := comp.Receive(true, tc.body)
			if err == nil {
				t.Fatal("bad payload accepted")
			}
			if got := errors.Is(err, tunnel.ErrDecompressedTooLarge); got != tc.oversized {
				t.Fatalf("oversized=%v, want %v (%v)", got, tc.oversized, err)
			}
			s := cs.Snapshot()
			if tc.oversized && (s.RxBombs != 1 || s.RxErrors != 0) || !tc.oversized && (s.RxErrors != 1 || s.RxBombs != 0) {
				t.Fatalf("counters: %+v", s)
			}
		})
	}
}

func TestNewCompressorNoneAndInvalid(t *testing.T) {
	for _, name := range []string{"", "none"} {
		comp, err := tunnel.NewCompressor(config.Compression{Algorithm: name}, nil)
		if comp != nil || err != nil {
			t.Fatalf("%q: %v %v", name, comp, err)
		}
	}
	if _, err := tunnel.NewCompressor(config.Compression{Algorithm: "lz77"}, nil); err == nil {
		t.Fatal("unknown algorithm accepted")
	}
}

// compressionOn lists the codecs exercised over every transport.
var compressionOn = []config.Compression{{Algorithm: "zstd", Level: 1}, {Algorithm: "s2", Level: 1}}

func TestTransportsCompressedRoundTrip(t *testing.T) {
	for _, tr := range transports(t) {
		for _, comp := range compressionOn {
			t.Run(tr.name+"/"+comp.Algorithm, func(t *testing.T) {
				srvStats, cliStats := &stats.CompressionCounters{}, &stats.CompressionCounters{}
				l, err := tr.listen("127.0.0.1:0", "tok", copt{comp, srvStats})
				must(t, err)
				defer l.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cli, err := tr.dialer(l.Addr(), "tok", copt{comp, cliStats}).Dial(ctx)
				must(t, err)
				defer cli.Close()
				srv := accept(t, l)
				defer srv.Close()

				noise := make([]byte, 900)
				_, _ = rand.Read(noise)
				payloads := [][]byte{
					bytes.Repeat([]byte("compressible "), 90),
					noise,
					{},
					[]byte("tiny"),
					bytes.Repeat([]byte{0x42}, 30000), // above the QUIC datagram limit
				}
				for i, p := range payloads {
					must(t, srv.Send(1, p))
					ch, got, err := recvWithin(t, cli)
					must(t, err)
					if ch != 1 || !bytes.Equal(got, p) {
						t.Fatalf("payload %d: channel %d, %d bytes, want %d bytes", i, ch, len(got), len(p))
					}
				}
				s, c := srvStats.Snapshot(), cliStats.Snapshot()
				if s.TxIn == 0 || s.TxOut >= s.TxIn || s.TxRaw != 3 {
					t.Fatalf("server tx counters: %+v", s)
				}
				if c.RxOut != s.TxIn || c.RxIn != s.TxOut || c.RxErrors != 0 {
					t.Fatalf("client rx %+v does not match server tx %+v", c, s)
				}
			})
		}
	}
}

func TestHandshakeCompressionMismatch(t *testing.T) {
	zstdCfg, s2Cfg, none := config.Compression{Algorithm: "zstd"}, config.Compression{Algorithm: "s2"}, config.Compression{}
	cases := []struct {
		name           string
		server, client config.Compression
		serverKnown    bool
		serverAlgo     tunnel.Algo
	}{
		{"client zstd, server none", none, zstdCfg, true, tunnel.AlgoNone},
		{"client s2, server zstd", zstdCfg, s2Cfg, true, tunnel.AlgoZstd},
		{"client none, server zstd", zstdCfg, none, false, tunnel.AlgoNone}, // version 1 hello, version 1 reply
	}
	for _, tr := range transports(t) {
		for _, tc := range cases {
			t.Run(tr.name+"/"+tc.name, func(t *testing.T) {
				l, err := tr.listen("127.0.0.1:0", "tok", copt{comp: tc.server})
				must(t, err)
				defer l.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err = tr.dialer(l.Addr(), "tok", copt{comp: tc.client}).Dial(ctx)
				var mm *tunnel.CompressionMismatchError
				if !errors.As(err, &mm) || !errors.Is(err, tunnel.ErrCompressionMismatch) {
					t.Fatalf("got %v, want a compression mismatch", err)
				}
				if mm.ServerKnown != tc.serverKnown || mm.Server != tc.serverAlgo {
					t.Fatalf("mismatch details: %+v", mm)
				}
				// A wrong token is still reported as an auth failure first.
				_, err = tr.dialer(l.Addr(), "bad", copt{comp: tc.client}).Dial(ctx)
				if !errors.Is(err, tunnel.ErrAuthFailed) {
					t.Fatalf("wrong token with mismatch: got %v, want ErrAuthFailed", err)
				}
			})
		}
	}
}

// oldServer speaks only protocol version 1, like a v0.2.0 server.
func oldServer(t *testing.T) (addr string, hellos chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { ln.Close() })
	hellos = make(chan []byte, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				head := make([]byte, 6)
				if _, err := c.Read(head); err != nil {
					return
				}
				hellos <- head
				if head[4] != 1 {
					_, _ = c.Write(append([]byte("UTT1"), 1, byte(tunnel.StatusBadVersion)))
					return
				}
				_ = tunnel.WriteReply(c, tunnel.StatusOK)
				buf := make([]byte, 256)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), hellos
}

func TestCompressionAgainstVersion1Server(t *testing.T) {
	addr, hellos := oldServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// compression none: the hello is the original version 1 hello.
	sess, err := tunnel.NewTCPDialer(tunnel.TCPDialerOptions{Addr: addr, Token: "t", Channels: []uint16{1}}).Dial(ctx)
	must(t, err)
	sess.Close()
	if h := <-hellos; h[4] != 1 {
		t.Fatalf("hello version %d, want 1", h[4])
	}

	// compression on: the old server rejects version 2; the error says why.
	_, err = tunnel.NewTCPDialer(tunnel.TCPDialerOptions{
		Addr: addr, Token: "t", Channels: []uint16{1}, Compression: config.Compression{Algorithm: "zstd"},
	}).Dial(ctx)
	if !errors.Is(err, tunnel.ErrCompressionUnsupported) {
		t.Fatalf("got %v, want ErrCompressionUnsupported", err)
	}
}
