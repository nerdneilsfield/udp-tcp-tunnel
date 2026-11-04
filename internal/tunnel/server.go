//go:build server

package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/capture"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/routing"
	"go.uber.org/zap"
	"net"
	"sync"
	"time"
)

type Server struct {
	cfg  config.Server
	log  *zap.Logger
	mu   sync.RWMutex
	conn net.Conn
	q    chan capture.Packet
}

func NewServer(c config.Server, l *zap.Logger) *Server {
	return &Server{cfg: c, log: l, q: make(chan capture.Packet, max(c.QueueSize, 256))}
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func (s *Server) Run(ctx context.Context) error {
	ln, e := net.Listen("tcp", s.cfg.Listen)
	if e != nil {
		return e
	}
	defer ln.Close()
	go s.accept(ctx, ln)
	cap, e := s.capture()
	if e != nil {
		return e
	}
	defer cap.Close()
	return cap.Run(ctx, s.enqueue)
}
func (s *Server) capture() (capture.Capture, error) {
	if s.cfg.Capture == "afpacket" {
		return capture.NewAFPacket(s.cfg.Interface, "udp")
	}
	return capture.NewSocket(s.cfg.UDPListen)
}
func (s *Server) enqueue(p capture.Packet) {
	_, ok := routing.Match(s.cfg.Channels, p.Source, p.DestinationPort)
	if !ok {
		return
	}
	p.Payload = append([]byte(nil), p.Payload...)
	select {
	case s.q <- p:
	default:
	}
}
func (s *Server) accept(ctx context.Context, ln net.Listener) {
	for {
		c, e := ln.Accept()
		if e != nil {
			return
		}
		if s.cfg.TLS.CertFile != "" {
			cert, e := tls.LoadX509KeyPair(s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
			if e != nil {
				c.Close()
				continue
			}
			c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}})
		}
		if e := ReadHandshake(c, s.cfg.Token); e != nil {
			_ = c.Close()
			continue
		}
		go s.writer(ctx, c)
	}
}
func (s *Server) writer(ctx context.Context, c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Time{})
	if e := WriteHandshake(c, s.cfg.Token); e != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-s.q:
			cch, ok := routing.Match(s.cfg.Channels, p.Source, p.DestinationPort)
			if ok {
				if e := WriteFrame(c, cch.ID, p.Payload); e != nil {
					return
				}
			}
		}
	}
}

var _ = fmt.Sprintf
