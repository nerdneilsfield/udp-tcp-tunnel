package tunnel

import (
	"context"
	"net"
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

// Client receives frames from the tunnel and re-emits each payload as a UDP
// datagram to the channel's local target, reconnecting with backoff.
type Client struct {
	cfg    config.Client
	dialer Dialer
	log    *zap.Logger
	stats  *stats.Registry
}

// NewClient builds a client using the TCP/TLS transport from cfg.
func NewClient(cfg config.Client, log *zap.Logger) (*Client, error) {
	tlsConf, err := ClientTLSConfig(cfg.TLS, cfg.Server)
	if err != nil {
		return nil, err
	}
	ids := make([]uint16, len(cfg.Channels))
	for i, ch := range cfg.Channels {
		ids[i] = ch.ID
	}
	dialer := NewTCPDialer(TCPDialerOptions{
		Addr:     cfg.Server,
		Token:    cfg.Token,
		Channels: ids,
		TLS:      tlsConf,
	})
	return NewClientWithDialer(cfg, dialer, log), nil
}

// NewClientWithDialer builds a client over any transport.
func NewClientWithDialer(cfg config.Client, dialer Dialer, log *zap.Logger) *Client {
	reg := stats.New("client", log)
	for _, ch := range cfg.Channels {
		reg.AddChannel(ch.ID, ch.Name)
	}
	return &Client{cfg: cfg, dialer: dialer, log: log, stats: reg}
}

// Stats exposes the counters, mainly for tests.
func (c *Client) Stats() *stats.Registry { return c.stats }

// Run connects and forwards until ctx is canceled, which returns nil.
func (c *Client) Run(ctx context.Context) error {
	sinks, err := c.openSinks()
	if err != nil {
		return err
	}
	defer func() {
		for _, s := range sinks {
			_ = s.conn.Close()
		}
	}()

	go c.stats.Run(ctx, c.cfg.StatsInterval, nil)

	backoff := c.cfg.ReconnectMin
	for ctx.Err() == nil {
		sess, err := c.dialer.Dial(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			c.log.Warn("connect failed", zap.String("server", c.cfg.Server),
				zap.Error(err), zap.Duration("retry_in", backoff))
			sleep(ctx, backoff)
			backoff = min(backoff*2, c.cfg.ReconnectMax)
			continue
		}
		backoff = c.cfg.ReconnectMin
		c.log.Info("connected", zap.String("server", sess.RemoteAddr()))

		err = c.receive(ctx, sess, sinks)
		if ctx.Err() != nil {
			break
		}
		c.log.Warn("disconnected", zap.Error(err), zap.Duration("retry_in", backoff))
		sleep(ctx, backoff)
	}
	return nil
}

func (c *Client) openSinks() (map[uint16]*sink, error) {
	sinks := make(map[uint16]*sink, len(c.cfg.Channels))
	for _, ch := range c.cfg.Channels {
		addr, err := net.ResolveUDPAddr("udp", ch.Target)
		if err != nil {
			return nil, err
		}
		conn, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			return nil, err
		}
		sinks[ch.ID] = &sink{conn: conn, counters: c.stats.Channel(ch.ID)}
	}
	return sinks, nil
}

// receive pumps frames from sess into the UDP sinks until the session ends.
func (c *Client) receive(ctx context.Context, sess Session, sinks map[uint16]*sink) error {
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
			c.stats.Global().Drop(stats.UnknownChannel)
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
