// Package server wires a packet capture to the tunnel transport.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/capture"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/routing"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/tunnel"
)

// Option customizes a Server, mainly for tests.
type Option func(*Server)

// WithCapture replaces the capture built from the config.
func WithCapture(c capture.Capture) Option { return func(s *Server) { s.capture = c } }

// WithListener replaces the TCP listener built from the config.
func WithListener(l tunnel.Listener) Option { return func(s *Server) { s.listener = l } }

// frame is a queued datagram. Its payload is exclusively owned by the
// queue, so the capture buffer can be reused immediately.
type frame struct {
	channel  uint16
	payload  []byte
	counters *stats.Counters
}

// Server forwards captured UDP datagrams to a single connected client.
//
// Queue policy: the queue between capture and the network writer is
// bounded. When it is full the newest packet is dropped and counted as
// queue_full for its channel, so the capture loop never blocks. While no
// client is connected packets are dropped (no_client) instead of buffered.
type Server struct {
	cfg    config.Server
	log    *zap.Logger
	router *routing.Router
	stats  *stats.Registry

	channels map[uint16]*stats.Counters
	queue    chan frame

	capture  capture.Capture
	listener tunnel.Listener
	ready    chan struct{}

	connected atomic.Bool
	mu        sync.Mutex
	active    tunnel.Session
	wg        sync.WaitGroup
}

// New builds a server from a validated config.
func New(cfg config.Server, log *zap.Logger, opts ...Option) *Server {
	s := &Server{
		cfg:      cfg,
		log:      log,
		router:   routing.New(cfg.Channels),
		stats:    stats.New("server", log),
		channels: make(map[uint16]*stats.Counters, len(cfg.Channels)),
		queue:    make(chan frame, cfg.QueueSize),
		ready:    make(chan struct{}),
	}
	for _, ch := range cfg.Channels {
		s.channels[ch.ID] = s.stats.AddChannel(ch.ID, ch.Name)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Stats exposes the counters, mainly for tests.
func (s *Server) Stats() *stats.Registry { return s.stats }

// Ready is closed once the listener and capture are set up.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// ListenAddr returns the tunnel listen address. It is only valid after
// Ready is closed.
func (s *Server) ListenAddr() string { return s.listener.Addr() }

// Connected reports whether a client is currently attached.
func (s *Server) Connected() bool { return s.connected.Load() }

// Run captures and forwards until ctx is canceled (returns nil) or the
// capture fails.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if s.listener == nil {
		l, err := s.listen()
		if err != nil {
			return err
		}
		s.listener = l
	}
	defer func() { _ = s.listener.Close() }()

	if s.capture == nil {
		c, err := newCapture(s.cfg)
		if err != nil {
			return err
		}
		s.capture = c
	}
	defer func() { _ = s.capture.Close() }()

	close(s.ready)
	s.warnIfExposed()
	s.log.Info("server started",
		zap.String("listen", s.listener.Addr()),
		zap.String("capture", s.cfg.Capture),
		zap.Int("channels", len(s.cfg.Channels)))

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(ctx)
	}()
	go func() {
		defer s.wg.Done()
		s.stats.Run(ctx, s.cfg.StatsInterval, s.refreshCaptureStats)
	}()

	err := s.capture.Run(ctx, s.handle)

	cancel()
	s.closeActive()
	_ = s.listener.Close()
	s.wg.Wait()
	return err
}

func (s *Server) listen() (tunnel.Listener, error) {
	tlsConf, err := tunnel.ServerTLSConfig(s.cfg.TLS)
	if err != nil {
		return nil, err
	}
	ids := make([]uint16, len(s.cfg.Channels))
	for i, ch := range s.cfg.Channels {
		ids[i] = ch.ID
	}
	l, err := tunnel.ListenTCP(s.cfg.Listen, tunnel.TCPServerOptions{
		Token:    s.cfg.Token,
		Channels: ids,
		TLS:      tlsConf,
		Log:      s.log,
	})
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", s.cfg.Listen, err)
	}
	return l, nil
}

func newCapture(cfg config.Server) (capture.Capture, error) {
	if cfg.Capture == config.CaptureAFPacket {
		rules := make([]capture.FilterRule, 0, len(cfg.Channels))
		for _, ch := range cfg.Channels {
			rule := capture.FilterRule{SrcPort: ch.SourcePort, DstPort: ch.DestinationPort}
			if ch.SourceIP != "" {
				addr, err := netip.ParseAddr(ch.SourceIP)
				if err != nil {
					return nil, err
				}
				rule.Src = addr.Unmap()
			}
			rules = append(rules, rule)
		}
		return capture.NewAFPacket(cfg.Interface, rules)
	}

	var ports []uint16
	seen := make(map[uint16]bool)
	for _, ch := range cfg.Channels {
		if !seen[ch.DestinationPort] {
			seen[ch.DestinationPort] = true
			ports = append(ports, ch.DestinationPort)
		}
	}
	return capture.NewSocket(cfg.UDPBind, ports)
}

// warnIfExposed logs when the tunnel is reachable beyond loopback without
// both TLS and a token.
func (s *Server) warnIfExposed() {
	host, _, err := net.SplitHostPort(s.listener.Addr())
	if err != nil || isLoopback(host) {
		return
	}
	if !s.cfg.TLS.Enabled || s.cfg.Token == "" {
		s.log.Warn("tunnel listens on a non-loopback address without full protection",
			zap.String("listen", s.listener.Addr()),
			zap.Bool("tls", s.cfg.TLS.Enabled),
			zap.Bool("token", s.cfg.Token != ""))
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// handle is the capture callback. It never blocks.
func (s *Server) handle(p capture.Packet) {
	id, ok := s.router.Match(p.Src, p.SrcPort, p.DstPort)
	if !ok {
		s.stats.Global().Drop(stats.UnknownChannel)
		return
	}
	counters := s.channels[id]

	switch {
	case len(p.Payload) > s.cfg.MaxPayload:
		counters.Drop(stats.Oversized)
	case !s.connected.Load():
		counters.Drop(stats.NoClient)
	default:
		select {
		case s.queue <- frame{channel: id, payload: p.Payload, counters: counters}:
		default:
			counters.Drop(stats.QueueFull) // drop the newest packet
		}
	}
}

func (s *Server) refreshCaptureStats() {
	st := s.capture.Stats()
	s.stats.Global().SetDrops(stats.CaptureDrop, st.KernelDrops)
	s.stats.Global().SetDrops(stats.CaptureFreeze, st.QueueFreezes)
	s.stats.Global().SetDrops(stats.Fragment, st.Fragments)
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		sess, err := s.listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Warn("accept failed", zap.Error(err))
			continue
		}
		s.attach(ctx, sess)
	}
}

// attach makes sess the active client, replacing any previous one (a
// reconnecting client usually means the old connection is half dead).
func (s *Server) attach(ctx context.Context, sess tunnel.Session) {
	s.mu.Lock()
	if s.active != nil {
		_ = s.active.Close()
	}
	s.active = sess
	s.drainQueue() // never deliver packets captured before this client connected
	s.connected.Store(true)
	s.mu.Unlock()

	s.log.Info("client connected", zap.String("remote", sess.RemoteAddr()))
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.serve(ctx, sess)
	}()
}

func (s *Server) drainQueue() {
	for {
		select {
		case <-s.queue:
		default:
			return
		}
	}
}

func (s *Server) closeActive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		_ = s.active.Close()
	}
}

// serve writes queued frames to sess until it fails or ctx ends.
func (s *Server) serve(ctx context.Context, sess tunnel.Session) {
	defer func() {
		_ = sess.Close()
		s.mu.Lock()
		if s.active == sess {
			s.active = nil
			s.connected.Store(false)
		}
		s.mu.Unlock()
	}()

	// The client never sends frames; Receive only reports disconnects.
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		for {
			if _, _, err := sess.Receive(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-gone:
			s.log.Info("client disconnected", zap.String("remote", sess.RemoteAddr()))
			return
		case f := <-s.queue:
			// A reconnecting client closes the previous session; stop so we
			// do not race the new serve loop for the shared queue.
			s.mu.Lock()
			stillActive := s.active == sess
			s.mu.Unlock()
			if !stillActive {
				return
			}
			if err := sess.Send(f.channel, f.payload); err != nil {
				f.counters.Drop(stats.WriteError)
				s.log.Warn("send failed", zap.String("remote", sess.RemoteAddr()), zap.Error(err))
				return
			}
			f.counters.Add(len(f.payload))
		}
	}
}
