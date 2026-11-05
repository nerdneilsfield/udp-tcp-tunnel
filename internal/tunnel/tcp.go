package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"
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

// tcpSession implements Session over a net.Conn (plain TCP or TLS).
type tcpSession struct {
	conn net.Conn
	r    *bufio.Reader

	wmu  sync.Mutex
	wbuf []byte
}

func newTCPSession(conn net.Conn) *tcpSession {
	return &tcpSession{conn: conn, r: bufio.NewReaderSize(conn, readBufferSize)}
}

func (s *tcpSession) Send(channel uint16, payload []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	buf, err := AppendFrame(s.wbuf[:0], channel, payload)
	if err != nil {
		return err
	}
	s.wbuf = buf
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = s.conn.Write(buf)
	return err
}

func (s *tcpSession) Receive() (uint16, []byte, error) { return ReadFrame(s.r) }

func (s *tcpSession) RemoteAddr() string { return s.conn.RemoteAddr().String() }

func (s *tcpSession) Close() error { return s.conn.Close() }

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
}

// TCPListener is the server side of the TCP transport.
type TCPListener struct {
	ln    net.Listener
	opts  TCPServerOptions
	known map[uint16]struct{}

	ready chan Session
	done  chan struct{}
	once  sync.Once
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
	l := &TCPListener{
		ln:    ln,
		opts:  opts,
		known: make(map[uint16]struct{}, len(opts.Channels)),
		ready: make(chan Session),
		done:  make(chan struct{}),
	}
	for _, id := range opts.Channels {
		l.known[id] = struct{}{}
	}
	go l.acceptLoop()
	return l, nil
}

// Addr returns the bound address, useful when listening on port 0.
func (l *TCPListener) Addr() string { return l.ln.Addr().String() }

// Accept returns the next client that passed the handshake.
func (l *TCPListener) Accept(ctx context.Context) (Session, error) {
	select {
	case s := <-l.ready:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close stops accepting connections.
func (l *TCPListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.ln.Close()
	})
	return err
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
	hello, err := ReadHello(conn)
	if err != nil {
		log.Warn("handshake failed", zap.Error(err))
		if errors.Is(err, ErrBadVersion) {
			_ = WriteReply(conn, StatusBadVersion)
		}
		_ = conn.Close()
		return
	}
	status := hello.Check(l.opts.Token, l.known)
	if err := WriteReply(conn, status); err != nil || status != StatusOK {
		log.Warn("handshake rejected", zap.Uint8("status", uint8(status)), zap.Error(err))
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	select {
	case l.ready <- newTCPSession(conn):
	case <-l.done:
		_ = conn.Close()
	}
}

// TCPDialerOptions configures a TCP dialer.
type TCPDialerOptions struct {
	Addr     string
	Token    string
	Channels []uint16
	TLS      *tls.Config // nil for plain TCP
	Timeout  time.Duration
}

// TCPDialer is the client side of the TCP transport.
type TCPDialer struct{ opts TCPDialerOptions }

// NewTCPDialer returns a Dialer for the given options.
func NewTCPDialer(opts TCPDialerOptions) *TCPDialer {
	if opts.Timeout == 0 {
		opts.Timeout = handshakeTimeout
	}
	return &TCPDialer{opts: opts}
}

// Dial connects, optionally upgrades to TLS and performs the handshake.
func (d *TCPDialer) Dial(ctx context.Context) (Session, error) {
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
	if err := d.handshake(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return newTCPSession(conn), nil
}

func (d *TCPDialer) handshake(conn net.Conn) error {
	if err := WriteHello(conn, Hello{Token: d.opts.Token, Channels: d.opts.Channels}); err != nil {
		return err
	}
	return ReadReply(conn)
}
