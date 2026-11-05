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
	Token        string          `yaml:"token"`
	TLS          *ClientTLS      `yaml:"tls"`
	ReconnectMin time.Duration   `yaml:"reconnect_backoff_min"`
	ReconnectMax time.Duration   `yaml:"reconnect_backoff_max"`
	Channels     []ClientChannel `yaml:"channels"`
}

// Server is the configuration of udp-tcp-tunnel-server.
type Server struct {
	Listen        string          `yaml:"listen"`
	Capture       string          `yaml:"capture"`
	UDPBind       string          `yaml:"udp_bind"`
	Interface     string          `yaml:"interface"`
	QueueSize     int             `yaml:"queue_size"`
	MaxPayload    int             `yaml:"max_payload"`
	StatsInterval time.Duration   `yaml:"stats_interval"`
	Token         string          `yaml:"token"`
	TLS           ServerTLS       `yaml:"tls"`
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
// token, TLS and backoff from the top-level defaults.
type Client struct {
	Server        string          `yaml:"server"`
	ReconnectMin  time.Duration   `yaml:"reconnect_backoff_min"`
	ReconnectMax  time.Duration   `yaml:"reconnect_backoff_max"`
	StatsInterval time.Duration   `yaml:"stats_interval"`
	Token         string          `yaml:"token"`
	TLS           ClientTLS       `yaml:"tls"`
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
// top-level token, TLS and backoff defaults to each upstream entry.
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
		c.Upstreams = []Upstream{{
			Name:         DefaultUpstreamName,
			Server:       c.Server,
			Token:        c.Token,
			TLS:          &tls,
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
		if u.TLS == nil {
			tls := c.TLS
			u.TLS = &tls
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
		if !tls.Enabled && (tls.CAFile != "" || tls.ServerName != "" || tls.InsecureSkipVerify) {
			add("%s: tls: options are set but tls.enabled is false", prefix)
		}
		for _, err := range validateClientChannels(u.Channels) {
			add("%s: %v", prefix, err)
		}
	}
	return errors.Join(errs...)
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
