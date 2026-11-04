package config

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"net"
	"os"
)

type TLS struct {
	CertFile           string `yaml:"cert_file"`
	KeyFile            string `yaml:"key_file"`
	CAFile             string `yaml:"ca_file"`
	ServerName         string `yaml:"server_name"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}
type Channel struct {
	ID              uint16 `yaml:"id"`
	Name            string `yaml:"name"`
	SourceIP        string `yaml:"source_ip"`
	SourcePort      uint16 `yaml:"source_port"`
	DestinationPort uint16 `yaml:"destination_port"`
	Target          string `yaml:"target"`
}
type Server struct {
	Listen        string    `yaml:"listen"`
	UDPListen     string    `yaml:"udp_listen"`
	Capture       string    `yaml:"capture"`
	Interface     string    `yaml:"interface"`
	QueueSize     int       `yaml:"queue_size"`
	StatsInterval int       `yaml:"stats_interval"`
	Token         string    `yaml:"token"`
	TLS           TLS       `yaml:"tls"`
	Channels      []Channel `yaml:"channels"`
}
type Client struct {
	Server           string    `yaml:"server"`
	ReconnectBackoff int       `yaml:"reconnect_backoff"`
	StatsInterval    int       `yaml:"stats_interval"`
	Token            string    `yaml:"token"`
	TLS              TLS       `yaml:"tls"`
	Channels         []Channel `yaml:"channels"`
}

func Load(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if e = yaml.Unmarshal(b, v); e != nil {
		return fmt.Errorf("parse config: %w", e)
	}
	switch x := v.(type) {
	case *Server:
		return x.Validate()
	case *Client:
		return x.Validate()
	}
	return nil
}
func (s *Server) Validate() error {
	if s.Listen == "" {
		s.Listen = ":9000"
	}
	if s.UDPListen == "" {
		s.UDPListen = ":2368"
	}
	if s.Capture == "" {
		s.Capture = "socket"
	}
	if s.Capture != "socket" && s.Capture != "afpacket" {
		return fmt.Errorf("capture must be socket or afpacket")
	}
	if len(s.Channels) == 0 {
		return fmt.Errorf("channels must not be empty")
	}
	return validateChannels(s.Channels)
}
func (c *Client) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server is required")
	}
	if _, e := net.ResolveTCPAddr("tcp", c.Server); e != nil {
		return fmt.Errorf("server: %w", e)
	}
	if len(c.Channels) == 0 {
		return fmt.Errorf("channels must not be empty")
	}
	return validateChannels(c.Channels)
}
func validateChannels(cs []Channel) error {
	seen := map[uint16]bool{}
	for _, c := range cs {
		if c.ID == 0 || seen[c.ID] {
			return fmt.Errorf("channel ids must be unique and nonzero")
		}
		seen[c.ID] = true
		if c.DestinationPort == 0 {
			return fmt.Errorf("channel %d has invalid destination_port", c.ID)
		}
		if c.Target != "" {
			if _, e := net.ResolveUDPAddr("udp", c.Target); e != nil {
				return fmt.Errorf("channel %d target: %w", c.ID, e)
			}
		}
	}
	return nil
}
