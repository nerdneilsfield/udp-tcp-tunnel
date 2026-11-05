package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"go.uber.org/zap"
)

// QUICALPN is the ALPN protocol ID both ends must offer.
const QUICALPN = "udp-tcp-tunnel/1"

// Application error codes carried in CONNECTION_CLOSE.
const (
	quicCodeClosed    quic.ApplicationErrorCode = 0
	quicCodeHandshake quic.ApplicationErrorCode = 1
)

// rejectLinger is how long the server keeps a rejected connection open so the
// reply on the stream reaches the client before CONNECTION_CLOSE.
const rejectLinger = 2 * time.Second

func quicConfig(idle time.Duration) *quic.Config {
	if idle == 0 {
		idle = livenessTimeout
	}
	return &quic.Config{
		EnableDatagrams:       true,
		MaxIdleTimeout:        idle,
		KeepAlivePeriod:       idle / 3,
		HandshakeIdleTimeout:  handshakeTimeout,
		MaxIncomingStreams:    1,
		MaxIncomingUniStreams: -1,
	}
}

func quicTLS(c *tls.Config) (*tls.Config, error) {
	if c == nil {
		return nil, errors.New("quic requires tls")
	}
	c = c.Clone()
	c.NextProtos = []string{QUICALPN}
	c.MinVersion = tls.VersionTLS13
	return c, nil
}

// recvItem is one datagram received from either QUIC datagrams or the stream.
type recvItem struct {
	channel uint16
	payload []byte
}

// quicSession implements Session over one QUIC connection. UDP payloads go
// in DATAGRAM frames (2-byte channel ID + payload). A payload that does not
// fit a datagram is sent as a regular frame on the handshake stream instead.
type quicSession struct {
	conn   *quic.Conn
	stream *quic.Stream

	wmu  sync.Mutex
	wbuf []byte

	in   chan recvItem
	done chan struct{}
	once sync.Once
	err  error
	ctx  context.Context
	stop context.CancelFunc
}

func newQUICSession(conn *quic.Conn, stream *quic.Stream) *quicSession {
	ctx, stop := context.WithCancel(context.Background())
	s := &quicSession{
		conn:   conn,
		stream: stream,
		in:     make(chan recvItem),
		done:   make(chan struct{}),
		ctx:    ctx,
		stop:   stop,
	}
	go s.streamLoop()
	go s.datagramLoop()
	return s
}

func (s *quicSession) fail(err error) {
	s.once.Do(func() {
		s.err = err
		close(s.done)
		s.stop()
		_ = s.conn.CloseWithError(quicCodeClosed, "closed")
	})
}

func (s *quicSession) push(it recvItem) bool {
	select {
	case s.in <- it:
		return true
	case <-s.done:
		return false
	}
}

func (s *quicSession) streamLoop() {
	r := bufio.NewReaderSize(s.stream, readBufferSize)
	for {
		ch, p, err := ReadFrame(r)
		if err != nil {
			s.fail(err)
			return
		}
		if ch == HeartbeatChannel {
			continue
		}
		if !s.push(recvItem{ch, p}) {
			return
		}
	}
}

func (s *quicSession) datagramLoop() {
	for {
		d, err := s.conn.ReceiveDatagram(s.ctx)
		if err != nil {
			s.fail(err)
			return
		}
		if len(d) < 2 {
			continue
		}
		if !s.push(recvItem{uint16(d[0])<<8 | uint16(d[1]), d[2:]}) {
			return
		}
	}
}

func (s *quicSession) Send(channel uint16, payload []byte) error {
	if len(payload) > MaxPayload {
		return ErrPayloadTooLarge
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()

	s.wbuf = append(s.wbuf[:0], byte(channel>>8), byte(channel))
	s.wbuf = append(s.wbuf, payload...)
	err := s.conn.SendDatagram(s.wbuf)
	var tooLarge *quic.DatagramTooLargeError
	if !errors.As(err, &tooLarge) {
		return err
	}
	// Larger than the path's datagram limit: deliver reliably on the stream.
	buf, err := AppendFrame(s.wbuf[:0], channel, payload)
	if err != nil {
		return err
	}
	s.wbuf = buf
	_ = s.stream.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = s.stream.Write(buf)
	return err
}

func (s *quicSession) Receive() (uint16, []byte, error) {
	select {
	case it := <-s.in:
		return it.channel, it.payload, nil
	case <-s.done:
		return 0, nil, s.err
	}
}

func (s *quicSession) RemoteAddr() string { return s.conn.RemoteAddr().String() }

func (s *quicSession) Close() error {
	s.fail(net.ErrClosed)
	return nil
}

// QUICServerOptions configures ListenQUIC.
type QUICServerOptions struct {
	Token    string
	Channels []uint16
	TLS      *tls.Config // required
	Log      *zap.Logger
	// IdleTimeout overrides the 30s dead-peer timeout, for tests.
	IdleTimeout time.Duration
}

// QUICListener is the server side of the QUIC transport.
type QUICListener struct {
	*baseListener
	ln    *quic.Listener
	opts  QUICServerOptions
	known map[uint16]struct{}
	ctx   context.Context
}

// ListenQUIC starts listening on a UDP port and handshaking in the background.
func ListenQUIC(addr string, opts QUICServerOptions) (*QUICListener, error) {
	tlsConf, err := quicTLS(opts.TLS)
	if err != nil {
		return nil, err
	}
	ln, err := quic.ListenAddr(addr, tlsConf, quicConfig(opts.IdleTimeout))
	if err != nil {
		return nil, err
	}
	if opts.Log == nil {
		opts.Log = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &QUICListener{
		baseListener: newBaseListener(ln.Addr().String(), func() error {
			cancel()
			return ln.Close()
		}),
		ln:    ln,
		opts:  opts,
		known: knownChannels(opts.Channels),
		ctx:   ctx,
	}
	go l.acceptLoop()
	return l, nil
}

func (l *QUICListener) acceptLoop() {
	for {
		conn, err := l.ln.Accept(l.ctx)
		if err != nil {
			if l.ctx.Err() != nil || errors.Is(err, quic.ErrServerClosed) {
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

func (l *QUICListener) handshake(conn *quic.Conn) {
	log := l.opts.Log.With(zap.String("remote", conn.RemoteAddr().String()))
	ctx, cancel := context.WithTimeout(l.ctx, handshakeTimeout)
	defer cancel()

	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		log.Warn("handshake failed", zap.Error(err))
		_ = conn.CloseWithError(quicCodeHandshake, "handshake failed")
		return
	}
	_ = stream.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := serverHandshake(stream, l.opts.Token, l.known, log); err != nil {
		_ = stream.Close()
		select { // let the reply reach the client, which closes first
		case <-conn.Context().Done():
		case <-time.After(rejectLinger):
		case <-l.ctx.Done():
		}
		_ = conn.CloseWithError(quicCodeHandshake, "handshake rejected")
		return
	}
	_ = stream.SetDeadline(time.Time{})
	l.deliver(newQUICSession(conn, stream))
}

// QUICDialerOptions configures a QUIC dialer.
type QUICDialerOptions struct {
	Addr     string
	Token    string
	Channels []uint16
	TLS      *tls.Config // required
	// IdleTimeout overrides the 30s dead-peer timeout, for tests.
	IdleTimeout time.Duration
}

// QUICDialer is the client side of the QUIC transport.
type QUICDialer struct{ opts QUICDialerOptions }

// NewQUICDialer returns a Dialer for the given options.
func NewQUICDialer(opts QUICDialerOptions) *QUICDialer { return &QUICDialer{opts: opts} }

// Dial connects, opens the control stream and performs the handshake.
func (d *QUICDialer) Dial(ctx context.Context) (Session, error) {
	tlsConf, err := quicTLS(d.opts.TLS)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	conn, err := quic.DialAddr(hctx, d.opts.Addr, tlsConf, quicConfig(d.opts.IdleTimeout))
	if err != nil {
		return nil, err
	}
	stream, err := conn.OpenStreamSync(hctx)
	if err == nil {
		_ = stream.SetDeadline(time.Now().Add(handshakeTimeout))
		err = clientHandshake(stream, Hello{Token: d.opts.Token, Channels: d.opts.Channels})
	}
	if err != nil {
		_ = conn.CloseWithError(quicCodeHandshake, "handshake failed")
		return nil, err
	}
	_ = stream.SetDeadline(time.Time{})
	return newQUICSession(conn, stream), nil
}
