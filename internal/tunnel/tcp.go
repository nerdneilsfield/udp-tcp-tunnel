package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
)

const (
	handshakeTimeout  = 10 * time.Second
	writeTimeout      = 15 * time.Second
	userTimeout       = 30 * time.Second // TCP_USER_TIMEOUT: max unacked data lifetime
	keepAliveIdle     = 15 * time.Second
	keepAliveInterval = 5 * time.Second
	keepAliveCount    = 3
	readBufferSize    = 64 << 10
)

// keepAliveConfig detects dead idle peers in about keepAliveIdle +
// keepAliveCount*keepAliveInterval (~30s). Continuous writers still need
// userTimeout: keepalive probes only run while the connection is idle.
func keepAliveConfig() net.KeepAliveConfig {
	return net.KeepAliveConfig{
		Enable:   true,
		Idle:     keepAliveIdle,
		Interval: keepAliveInterval,
		Count:    keepAliveCount,
	}
}

// tuneTCP applies TCP_NODELAY, aggressive keepalive and (on Linux)
// TCP_USER_TIMEOUT so a blackholed peer is dropped in tens of seconds
// instead of the kernel tcp_retries2 window (~8–15 minutes).
func tuneTCP(conn net.Conn) {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tc.SetNoDelay(true)
	_ = tc.SetKeepAliveConfig(keepAliveConfig())
	setTCPUserTimeout(tc, userTimeout)
}

// TCPServerOptions configures ListenTCP.
type TCPServerOptions struct {
	Token    string
	Channels []uint16
	TLS      *tls.Config // nil for plain TCP
	Log      *zap.Logger

	Compression config.Compression
	CompStats   *stats.CompressionCounters // optional
}

// TCPListener is the server side of the TCP transport.
type TCPListener struct {
	*baseListener
	ln    net.Listener
	opts  TCPServerOptions
	known map[uint16]struct{}
	comp  *Compressor
}

// ListenTCP starts listening and performing handshakes in the background.
func ListenTCP(addr string, opts TCPServerOptions) (*TCPListener, error) {
	lc := net.ListenConfig{KeepAliveConfig: keepAliveConfig()}
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	if opts.Log == nil {
		opts.Log = zap.NewNop()
	}
	comp, err := NewCompressor(opts.Compression, opts.CompStats)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	l := &TCPListener{
		baseListener: newBaseListener(ln.Addr().String(), ln.Close),
		ln:           ln,
		opts:         opts,
		known:        knownChannels(opts.Channels),
		comp:         comp,
	}
	go l.acceptLoop()
	return l, nil
}

func knownChannels(ids []uint16) map[uint16]struct{} {
	known := make(map[uint16]struct{}, len(ids))
	for _, id := range ids {
		known[id] = struct{}{}
	}
	return known
}

func (l *TCPListener) acceptLoop() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			l.opts.Log.Warn("accept failed", zap.Error(err))
			select {
			case <-time.After(100 * time.Millisecond):
			case <-l.done:
				return
			}
			continue
		}
		go l.handshake(conn)
	}
}

func (l *TCPListener) handshake(raw net.Conn) {
	tuneTCP(raw)
	conn := raw
	if l.opts.TLS != nil {
		conn = tls.Server(raw, l.opts.TLS)
	}
	log := l.opts.Log.With(zap.String("remote", raw.RemoteAddr().String()))

	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := serverHandshake(conn, l.opts.Token, l.known, l.comp.Algo(), log); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	l.deliver(newConnSession(conn, 0, l.comp))
}

// TCPDialerOptions configures a TCP dialer.
type TCPDialerOptions struct {
	Addr     string
	Token    string
	Channels []uint16
	TLS      *tls.Config // nil for plain TCP
	Timeout  time.Duration

	Compression config.Compression
	CompStats   *stats.CompressionCounters // optional
}

// TCPDialer is the client side of the TCP transport.
type TCPDialer struct {
	opts TCPDialerOptions
	comp *Compressor
	err  error // compression setup error, reported by Dial
}

// NewTCPDialer returns a Dialer for the given options.
func NewTCPDialer(opts TCPDialerOptions) *TCPDialer {
	if opts.Timeout == 0 {
		opts.Timeout = handshakeTimeout
	}
	comp, err := NewCompressor(opts.Compression, opts.CompStats)
	return &TCPDialer{opts: opts, comp: comp, err: err}
}

// Dial connects, optionally upgrades to TLS and performs the handshake.
func (d *TCPDialer) Dial(ctx context.Context) (Session, error) {
	if d.err != nil {
		return nil, d.err
	}
	nd := net.Dialer{Timeout: d.opts.Timeout, KeepAliveConfig: keepAliveConfig()}
	raw, err := nd.DialContext(ctx, "tcp", d.opts.Addr)
	if err != nil {
		return nil, err
	}
	tuneTCP(raw)

	conn := raw
	if d.opts.TLS != nil {
		conn = tls.Client(raw, d.opts.TLS)
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := clientHandshake(conn, Hello{Token: d.opts.Token, Channels: d.opts.Channels, Compression: d.comp.Algo()}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return newConnSession(conn, 0, d.comp), nil
}
