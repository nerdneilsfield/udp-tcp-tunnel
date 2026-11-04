package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validServer() Server {
	s := Server{
		Channels: []ServerChannel{
			{ID: 1, Name: "a", SourceIP: "192.168.1.201", DestinationPort: 2368},
			{ID: 2, Name: "b", SourceIP: "192.168.1.202", DestinationPort: 2368},
		},
	}
	s.SetDefaults()
	return s
}

func validClient() Client {
	c := Client{
		Server: "192.168.1.10:9000",
		Channels: []ClientChannel{
			{ID: 1, Target: "127.0.0.1:2368"},
			{ID: 2, Target: "127.0.0.1:2369"},
		},
	}
	c.SetDefaults()
	return c
}

func TestServerValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Server)
		wantErr string // empty means valid
	}{
		{"valid", func(*Server) {}, ""},
		{"bad capture", func(s *Server) { s.Capture = "pcap" }, "capture"},
		{"afpacket without interface", func(s *Server) { s.Capture = CaptureAFPacket }, "interface"},
		{"afpacket with interface", func(s *Server) { s.Capture, s.Interface = CaptureAFPacket, "eth0" }, ""},
		{"bad listen", func(s *Server) { s.Listen = "nope" }, "listen"},
		{"bad listen port", func(s *Server) { s.Listen = ":99999" }, "listen"},
		{"bad udp bind", func(s *Server) { s.UDPBind = "host" }, "udp_bind"},
		{"zero queue", func(s *Server) { s.QueueSize = -1 }, "queue_size"},
		{"max payload too large", func(s *Server) { s.MaxPayload = 70000 }, "max_payload"},
		{"negative interval", func(s *Server) { s.StatsInterval = -time.Second }, "stats_interval"},
		{"long token", func(s *Server) { s.Token = strings.Repeat("x", 256) }, "token"},
		{"tls missing key", func(s *Server) { s.TLS = ServerTLS{Enabled: true, CertFile: "c"} }, "tls"},
		{"tls files without enabled", func(s *Server) { s.TLS = ServerTLS{CertFile: "c", KeyFile: "k"} }, "tls"},
		{"no channels", func(s *Server) { s.Channels = nil }, "at least one channel"},
		{"zero channel id", func(s *Server) { s.Channels[0].ID = 0 }, "id must be non-zero"},
		{"duplicate id", func(s *Server) { s.Channels[1].ID = 1 }, "already used"},
		{"missing destination port", func(s *Server) { s.Channels[0].DestinationPort = 0 }, "destination_port"},
		{"bad source ip", func(s *Server) { s.Channels[0].SourceIP = "lidar" }, "source_ip"},
		{"duplicate rule", func(s *Server) { s.Channels[1].SourceIP = "192.168.1.201" }, "same match rule"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validServer()
			tc.mutate(&s)
			assertErr(t, s.Validate(), tc.wantErr)
		})
	}
}

func TestClientValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Client)
		wantErr string
	}{
		{"valid", func(*Client) {}, ""},
		{"missing server", func(c *Client) { c.Server = "" }, "server"},
		{"server without port", func(c *Client) { c.Server = "example.com" }, "server"},
		{"backoff max below min", func(c *Client) { c.ReconnectMax = time.Millisecond }, "reconnect_backoff_max"},
		{"tls options without enabled", func(c *Client) { c.TLS.InsecureSkipVerify = true }, "tls"},
		{"tls enabled", func(c *Client) { c.TLS = ClientTLS{Enabled: true, ServerName: "x"} }, ""},
		{"no channels", func(c *Client) { c.Channels = nil }, "at least one channel"},
		{"duplicate id", func(c *Client) { c.Channels[1].ID = 1 }, "already used"},
		{"bad target", func(c *Client) { c.Channels[0].Target = "127.0.0.1" }, "target"},
		{"target without host", func(c *Client) { c.Channels[0].Target = ":2368" }, "no host"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validClient()
			tc.mutate(&c)
			assertErr(t, c.Validate(), tc.wantErr)
		})
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	s := validServer()
	s.Capture = "bad"
	s.QueueSize = -5
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "capture") || !strings.Contains(err.Error(), "queue_size") {
		t.Fatalf("expected both problems, got %v", err)
	}
}

func TestDefaults(t *testing.T) {
	var s Server
	s.SetDefaults()
	if s.Listen != DefaultListen || s.Capture != CaptureSocket || s.QueueSize != DefaultQueueSize ||
		s.MaxPayload != DefaultMaxPayload || s.StatsInterval != DefaultStatsInterval {
		t.Fatalf("unexpected server defaults: %+v", s)
	}
	var c Client
	c.SetDefaults()
	if c.ReconnectMin != DefaultBackoffInitial || c.ReconnectMax != DefaultBackoffCeiling || c.StatsInterval != DefaultStatsInterval {
		t.Fatalf("unexpected client defaults: %+v", c)
	}
}

func TestLoadExamples(t *testing.T) {
	srv, err := LoadServer(filepath.Join("..", "..", "examples", "server.yaml"))
	if err != nil {
		t.Fatalf("server example: %v", err)
	}
	if len(srv.Channels) != 2 || srv.Channels[0].SourceIP != "192.168.1.201" {
		t.Fatalf("unexpected server example: %+v", srv)
	}
	cli, err := LoadClient(filepath.Join("..", "..", "examples", "client.yaml"))
	if err != nil {
		t.Fatalf("client example: %v", err)
	}
	if len(cli.Channels) != 2 || cli.Channels[1].Target != "127.0.0.1:2369" {
		t.Fatalf("unexpected client example: %+v", cli)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		p := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if _, err := LoadServer(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
	_, err := LoadServer(write("listen: ':9000'\nunknown_field: 1\nchannels: []\n"))
	assertErr(t, err, "unknown_field")
	_, err = LoadServer(write("channels:\n  - {id: 1, destination_port: 2368}\nstats_interval: soon\n"))
	assertErr(t, err, "soon")
	_, err = LoadClient(write("server: 'h:1'\nchannels: []\n"))
	assertErr(t, err, "at least one channel")
}

func assertErr(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case want != "" && err == nil:
		t.Fatalf("expected error containing %q", want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Fatalf("error %q does not contain %q", err, want)
	}
}
