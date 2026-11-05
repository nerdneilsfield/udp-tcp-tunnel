// Package config loads and validates the server and client YAML files.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Capture modes understood by the server.
const (
	CaptureSocket   = "socket"
	CaptureAFPacket = "afpacket"
)

// MaxTokenLen is the longest token the handshake can carry.
const MaxTokenLen = 255

// Defaults applied when a field is omitted.
const (
	DefaultListen         = ":9000"
	DefaultUDPBind        = "0.0.0.0"
	DefaultQueueSize      = 1024
	DefaultMaxPayload     = 65535
	DefaultStatsInterval  = 10 * time.Second
	DefaultBackoffInitial = time.Second
	DefaultBackoffCeiling = 30 * time.Second
	DefaultUpstreamName   = "default"
)

// Tunnel transports.
const (
	TransportTCP  = "tcp"
	TransportQUIC = "quic"
	TransportKCP  = "kcp"
)

// Compression algorithms.
const (
	CompressionNone = "none"
	CompressionZstd = "zstd"
	CompressionS2   = "s2"
)

// Compression levels: both codecs default to their fastest level.
const (
	DefaultCompressionLevel = 1
	maxZstdLevel            = 4 // 1 fastest, 2 default, 3 better, 4 best
	maxS2Level              = 3 // 1 standard, 2 better, 3 best
)

// KCP defaults. The tuning is the "fast" profile of kcp-go.
const (
	DefaultKCPInterval   = 20 * time.Millisecond
	DefaultKCPResend     = 2
	DefaultKCPWindow     = 1024
	DefaultKCPMTU        = 1350
	minKCPInterval       = 10 * time.Millisecond
	maxKCPInterval       = time.Second
	minKCPMTU            = 200
	maxKCPMTU            = 1500
	maxKCPWindow         = 32768
	maxKCPResend         = 10
	maxKCPTotalFECShards = 255
)

// KCP tunes the KCP transport. Zero values select the defaults. Key, data_shards
// and parity_shards must be identical on both ends; the other fields only
// affect the local side.
type KCP struct {
	Key          string        `yaml:"key"`
	NoDelay      *bool         `yaml:"nodelay"`
	Interval     time.Duration `yaml:"interval"`
	Resend       int           `yaml:"resend"`
	NoCongestion *bool         `yaml:"no_congestion"`
	SendWindow   int           `yaml:"send_window"`
	RecvWindow   int           `yaml:"recv_window"`
	MTU          int           `yaml:"mtu"`
	DataShards   int           `yaml:"data_shards"`
	ParityShards int           `yaml:"parity_shards"`
}

// Resolved returns a copy with every omitted field set to its default.
func (k KCP) Resolved() KCP {
	yes := true
	if k.NoDelay == nil {
		k.NoDelay = &yes
	}
	if k.NoCongestion == nil {
		k.NoCongestion = &yes
	}
	if k.Interval == 0 {
		k.Interval = DefaultKCPInterval
	}
	if k.Resend == 0 {
		k.Resend = DefaultKCPResend
	}
	if k.SendWindow == 0 {
		k.SendWindow = DefaultKCPWindow
	}
	if k.RecvWindow == 0 {
		k.RecvWindow = DefaultKCPWindow
	}
	if k.MTU == 0 {
		k.MTU = DefaultKCPMTU
	}
	return k
}

// validate reports problems with the resolved values, each prefixed by prefix.
func (k KCP) validate(prefix string) []error {
	k = k.Resolved()
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(prefix+format, args...))
	}
	if k.Interval < minKCPInterval || k.Interval > maxKCPInterval {
		add("interval: must be between %s and %s", minKCPInterval, maxKCPInterval)
	}
	if k.Resend < 0 || k.Resend > maxKCPResend {
		add("resend: must be between 0 and %d", maxKCPResend)
	}
	if k.SendWindow < 1 || k.SendWindow > maxKCPWindow {
		add("send_window: must be between 1 and %d", maxKCPWindow)
	}
	if k.RecvWindow < 1 || k.RecvWindow > maxKCPWindow {
		add("recv_window: must be between 1 and %d", maxKCPWindow)
	}
	if k.MTU < minKCPMTU || k.MTU > maxKCPMTU {
		add("mtu: must be between %d and %d", minKCPMTU, maxKCPMTU)
	}
	if (k.DataShards == 0) != (k.ParityShards == 0) || k.DataShards < 0 || k.ParityShards < 0 {
		add("data_shards and parity_shards: set both to positive values, or neither")
	} else if k.DataShards+k.ParityShards > maxKCPTotalFECShards {
		add("data_shards + parity_shards: at most %d", maxKCPTotalFECShards)
	}
	return errs
}

// Compression selects payload compression. In YAML it is either a string
// (the algorithm, with the default level) or a mapping with algorithm and level.
type Compression struct {
	Algorithm string `yaml:"algorithm"`
	Level     int    `yaml:"level"`
}

// UnmarshalYAML accepts `compression: zstd` as shorthand for
// `compression: {algorithm: zstd}` and rejects unknown keys in the mapping.
func (c *Compression) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var name string
		if err := node.Decode(&name); err != nil {
			return err
		}
		*c = Compression{Algorithm: name}
		return nil
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			if key := node.Content[i].Value; key != "algorithm" && key != "level" {
				return fmt.Errorf("line %d: field %s not found in compression", node.Content[i].Line, key)
			}
		}
		type plain Compression
		var p plain
		if err := node.Decode(&p); err != nil {
			return err
		}
		*c = Compression(p)
		return nil
	default:
		return fmt.Errorf("line %d: compression must be a string or a mapping", node.Line)
	}
}

// Resolved fills the defaults: algorithm none, and the fastest level.
func (c Compression) Resolved() Compression {
	if c.Algorithm == "" {
		c.Algorithm = CompressionNone
	}
	if c.Algorithm != CompressionNone && c.Level == 0 {
		c.Level = DefaultCompressionLevel
	}
	return c
}

func (c Compression) validate(prefix string) []error {
	c = c.Resolved()
	switch c.Algorithm {
	case CompressionNone:
		if c.Level != 0 {
			return []error{fmt.Errorf("%scompression: level is not used with algorithm %q", prefix, CompressionNone)}
		}
	case CompressionZstd:
		if c.Level < 1 || c.Level > maxZstdLevel {
			return []error{fmt.Errorf("%scompression: level for zstd must be between 1 and %d", prefix, maxZstdLevel)}
		}
	case CompressionS2:
		if c.Level < 1 || c.Level > maxS2Level {
			return []error{fmt.Errorf("%scompression: level for s2 must be between 1 and %d", prefix, maxS2Level)}
		}
	default:
		return []error{fmt.Errorf("%scompression: algorithm %q must be %q, %q or %q", prefix, c.Algorithm, CompressionNone, CompressionZstd, CompressionS2)}
	}
	return nil
}

// ServerTLS configures TLS on the tunnel listener.
type ServerTLS struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// ClientTLS configures TLS on the tunnel connection.
type ClientTLS struct {
	Enabled            bool   `yaml:"enabled"`
	CAFile             string `yaml:"ca_file"`
	ServerName         string `yaml:"server_name"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

// ServerChannel maps captured UDP datagrams to a channel id.
type ServerChannel struct {
	ID              uint16 `yaml:"id"`
	Name            string `yaml:"name"`
	SourceIP        string `yaml:"source_ip"`
	SourcePort      uint16 `yaml:"source_port"`
	DestinationPort uint16 `yaml:"destination_port"`
}

// ClientChannel maps a channel id to a local UDP target.
type ClientChannel struct {
	ID     uint16 `yaml:"id"`
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
}

// Upstream is one tunnel server the client dials, with its own channel list.
// After Normalize, every client config has at least one Upstream.
type Upstream struct {
	Name         string          `yaml:"name"`
	Server       string          `yaml:"server"`
	Transport    string          `yaml:"transport"`
	Compression  *Compression    `yaml:"compression"`
	Token        string          `yaml:"token"`
	TLS          *ClientTLS      `yaml:"tls"`
	KCP          *KCP            `yaml:"kcp"`
	ReconnectMin time.Duration   `yaml:"reconnect_backoff_min"`
	ReconnectMax time.Duration   `yaml:"reconnect_backoff_max"`
	Channels     []ClientChannel `yaml:"channels"`
}

// Server is the configuration of udp-tcp-tunnel-server.
type Server struct {
	Listen        string          `yaml:"listen"`
	Transport     string          `yaml:"transport"`
	Compression   Compression     `yaml:"compression"`
	Capture       string          `yaml:"capture"`
	UDPBind       string          `yaml:"udp_bind"`
	Interface     string          `yaml:"interface"`
	QueueSize     int             `yaml:"queue_size"`
	MaxPayload    int             `yaml:"max_payload"`
	StatsInterval time.Duration   `yaml:"stats_interval"`
	Token         string          `yaml:"token"`
	TLS           ServerTLS       `yaml:"tls"`
	KCP           KCP             `yaml:"kcp"`
	Channels      []ServerChannel `yaml:"channels"`
}

// Client is the configuration of udp-tcp-tunnel-client.
//
// Two shapes are accepted:
//   - single upstream: top-level server + channels (and shared token/tls/backoff)
//   - multiple upstreams: an upstreams list; each entry has its own server and channels
//
// The two shapes are mutually exclusive. Normalize turns the single-upstream
// shape into one Upstream named "default" and fills omitted per-upstream
// transport, token, TLS, KCP and backoff from the top-level defaults.
type Client struct {
	Server        string          `yaml:"server"`
	Transport     string          `yaml:"transport"`
	Compression   Compression     `yaml:"compression"`
	ReconnectMin  time.Duration   `yaml:"reconnect_backoff_min"`
	ReconnectMax  time.Duration   `yaml:"reconnect_backoff_max"`
	StatsInterval time.Duration   `yaml:"stats_interval"`
	Token         string          `yaml:"token"`
	TLS           ClientTLS       `yaml:"tls"`
	KCP           KCP             `yaml:"kcp"`
	Channels      []ClientChannel `yaml:"channels"`
	Upstreams     []Upstream      `yaml:"upstreams"`
}

// LoadServer reads, defaults and validates a server config file.
func LoadServer(path string) (Server, error) {
	var cfg Server
	if err := load(path, &cfg); err != nil {
		return Server{}, err
	}
	cfg.SetDefaults()
	if err := cfg.Validate(); err != nil {
		return Server{}, fmt.Errorf("invalid server config %s: %w", path, err)
	}
	return cfg, nil
}

// LoadClient reads, defaults, normalizes and validates a client config file.
func LoadClient(path string) (Client, error) {
	var cfg Client
	if err := load(path, &cfg); err != nil {
		return Client{}, err
	}
	cfg.SetDefaults()
	if err := cfg.Normalize(); err != nil {
		return Client{}, fmt.Errorf("invalid client config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Client{}, fmt.Errorf("invalid client config %s: %w", path, err)
	}
	return cfg, nil
}

func load(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

// SetDefaults fills omitted optional fields.
func (s *Server) SetDefaults() {
	if s.Listen == "" {
		s.Listen = DefaultListen
	}
	if s.Transport == "" {
		s.Transport = TransportTCP
	}
	if s.Capture == "" {
		s.Capture = CaptureSocket
	}
	if s.UDPBind == "" {
		s.UDPBind = DefaultUDPBind
	}
	if s.QueueSize == 0 {
		s.QueueSize = DefaultQueueSize
	}
	if s.MaxPayload == 0 {
		s.MaxPayload = DefaultMaxPayload
	}
	if s.StatsInterval == 0 {
		s.StatsInterval = DefaultStatsInterval
	}
}

// SetDefaults fills omitted optional top-level fields.
func (c *Client) SetDefaults() {
	if c.Transport == "" {
		c.Transport = TransportTCP
	}
	if c.ReconnectMin == 0 {
		c.ReconnectMin = DefaultBackoffInitial
	}
	if c.ReconnectMax == 0 {
		c.ReconnectMax = DefaultBackoffCeiling
	}
	if c.StatsInterval == 0 {
		c.StatsInterval = DefaultStatsInterval
	}
}

// Normalize turns the single-upstream shape into Upstreams and applies
// top-level transport, compression, token, TLS, KCP and backoff defaults to each upstream entry.
func (c *Client) Normalize() error {
	hasServer := c.Server != ""
	hasChannels := len(c.Channels) > 0
	hasUpstreams := len(c.Upstreams) > 0

	var errs []error
	if hasUpstreams && (hasServer || hasChannels) {
		if hasServer {
			errs = append(errs, errors.New("server: cannot be set together with upstreams"))
		}
		if hasChannels {
			errs = append(errs, errors.New("channels: cannot be set together with upstreams"))
		}
		return errors.Join(errs...)
	}
	if !hasUpstreams {
		tls := c.TLS
		kcp := c.KCP
		comp := c.Compression
		c.Upstreams = []Upstream{{
			Name:         DefaultUpstreamName,
			Server:       c.Server,
			Transport:    c.Transport,
			Compression:  &comp,
			Token:        c.Token,
			TLS:          &tls,
			KCP:          &kcp,
			ReconnectMin: c.ReconnectMin,
			ReconnectMax: c.ReconnectMax,
			Channels:     c.Channels,
		}}
		return nil
	}
	for i := range c.Upstreams {
		u := &c.Upstreams[i]
		if u.Token == "" {
			u.Token = c.Token
		}
		if u.Transport == "" {
			u.Transport = c.Transport
		}
		if u.TLS == nil {
			// KCP has no TLS layer; do not let a top-level block meant for
			// other upstreams enable TLS validation on it.
			tls := c.TLS
			if u.Transport == TransportKCP {
				tls = ClientTLS{}
			}
			u.TLS = &tls
		}
		if u.KCP == nil {
			kcp := c.KCP
			u.KCP = &kcp
		}
		if u.Compression == nil {
			comp := c.Compression
			u.Compression = &comp
		}
		if u.ReconnectMin == 0 {
			u.ReconnectMin = c.ReconnectMin
		}
		if u.ReconnectMax == 0 {
			u.ReconnectMax = c.ReconnectMax
		}
	}
	return nil
}

// Validate reports every problem found in the server config.
func (s *Server) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if err := checkHostPort(s.Listen, false); err != nil {
		add("listen: %v", err)
	}
	errs = append(errs, validateTransport("", s.Transport, s.TLS.Enabled)...)
	errs = append(errs, s.Compression.validate("")...)
	if s.Transport == TransportKCP {
		errs = append(errs, s.KCP.validate("kcp: ")...)
	}
	switch s.Capture {
	case CaptureSocket:
		if _, err := netip.ParseAddr(s.UDPBind); err != nil {
			add("udp_bind: %q is not an IP address", s.UDPBind)
		}
	case CaptureAFPacket:
		if s.Interface == "" {
			add("interface: required when capture is %q", CaptureAFPacket)
		}
	default:
		add("capture: %q must be %q or %q", s.Capture, CaptureSocket, CaptureAFPacket)
	}
	if s.QueueSize < 1 {
		add("queue_size: must be at least 1")
	}
	if s.MaxPayload < 1 || s.MaxPayload > DefaultMaxPayload {
		add("max_payload: must be between 1 and %d", DefaultMaxPayload)
	}
	if s.StatsInterval <= 0 {
		add("stats_interval: must be positive")
	}
	if len(s.Token) > MaxTokenLen {
		add("token: longer than %d bytes", MaxTokenLen)
	}
	if s.TLS.Enabled && (s.TLS.CertFile == "" || s.TLS.KeyFile == "") {
		add("tls: cert_file and key_file are required when tls.enabled is true")
	}
	if !s.TLS.Enabled && (s.TLS.CertFile != "" || s.TLS.KeyFile != "") {
		add("tls: cert_file/key_file are set but tls.enabled is false")
	}
	errs = append(errs, validateServerChannels(s.Channels)...)
	return errors.Join(errs...)
}

// Validate reports every problem found in the client config.
// Call Normalize before Validate so Upstreams is populated.
func (c *Client) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if c.ReconnectMin <= 0 {
		add("reconnect_backoff_min: must be positive")
	}
	if c.ReconnectMax < c.ReconnectMin {
		add("reconnect_backoff_max: must not be below reconnect_backoff_min")
	}
	if c.StatsInterval <= 0 {
		add("stats_interval: must be positive")
	}
	if len(c.Token) > MaxTokenLen {
		add("token: longer than %d bytes", MaxTokenLen)
	}
	if !c.TLS.Enabled && (c.TLS.CAFile != "" || c.TLS.ServerName != "" || c.TLS.InsecureSkipVerify) {
		add("tls: options are set but tls.enabled is false")
	}
	if len(c.Upstreams) == 0 {
		add("upstreams: at least one upstream is required")
		return errors.Join(errs...)
	}
	names := make(map[string]int)
	for i, u := range c.Upstreams {
		prefix := fmt.Sprintf("upstreams[%d]", i)
		if u.Name == "" {
			add("%s: name is required", prefix)
		} else if first, dup := names[u.Name]; dup {
			add("%s: name %q already used by upstreams[%d]", prefix, u.Name, first)
		} else {
			names[u.Name] = i
		}
		if err := checkHostPort(u.Server, true); err != nil {
			add("%s: server: %v", prefix, err)
		}
		if len(u.Token) > MaxTokenLen {
			add("%s: token: longer than %d bytes", prefix, MaxTokenLen)
		}
		if u.ReconnectMin <= 0 {
			add("%s: reconnect_backoff_min: must be positive", prefix)
		}
		if u.ReconnectMax < u.ReconnectMin {
			add("%s: reconnect_backoff_max: must not be below reconnect_backoff_min", prefix)
		}
		tls := ClientTLS{}
		if u.TLS != nil {
			tls = *u.TLS
		}
		errs = append(errs, validateTransport(prefix+": ", u.Transport, tls.Enabled)...)
		if u.Compression != nil {
			errs = append(errs, u.Compression.validate(prefix+": ")...)
		}
		if u.Transport == TransportKCP && u.KCP != nil {
			errs = append(errs, u.KCP.validate(prefix+": kcp: ")...)
		}
		if !tls.Enabled && (tls.CAFile != "" || tls.ServerName != "" || tls.InsecureSkipVerify) {
			add("%s: tls: options are set but tls.enabled is false", prefix)
		}
		for _, err := range validateClientChannels(u.Channels) {
			add("%s: %v", prefix, err)
		}
	}
	return errors.Join(errs...)
}

// validateTransport checks the transport name and its TLS requirement.
func validateTransport(prefix, transport string, tlsEnabled bool) []error {
	switch transport {
	case TransportTCP:
		return nil
	case TransportQUIC:
		if !tlsEnabled {
			return []error{fmt.Errorf("%stls: must be enabled for transport %q", prefix, TransportQUIC)}
		}
		return nil
	case TransportKCP:
		if tlsEnabled {
			return []error{fmt.Errorf("%stls: not supported with transport %q; set kcp.key to encrypt", prefix, TransportKCP)}
		}
		return nil
	default:
		return []error{fmt.Errorf("%stransport: %q must be %q, %q or %q", prefix, transport, TransportTCP, TransportQUIC, TransportKCP)}
	}
}

func validateServerChannels(channels []ServerChannel) []error {
	if len(channels) == 0 {
		return []error{errors.New("channels: at least one channel is required")}
	}
	var errs []error
	ids := make(map[uint16]int)
	rules := make(map[string]int)
	for i, ch := range channels {
		if ch.ID == 0 {
			errs = append(errs, fmt.Errorf("channels[%d]: id must be non-zero", i))
		} else if first, dup := ids[ch.ID]; dup {
			errs = append(errs, fmt.Errorf("channels[%d]: id %d already used by channels[%d]", i, ch.ID, first))
		} else {
			ids[ch.ID] = i
		}
		if ch.DestinationPort == 0 {
			errs = append(errs, fmt.Errorf("channels[%d]: destination_port is required", i))
		}
		if ch.SourceIP != "" {
			if _, err := netip.ParseAddr(ch.SourceIP); err != nil {
				errs = append(errs, fmt.Errorf("channels[%d]: source_ip %q is not an IP address", i, ch.SourceIP))
			}
		}
		key := fmt.Sprintf("%s|%d|%d", ch.SourceIP, ch.SourcePort, ch.DestinationPort)
		if first, dup := rules[key]; dup {
			errs = append(errs, fmt.Errorf("channels[%d]: same match rule as channels[%d]", i, first))
		} else {
			rules[key] = i
		}
	}
	return errs
}

func validateClientChannels(channels []ClientChannel) []error {
	if len(channels) == 0 {
		return []error{errors.New("channels: at least one channel is required")}
	}
	var errs []error
	ids := make(map[uint16]int)
	for i, ch := range channels {
		if ch.ID == 0 {
			errs = append(errs, fmt.Errorf("channels[%d]: id must be non-zero", i))
		} else if first, dup := ids[ch.ID]; dup {
			errs = append(errs, fmt.Errorf("channels[%d]: id %d already used by channels[%d]", i, ch.ID, first))
		} else {
			ids[ch.ID] = i
		}
		if err := checkHostPort(ch.Target, true); err != nil {
			errs = append(errs, fmt.Errorf("channels[%d]: target: %w", i, err))
		}
	}
	return errs
}

// checkHostPort validates a host:port string. A host is mandatory when
// needHost is set; the port must always be 1-65535.
func checkHostPort(addr string, needHost bool) error {
	if addr == "" {
		return errors.New("required")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not host:port", addr)
	}
	if needHost && host == "" {
		return fmt.Errorf("%q has no host", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q has an invalid port", addr)
	}
	return nil
}
