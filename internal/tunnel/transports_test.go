package tunnel_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/tunnel"
)

const testLiveness = 900 * time.Millisecond

// selfSigned returns a server TLS config and a client config trusting it.
func selfSigned(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(t, err)
	leaf, err := x509.ParseCertificate(der)
	must(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		&tls.Config{RootCAs: pool, ServerName: "localhost"}
}

// transport builds matching listeners and dialers for one transport.
type transport struct {
	name   string
	listen func(addr, token string, o copt) (tunnel.Listener, error)
	dialer func(addr, token string, o copt) tunnel.Dialer
}

// copt carries the optional compression settings of a test listener or dialer.
type copt struct {
	comp  config.Compression
	stats *stats.CompressionCounters
}

func transports(t *testing.T) []transport {
	t.Helper()
	srvTLS, cliTLS := selfSigned(t)
	ids := []uint16{1}
	kcpCfg := config.KCP{Key: "test-key", DataShards: 3, ParityShards: 1}
	return []transport{
		{
			name: "tcp",
			listen: func(addr, token string, o copt) (tunnel.Listener, error) {
				l, err := tunnel.ListenTCP(addr, tunnel.TCPServerOptions{Token: token, Channels: ids, Compression: o.comp, CompStats: o.stats})
				if err != nil {
					return nil, err
				}
				return l, nil
			},
			dialer: func(addr, token string, o copt) tunnel.Dialer {
				return tunnel.NewTCPDialer(tunnel.TCPDialerOptions{Addr: addr, Token: token, Channels: ids, Compression: o.comp, CompStats: o.stats})
			},
		},
		{
			name: "quic",
			listen: func(addr, token string, o copt) (tunnel.Listener, error) {
				l, err := tunnel.ListenQUIC(addr, tunnel.QUICServerOptions{Token: token, Channels: ids, TLS: srvTLS, IdleTimeout: testLiveness, Compression: o.comp, CompStats: o.stats})
				if err != nil {
					return nil, err
				}
				return l, nil
			},
			dialer: func(addr, token string, o copt) tunnel.Dialer {
				return tunnel.NewQUICDialer(tunnel.QUICDialerOptions{Addr: addr, Token: token, Channels: ids, TLS: cliTLS, IdleTimeout: testLiveness, Compression: o.comp, CompStats: o.stats})
			},
		},
		{
			name: "kcp",
			listen: func(addr, token string, o copt) (tunnel.Listener, error) {
				l, err := tunnel.ListenKCP(addr, tunnel.KCPServerOptions{Token: token, Channels: ids, KCP: kcpCfg, Liveness: testLiveness, Compression: o.comp, CompStats: o.stats})
				if err != nil {
					return nil, err
				}
				return l, nil
			},
			dialer: func(addr, token string, o copt) tunnel.Dialer {
				return tunnel.NewKCPDialer(tunnel.KCPDialerOptions{Addr: addr, Token: token, Channels: ids, KCP: kcpCfg, Liveness: testLiveness, Compression: o.comp, CompStats: o.stats})
			},
		},
	}
}

func accept(t *testing.T, l tunnel.Listener) tunnel.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := l.Accept(ctx)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return sess
}

func TestTransportsAuth(t *testing.T) {
	for _, tr := range transports(t) {
		t.Run(tr.name, func(t *testing.T) {
			l, err := tr.listen("127.0.0.1:0", "secret", copt{})
			must(t, err)
			defer l.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, derr := tr.dialer(l.Addr(), "wrong", copt{}).Dial(ctx)
			if !errors.Is(derr, tunnel.ErrAuthFailed) {
				t.Fatalf("wrong token: got %v, want ErrAuthFailed", derr)
			}

			cli, err := tr.dialer(l.Addr(), "secret", copt{}).Dial(ctx)
			must(t, err)
			defer cli.Close()
			srv := accept(t, l)
			defer srv.Close()

			// The rejected attempt must not have produced a session.
			short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer stop()
			if extra, err := l.Accept(short); err == nil {
				extra.Close()
				t.Fatal("rejected client produced a session")
			}
		})
	}
}

func TestTransportsRoundTrip(t *testing.T) {
	for _, tr := range transports(t) {
		t.Run(tr.name, func(t *testing.T) {
			l, err := tr.listen("127.0.0.1:0", "tok", copt{})
			must(t, err)
			defer l.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			cli, err := tr.dialer(l.Addr(), "tok", copt{}).Dial(ctx)
			must(t, err)
			defer cli.Close()
			srv := accept(t, l)
			defer srv.Close()

			// Small payload, an empty payload and a payload far above any
			// datagram limit (QUIC falls back to the stream).
			big := bytes.Repeat([]byte{0xab}, 30000)
			for _, p := range [][]byte{[]byte("hello"), {}, big} {
				must(t, srv.Send(1, p))
				ch, got, err := recvWithin(t, cli)
				must(t, err)
				if ch != 1 || !bytes.Equal(got, p) {
					t.Fatalf("got channel %d, %d bytes; want channel 1, %d bytes", ch, len(got), len(p))
				}
			}
		})
	}
}

func recvWithin(t *testing.T, s tunnel.Session) (uint16, []byte, error) {
	t.Helper()
	type res struct {
		ch  uint16
		p   []byte
		err error
	}
	c := make(chan res, 1)
	go func() {
		ch, p, err := s.Receive()
		c <- res{ch, p, err}
	}()
	select {
	case r := <-c:
		return r.ch, r.p, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("receive timed out")
		return 0, nil, nil
	}
}

func TestTransportsIdleSessionSurvives(t *testing.T) {
	for _, tr := range transports(t) {
		t.Run(tr.name, func(t *testing.T) {
			l, err := tr.listen("127.0.0.1:0", "", copt{})
			must(t, err)
			defer l.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cli, err := tr.dialer(l.Addr(), "", copt{}).Dial(ctx)
			must(t, err)
			defer cli.Close()
			srv := accept(t, l)
			defer srv.Close()

			// Both ends read continuously, as the real client and server do.
			go func() {
				for {
					if _, _, err := srv.Receive(); err != nil {
						return
					}
				}
			}()
			type res struct {
				p   []byte
				err error
			}
			got := make(chan res, 1)
			go func() {
				_, p, err := cli.Receive()
				got <- res{p, err}
			}()

			// Idle for longer than the dead-peer timeout: heartbeats must hold the session.
			time.Sleep(testLiveness + testLiveness/3)
			must(t, srv.Send(1, []byte("still alive")))
			select {
			case r := <-got:
				if r.err != nil || string(r.p) != "still alive" {
					t.Fatalf("idle session died: %v %q", r.err, r.p)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no data after idle period")
			}
		})
	}
}

func TestTransportsPeerGoneDetected(t *testing.T) {
	for _, tr := range transports(t) {
		t.Run(tr.name, func(t *testing.T) {
			l, err := tr.listen("127.0.0.1:0", "", copt{})
			must(t, err)
			defer l.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cli, err := tr.dialer(l.Addr(), "", copt{}).Dial(ctx)
			must(t, err)
			defer cli.Close()
			srv := accept(t, l)

			// KCP sends no close notification, so the client has to notice
			// by the missing heartbeats; QUIC notices immediately.
			start := time.Now()
			srv.Close()
			if _, _, err := recvWithin(t, cli); err == nil {
				t.Fatal("Receive succeeded after the peer closed")
			}
			if d := time.Since(start); d > 3*testLiveness {
				t.Fatalf("detected after %s, want within %s", d, 3*testLiveness)
			}
		})
	}
}

func TestClientOverTransports(t *testing.T) {
	for _, tr := range transports(t) {
		t.Run(tr.name, func(t *testing.T) {
			target := udpListener(t)
			l, err := tr.listen("127.0.0.1:0", "tok", copt{})
			must(t, err)
			addr := l.Addr()

			cfg := config.Client{
				ReconnectMin:  20 * time.Millisecond,
				ReconnectMax:  100 * time.Millisecond,
				StatsInterval: time.Hour,
				Transport:     tr.name,
				TLS:           config.ClientTLS{Enabled: tr.name == "quic"},
				Upstreams: []config.Upstream{{
					Name:     "u",
					Server:   addr,
					Channels: []config.ClientChannel{{ID: 1, Target: target.LocalAddr().String()}},
				}},
			}
			client, err := tunnel.NewClientWithDialer(cfg, tr.dialer(addr, "tok", copt{}), zap.NewNop())
			must(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- client.Run(ctx) }()

			sess := accept(t, l)
			must(t, sess.Send(1, []byte("before")))
			if got := readUDP(t, target); string(got) != "before" {
				t.Fatalf("got %q", got)
			}

			// Restart the server on the same UDP port.
			sess.Close()
			l.Close()
			for i := 0; ; i++ {
				l, err = tr.listen(addr, "tok", copt{})
				if err == nil {
					break
				}
				if i > 50 {
					t.Fatalf("rebind %s: %v", addr, err)
				}
				time.Sleep(50 * time.Millisecond)
			}
			defer l.Close()

			sess = accept(t, l)
			must(t, sess.Send(1, []byte("after")))
			if got := readUDP(t, target); string(got) != "after" {
				t.Fatalf("got %q", got)
			}

			cancel()
			select {
			case err := <-done:
				must(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("client did not stop on cancel")
			}
		})
	}
}
