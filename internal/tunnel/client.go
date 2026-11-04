package tunnel

import (
	"context"
	"crypto/tls"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"go.uber.org/zap"
	"net"
	"time"
)

type Client struct {
	cfg config.Client
	log *zap.Logger
}

func NewClient(c config.Client, l *zap.Logger) *Client { return &Client{c, l} }
func (c *Client) Run(ctx context.Context) error {
	for {
		e := c.once(ctx)
		if ctx.Err() != nil {
			return nil
		}
		c.log.Warn("tunnel disconnected", zap.Error(e))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Duration(max(c.cfg.ReconnectBackoff, 1)) * time.Second):
		}
	}
}
func (c *Client) once(ctx context.Context) error {
	conn, e := net.Dial("tcp", c.cfg.Server)
	if e != nil {
		return e
	}
	defer conn.Close()
	if c.cfg.TLS.CAFile != "" || c.cfg.TLS.InsecureSkipVerify {
		conn = tls.Client(conn, &tls.Config{InsecureSkipVerify: c.cfg.TLS.InsecureSkipVerify, ServerName: c.cfg.TLS.ServerName})
	}
	if e = WriteHandshake(conn, c.cfg.Token); e != nil {
		return e
	}
	if e = ReadHandshake(conn, c.cfg.Token); e != nil {
		return e
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		id, p, e := ReadFrame(conn)
		if e != nil {
			return e
		}
		for _, ch := range c.cfg.Channels {
			if ch.ID == id {
				u, e := net.ResolveUDPAddr("udp", ch.Target)
				if e != nil {
					return e
				}
				x, e := net.DialUDP("udp", nil, u)
				if e == nil {
					_, _ = x.Write(p)
					x.Close()
				}
				break
			}
		}
	}
}
