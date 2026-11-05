package server_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/server"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/tunnel"
)

var loopback = net.IPv4(127, 0, 0, 1)

// freeUDPPort returns a UDP port that was free a moment ago.
func freeUDPPort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: loopback})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return uint16(conn.LocalAddr().(*net.UDPAddr).Port)
}

func listenTarget(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: loopback})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func sender(t *testing.T, port uint16) *net.UDPConn {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: loopback, Port: int(port)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func writeSelfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "udp-tcp-tunnel-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{loopback},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	must(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	must(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestEndToEnd runs a real server (socket capture) and client in-process and
// checks that two channels deliver identical payloads with boundaries intact.
func TestEndToEnd(t *testing.T) {
	cases := []struct {
		name      string
		transport string
		tls       bool
	}{
		{"tcp", config.TransportTCP, false},
		{"tcp+tls", config.TransportTCP, true},
		{"quic", config.TransportQUIC, true},
		{"kcp", config.TransportKCP, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runEndToEnd(t, c.transport, c.tls) })
	}
}

func runEndToEnd(t *testing.T, transport string, useTLS bool) {
	port1, port2 := freeUDPPort(t), freeUDPPort(t)
	target1, target2 := listenTarget(t), listenTarget(t)

	srvCfg := config.Server{
		Listen:    "127.0.0.1:0",
		Transport: transport,
		Capture:   config.CaptureSocket,
		UDPBind:   "127.0.0.1",
		Token:     "e2e-token",
		Channels: []config.ServerChannel{
			{ID: 1, Name: "lidar-1", SourceIP: "127.0.0.1", DestinationPort: port1},
			{ID: 2, Name: "lidar-2", DestinationPort: port2},
		},
	}
	srvCfg.SetDefaults()
	srvCfg.StatsInterval = time.Hour

	cliCfg := config.Client{
		Transport:     transport,
		ReconnectMin:  20 * time.Millisecond,
		ReconnectMax:  100 * time.Millisecond,
		StatsInterval: time.Hour,
		Token:         "e2e-token",
		Channels: []config.ClientChannel{
			{ID: 1, Name: "lidar-1", Target: target1.LocalAddr().String()},
			{ID: 2, Name: "lidar-2", Target: target2.LocalAddr().String()},
		},
	}
	cliCfg.SetDefaults()

	if useTLS {
		certFile, keyFile := writeSelfSignedCert(t)
		srvCfg.TLS = config.ServerTLS{Enabled: true, CertFile: certFile, KeyFile: keyFile}
		cliCfg.TLS = config.ClientTLS{Enabled: true, CAFile: certFile}
	}

	if transport == config.TransportKCP {
		srvCfg.KCP = config.KCP{Key: "e2e-key"}
		cliCfg.KCP = config.KCP{Key: "e2e-key"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := server.New(srvCfg, zap.NewNop())
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()
	select {
	case <-srv.Ready():
	case err := <-srvDone:
		t.Fatalf("server exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}

	cliCfg.Server = srv.ListenAddr()
	client, err := tunnel.NewClient(cliCfg, zap.NewNop())
	must(t, err)
	cliDone := make(chan error, 1)
	go func() { cliDone <- client.Run(ctx) }()
	eventually(t, "client connection", srv.Connected)

	send1, send2 := sender(t, port1), sender(t, port2)
	type message struct {
		payload []byte
	}
	var want1, want2 []message
	for i := range 30 {
		size := 1 + i*47 // 1 .. 1364 bytes, so boundaries differ per packet
		p1 := bytes.Repeat([]byte{byte(i)}, size)
		p2 := append([]byte(fmt.Sprintf("ch2-%02d-", i)), bytes.Repeat([]byte{0xee}, size)...)
		want1, want2 = append(want1, message{p1}), append(want2, message{p2})
		_, werr := send1.Write(p1)
		must(t, werr)
		_, werr = send2.Write(p2)
		must(t, werr)
	}
	// An empty datagram and a large one must survive too.
	big := bytes.Repeat([]byte{0x42}, 9000)
	want1 = append(want1, message{nil}, message{big})
	_, err = send1.Write(nil)
	must(t, err)
	_, err = send1.Write(big)
	must(t, err)

	// QUIC sends small payloads as datagrams and larger ones on the stream,
	// so only the set of payloads is guaranteed, not their order.
	ordered := transport != config.TransportQUIC
	check := func(name string, target *net.UDPConn, want []message) {
		buf := make([]byte, 70000)
		remaining := make(map[string]int)
		for _, w := range want {
			remaining[string(w.payload)]++
		}
		for i, w := range want {
			_ = target.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, _, err := target.ReadFromUDP(buf)
			if err != nil {
				t.Fatalf("%s datagram %d: %v", name, i, err)
			}
			if ordered && !bytes.Equal(buf[:n], w.payload) {
				t.Fatalf("%s datagram %d: got %d bytes, want %d (content differs)", name, i, n, len(w.payload))
			}
			if remaining[string(buf[:n])]--; remaining[string(buf[:n])] < 0 {
				t.Fatalf("%s datagram %d: unexpected payload of %d bytes", name, i, n)
			}
		}
	}
	check("channel 1", target1, want1)
	check("channel 2", target2, want2)

	// Server-side counters match what was sent.
	eventually(t, "server counters", func() bool {
		return srv.Stats().Channel(1).Packets() == uint64(len(want1)) &&
			srv.Stats().Channel(2).Packets() == uint64(len(want2))
	})
	if d := srv.Stats().Channel(1).Drops(stats.QueueFull); d != 0 {
		t.Fatalf("unexpected queue drops: %d", d)
	}

	cancel()
	for name, done := range map[string]chan error{"server": srvDone, "client": cliDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not stop", name)
		}
	}
}
