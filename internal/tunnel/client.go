package tunnel

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
)

// sink is the local UDP target of one channel.
type sink struct {
	conn     *net.UDPConn
	counters *stats.Counters
}

// upstreamRuntime is one dialer and its channel list.
type upstreamRuntime struct {
	cfg    config.Upstream
	dialer Dialer
}

// Client receives frames from one or more tunnel upstreams and re-emits each
// payload as a UDP datagram to that channel's local target. Each upstream
// reconnects with its own backoff.
type Client struct {
	cfg       config.Client
	upstreams []upstreamRuntime
	log       *zap.Logger
	stats     *stats.Registry
}

// NewClient builds a client using the TCP/TLS transport from cfg.
func NewClient(cfg config.Client, log *zap.Logger) (*Client, error) {
	cfg.SetDefaults()
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	upstreams := make([]upstreamRuntime, 0, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		tlsCfg := config.ClientTLS{}
		if u.TLS != nil {
			tlsCfg = *u.TLS
		}
		tlsConf, err := ClientTLSConfig(tlsCfg, u.Server)
		if err != nil {
			return nil, fmt.Errorf("upstream %q: %w", u.Name, err)
		}
		ids := make([]uint16, len(u.Channels))
		for i, ch := range u.Channels {
			ids[i] = ch.ID
		}
		upstreams = append(upstreams, upstreamRuntime{
			cfg: u,
			dialer: NewTCPDialer(TCPDialerOptions{
				Addr:     u.Server,
				Token:    u.Token,
				Channels: ids,
				TLS:      tlsConf,
			}),
		})
	}
	return newClient(cfg, upstreams, log), nil
}

// NewClientWithDialer builds a single-upstream client over any transport.
// cfg must describe exactly one upstream after Normalize.
func NewClientWithDialer(cfg config.Client, dialer Dialer, log *zap.Logger) (*Client, error) {
	cfg.SetDefaults()
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.Upstreams) != 1 {
		return nil, fmt.Errorf("NewClientWithDialer requires exactly one upstream, got %d", len(cfg.Upstreams))
	}
	return newClient(cfg, []upstreamRuntime{{cfg: cfg.Upstreams[0], dialer: dialer}}, log), nil
}

// NewClientWithDialers builds a client over the provided dialers, keyed by
// upstream name. Every configured upstream must have a dialer.
func NewClientWithDialers(cfg config.Client, dialers map[string]Dialer, log *zap.Logger) (*Client, error) {
	cfg.SetDefaults()
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	upstreams := make([]upstreamRuntime, 0, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		d, ok := dialers[u.Name]
		if !ok {
			return nil, fmt.Errorf("missing dialer for upstream %q", u.Name)
		}
		upstreams = append(upstreams, upstreamRuntime{cfg: u, dialer: d})
	}
	return newClient(cfg, upstreams, log), nil
}

func newClient(cfg config.Client, upstreams []upstreamRuntime, log *zap.Logger) *Client {
	reg := stats.New("client", log)
	for _, u := range upstreams {
		for _, ch := range u.cfg.Channels {
			reg.AddUpstreamChannel(u.cfg.Name, ch.ID, ch.Name)
		}
	}
	return &Client{cfg: cfg, upstreams: upstreams, log: log, stats: reg}
}

// Stats exposes the counters, mainly for tests.
func (c *Client) Stats() *stats.Registry { return c.stats }

// Run connects every upstream and forwards until ctx is canceled, which
// returns nil. Upstream sessions reconnect independently.
func (c *Client) Run(ctx context.Context) error {
	go c.stats.Run(ctx, c.cfg.StatsInterval, nil)

	var wg sync.WaitGroup
	errCh := make(chan error, len(c.upstreams))
	for _, u := range c.upstreams {
		u := u
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.runUpstream(ctx, u); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) runUpstream(ctx context.Context, u upstreamRuntime) error {
	log := c.log.With(zap.String("upstream", u.cfg.Name), zap.String("server", u.cfg.Server))
	sinks, err := c.openSinks(u.cfg)
	if err != nil {
		return fmt.Errorf("upstream %q: %w", u.cfg.Name, err)
	}
	defer func() {
		for _, s := range sinks {
			_ = s.conn.Close()
		}
	}()

	backoff := u.cfg.ReconnectMin
	for ctx.Err() == nil {
		sess, err := u.dialer.Dial(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Warn("connect failed", zap.Error(err), zap.Duration("retry_in", backoff))
			sleep(ctx, backoff)
			backoff = min(backoff*2, u.cfg.ReconnectMax)
			continue
		}
		backoff = u.cfg.ReconnectMin
		log.Info("connected", zap.String("peer", sess.RemoteAddr()))

		err = c.receive(ctx, u.cfg.Name, sess, sinks)
		if ctx.Err() != nil {
			break
		}
		log.Warn("disconnected", zap.Error(err), zap.Duration("retry_in", backoff))
		sleep(ctx, backoff)
	}
	return nil
}

func (c *Client) openSinks(u config.Upstream) (map[uint16]*sink, error) {
	sinks := make(map[uint16]*sink, len(u.Channels))
	for _, ch := range u.Channels {
		addr, err := net.ResolveUDPAddr("udp", ch.Target)
		if err != nil {
			return nil, err
		}
		conn, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			return nil, err
		}
		sinks[ch.ID] = &sink{
			conn:     conn,
			counters: c.stats.UpstreamChannel(u.Name, ch.ID),
		}
	}
	return sinks, nil
}

// receive pumps frames from sess into the UDP sinks until the session ends.
func (c *Client) receive(ctx context.Context, upstream string, sess Session, sinks map[uint16]*sink) error {
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()
	defer func() { _ = sess.Close() }()

	for {
		id, payload, err := sess.Receive()
		if err != nil {
			return err
		}
		s, ok := sinks[id]
		if !ok {
			c.stats.UpstreamGlobal(upstream).Drop(stats.UnknownChannel)
			continue
		}
		if _, err := s.conn.Write(payload); err != nil {
			s.counters.Drop(stats.WriteError)
			continue
		}
		s.counters.Add(len(payload))
	}
}

// sleep waits for d or until ctx is canceled.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
