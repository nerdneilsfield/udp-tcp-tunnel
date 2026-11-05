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
	if err := c.Normalize(); err != nil {
		panic(err)
	}
	return c
}

func validMultiClient() Client {
	c := Client{
		Token: "shared",
		TLS:   ClientTLS{Enabled: true, ServerName: "tunnel.example.com"},
		Upstreams: []Upstream{
			{
				Name:   "east",
				Server: "192.168.1.10:9000",
				Channels: []ClientChannel{
					{ID: 1, Target: "127.0.0.1:2368"},
				},
			},
			{
				Name:   "west",
				Server: "192.168.1.11:9000",
				Token:  "west-only",
				Channels: []ClientChannel{
					{ID: 1, Target: "127.0.0.1:2369"},
				},
			},
		},
	}
	c.SetDefaults()
	if err := c.Normalize(); err != nil {
		panic(err)
	}
	return c
}

func mustNormalize(t *testing.T, c *Client) {
	t.Helper()
	if err := c.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
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
		{"missing server", func(c *Client) { c.Upstreams[0].Server = "" }, "server"},
		{"server without port", func(c *Client) { c.Upstreams[0].Server = "example.com" }, "server"},
		{"backoff max below min", func(c *Client) {
			c.ReconnectMax = time.Millisecond
			c.Upstreams[0].ReconnectMax = time.Millisecond
		}, "reconnect_backoff_max"},
		{"tls options without enabled", func(c *Client) {
			c.TLS.InsecureSkipVerify = true
			c.Upstreams[0].TLS.InsecureSkipVerify = true
		}, "tls"},
		{"tls enabled", func(c *Client) {
			c.TLS = ClientTLS{Enabled: true, ServerName: "x"}
			*c.Upstreams[0].TLS = c.TLS
		}, ""},
		{"no channels", func(c *Client) { c.Upstreams[0].Channels = nil }, "at least one channel"},
		{"duplicate id", func(c *Client) { c.Upstreams[0].Channels[1].ID = 1 }, "already used"},
		{"bad target", func(c *Client) { c.Upstreams[0].Channels[0].Target = "127.0.0.1" }, "target"},
		{"target without host", func(c *Client) { c.Upstreams[0].Channels[0].Target = ":2368" }, "no host"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validClient()
			tc.mutate(&c)
			assertErr(t, c.Validate(), tc.wantErr)
		})
	}
}

func TestClientMultiValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Client)
		wantErr string
	}{
		{"valid multi", func(*Client) {}, ""},
		{"empty upstreams after normalize blocked earlier", func(c *Client) { c.Upstreams = nil }, "at least one upstream"},
		{"missing name", func(c *Client) { c.Upstreams[0].Name = "" }, "name is required"},
		{"duplicate name", func(c *Client) { c.Upstreams[1].Name = "east" }, "already used"},
		{"per upstream channel clash", func(c *Client) {
			c.Upstreams[0].Channels = append(c.Upstreams[0].Channels, ClientChannel{ID: 1, Target: "127.0.0.1:2400"})
		}, "already used"},
		{"same channel id on different upstreams ok", func(*Client) {}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validMultiClient()
			tc.mutate(&c)
			assertErr(t, c.Validate(), tc.wantErr)
		})
	}
}

func TestClientNormalizeSingle(t *testing.T) {
	c := Client{
		Server: "192.168.1.10:9000",
		Token:  "tok",
		TLS:    ClientTLS{Enabled: true, ServerName: "ex"},
		Channels: []ClientChannel{
			{ID: 1, Target: "127.0.0.1:2368"},
		},
	}
	c.SetDefaults()
	mustNormalize(t, &c)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.Upstreams) != 1 || c.Upstreams[0].Name != DefaultUpstreamName {
		t.Fatalf("upstreams: %+v", c.Upstreams)
	}
	u := c.Upstreams[0]
	if u.Server != "192.168.1.10:9000" || u.Token != "tok" || u.TLS == nil || !u.TLS.Enabled {
		t.Fatalf("inherited fields: %+v", u)
	}
}

func TestClientNormalizeInherit(t *testing.T) {
	c := validMultiClient()
	east, west := c.Upstreams[0], c.Upstreams[1]
	if east.Token != "shared" {
		t.Fatalf("east token = %q, want inherited shared", east.Token)
	}
	if west.Token != "west-only" {
		t.Fatalf("west token = %q, want override", west.Token)
	}
	if east.TLS == nil || !east.TLS.Enabled || east.TLS.ServerName != "tunnel.example.com" {
		t.Fatalf("east tls not inherited: %+v", east.TLS)
	}
	if east.ReconnectMin != DefaultBackoffInitial || east.ReconnectMax != DefaultBackoffCeiling {
		t.Fatalf("east backoff: %v %v", east.ReconnectMin, east.ReconnectMax)
	}
}

func TestClientNormalizeMutualExclusion(t *testing.T) {
	c := Client{
		Server:    "192.168.1.10:9000",
		Channels:  []ClientChannel{{ID: 1, Target: "127.0.0.1:2368"}},
		Upstreams: []Upstream{{Name: "east", Server: "192.168.1.11:9000", Channels: []ClientChannel{{ID: 1, Target: "127.0.0.1:2369"}}}},
	}
	c.SetDefaults()
	err := c.Normalize()
	if err == nil || !strings.Contains(err.Error(), "server") || !strings.Contains(err.Error(), "channels") {
		t.Fatalf("want mutual exclusion errors, got %v", err)
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
	if len(cli.Upstreams) != 1 || cli.Upstreams[0].Name != DefaultUpstreamName {
		t.Fatalf("expected normalized default upstream: %+v", cli.Upstreams)
	}
	multi, err := LoadClient(filepath.Join("..", "..", "examples", "client-multi.yaml"))
	if err != nil {
		t.Fatalf("client-multi example: %v", err)
	}
	if len(multi.Upstreams) != 2 || multi.Upstreams[0].Name != "east" {
		t.Fatalf("unexpected client-multi example: %+v", multi.Upstreams)
	}
	if multi.Upstreams[0].Token != "change-me" {
		t.Fatalf("east should inherit token, got %q", multi.Upstreams[0].Token)
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
	_, err = LoadClient(write("server: 'h:1'\nchannels: [{id: 1, target: '127.0.0.1:1'}]\nupstreams: [{name: a, server: 'h:2', channels: [{id: 1, target: '127.0.0.1:2'}]}]\n"))
	assertErr(t, err, "cannot be set together with upstreams")
	_, err = LoadClient(write("upstreams: []\n"))
	assertErr(t, err, "server")
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

func TestTransportDefaultsAndValidation(t *testing.T) {
	s := validServer()
	if s.Transport != TransportTCP {
		t.Fatalf("server default transport = %q", s.Transport)
	}
	c := validClient()
	if c.Upstreams[0].Transport != TransportTCP {
		t.Fatalf("client default transport = %q", c.Upstreams[0].Transport)
	}

	s.Transport = "udp"
	assertErr(t, s.Validate(), `transport: "udp" must be`)
	s.Transport = TransportQUIC
	assertErr(t, s.Validate(), `tls: must be enabled for transport "quic"`)
	s.TLS = ServerTLS{Enabled: true, CertFile: "c", KeyFile: "k"}
	if err := s.Validate(); err != nil {
		t.Fatalf("quic with tls: %v", err)
	}
	s.Transport = TransportKCP
	assertErr(t, s.Validate(), `tls: not supported with transport "kcp"`)
	s.TLS = ServerTLS{}
	if err := s.Validate(); err != nil {
		t.Fatalf("kcp defaults: %v", err)
	}
	s.KCP = KCP{MTU: 100, DataShards: 3, Interval: time.Millisecond}
	err := s.Validate()
	assertErr(t, err, "kcp: mtu")
	assertErr(t, err, "kcp: interval")
	assertErr(t, err, "data_shards and parity_shards")
}

func TestKCPResolved(t *testing.T) {
	k := KCP{}.Resolved()
	if !*k.NoDelay || !*k.NoCongestion || k.Interval != DefaultKCPInterval || k.Resend != DefaultKCPResend ||
		k.SendWindow != DefaultKCPWindow || k.RecvWindow != DefaultKCPWindow || k.MTU != DefaultKCPMTU {
		t.Fatalf("unexpected defaults: %+v", k)
	}
	off := false
	k = KCP{NoDelay: &off, MTU: 1200}.Resolved()
	if *k.NoDelay || k.MTU != 1200 {
		t.Fatalf("explicit values lost: %+v", k)
	}
}

func TestClientTransportInheritance(t *testing.T) {
	c := Client{
		Transport: TransportKCP,
		Token:     "t",
		KCP:       KCP{Key: "k"},
		Upstreams: []Upstream{
			{Name: "a", Server: "h:1", Channels: []ClientChannel{{ID: 1, Target: "127.0.0.1:1"}}},
			{
				Name: "b", Server: "h:2", Transport: TransportQUIC,
				TLS:      &ClientTLS{Enabled: true},
				Channels: []ClientChannel{{ID: 1, Target: "127.0.0.1:2"}},
			},
			{Name: "c", Server: "h:3", Transport: TransportTCP, Channels: []ClientChannel{{ID: 1, Target: "127.0.0.1:3"}}},
		},
	}
	c.SetDefaults()
	mustNormalize(t, &c)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	a, b, d := c.Upstreams[0], c.Upstreams[1], c.Upstreams[2]
	if a.Transport != TransportKCP || a.KCP.Key != "k" {
		t.Fatalf("a should inherit kcp: %+v", a)
	}
	if b.Transport != TransportQUIC || d.Transport != TransportTCP {
		t.Fatalf("explicit transports lost: %q %q", b.Transport, d.Transport)
	}
}

func TestClientTLSNotInheritedByKCP(t *testing.T) {
	c := Client{
		TLS: ClientTLS{Enabled: true},
		Upstreams: []Upstream{
			{Name: "tcp", Server: "h:1", Channels: []ClientChannel{{ID: 1, Target: "127.0.0.1:1"}}},
			{Name: "kcp", Server: "h:2", Transport: TransportKCP, Channels: []ClientChannel{{ID: 1, Target: "127.0.0.1:2"}}},
		},
	}
	c.SetDefaults()
	mustNormalize(t, &c)
	if err := c.Validate(); err != nil {
		t.Fatalf("kcp upstream must ignore top-level tls: %v", err)
	}
	if c.Upstreams[0].TLS.Enabled != true || c.Upstreams[1].TLS.Enabled {
		t.Fatalf("tls inheritance: %+v %+v", c.Upstreams[0].TLS, c.Upstreams[1].TLS)
	}

	c.Upstreams[1].TLS = &ClientTLS{Enabled: true}
	assertErr(t, c.Validate(), `upstreams[1]: tls: not supported with transport "kcp"`)
	c.Upstreams[1].Transport = TransportQUIC
	c.Upstreams[1].TLS = &ClientTLS{}
	assertErr(t, c.Validate(), `upstreams[1]: tls: must be enabled for transport "quic"`)
}

func TestLoadTransportExamples(t *testing.T) {
	ex := func(name string) string { return filepath.Join("..", "..", "examples", name) }
	for _, tc := range []struct{ file, transport string }{
		{"server-quic.yaml", TransportQUIC},
		{"server-kcp.yaml", TransportKCP},
	} {
		s, err := LoadServer(ex(tc.file))
		if err != nil || s.Transport != tc.transport {
			t.Fatalf("%s: transport %q, err %v", tc.file, s.Transport, err)
		}
	}
	for _, tc := range []struct{ file, transport string }{
		{"client-quic.yaml", TransportQUIC},
		{"client-kcp.yaml", TransportKCP},
	} {
		c, err := LoadClient(ex(tc.file))
		if err != nil || c.Upstreams[0].Transport != tc.transport {
			t.Fatalf("%s: %+v, err %v", tc.file, c.Upstreams, err)
		}
	}
}

func loadClientYAML(t *testing.T, content string) (Client, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadClient(p)
}

func TestCompressionYAMLForms(t *testing.T) {
	base := "server: 'h:1'\nchannels: [{id: 1, target: '127.0.0.1:1'}]\n"
	for _, tc := range []struct {
		yaml, algo string
		level      int
	}{
		{"", "none", 0},
		{"compression: zstd\n", "zstd", 1},
		{"compression: s2\n", "s2", 1},
		{"compression: none\n", "none", 0},
		{"compression: {algorithm: zstd, level: 3}\n", "zstd", 3},
		{"compression: {algorithm: s2}\n", "s2", 1},
	} {
		c, err := loadClientYAML(t, base+tc.yaml)
		if err != nil {
			t.Fatalf("%q: %v", tc.yaml, err)
		}
		got := c.Upstreams[0].Compression.Resolved()
		if got.Algorithm != tc.algo || got.Level != tc.level {
			t.Fatalf("%q: got %+v, want %s level %d", tc.yaml, got, tc.algo, tc.level)
		}
	}
}

func TestCompressionValidation(t *testing.T) {
	base := "server: 'h:1'\nchannels: [{id: 1, target: '127.0.0.1:1'}]\n"
	for yaml, want := range map[string]string{
		"compression: lz4\n":                         `compression: algorithm "lz4" must be`,
		"compression: {algorithm: zstd, level: 9}\n": "level for zstd must be between 1 and 4",
		"compression: {algorithm: s2, level: 4}\n":   "level for s2 must be between 1 and 3",
		"compression: {algorithm: none, level: 1}\n": `level is not used with algorithm "none"`,
		"compression: {algorithm: zstd, lvl: 1}\n":   "field lvl not found in compression",
		"compression: [zstd]\n":                      "compression must be a string or a mapping",
	} {
		_, err := loadClientYAML(t, base+yaml)
		assertErr(t, err, want)
	}

	s := validServer()
	s.Compression = Compression{Algorithm: "zstd", Level: 7}
	assertErr(t, s.Validate(), "level for zstd")
	s.Compression = Compression{Algorithm: "s2"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCompressionInheritance(t *testing.T) {
	c, err := loadClientYAML(t, `
compression: zstd
upstreams:
  - {name: a, server: 'h:1', channels: [{id: 1, target: '127.0.0.1:1'}]}
  - {name: b, server: 'h:2', compression: none, channels: [{id: 1, target: '127.0.0.1:2'}]}
  - {name: c, server: 'h:3', compression: {algorithm: s2, level: 2}, channels: [{id: 1, target: '127.0.0.1:3'}]}
`)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []Compression{{"zstd", 1}, {"none", 0}, {"s2", 2}} {
		if got := c.Upstreams[i].Compression.Resolved(); got != want {
			t.Fatalf("upstream %d: got %+v, want %+v", i, got, want)
		}
	}
}

func TestLoadCompressionExamples(t *testing.T) {
	ex := func(name string) string { return filepath.Join("..", "..", "examples", name) }
	s, err := LoadServer(ex("server-quic.yaml"))
	if err != nil || s.Compression.Resolved().Algorithm != CompressionZstd {
		t.Fatalf("server-quic: %+v %v", s.Compression, err)
	}
	s, err = LoadServer(ex("server-kcp.yaml"))
	if err != nil || s.Compression.Resolved().Algorithm != CompressionS2 {
		t.Fatalf("server-kcp: %+v %v", s.Compression, err)
	}
}
