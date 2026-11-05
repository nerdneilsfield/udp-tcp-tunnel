package tunnel

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
)

const (
	kcpSocketBuffer = 4 << 20
	kcpKeyIter      = 100_000
	kcpKeyLen       = 32 // AES-256
)

// kcpKeySalt is fixed so both ends derive the same key from the shared
// string. It only separates this program's derivation from other uses of the
// same passphrase.
const kcpKeySalt = "udp-tcp-tunnel kcp v1"

// kcpBlock derives the AES-256 block crypt from the configured key, or
// returns nil (no encryption) when the key is empty.
func kcpBlock(key string) (kcp.BlockCrypt, error) {
	if key == "" {
		return nil, nil
	}
	dk, err := pbkdf2.Key(sha256.New, key, []byte(kcpKeySalt), kcpKeyIter, kcpKeyLen)
	if err != nil {
		return nil, fmt.Errorf("derive kcp key: %w", err)
	}
	return kcp.NewAESBlockCrypt(dk)
}

// tuneKCP applies the (resolved) tuning to one session. Stream mode makes
// the session a plain byte stream, so the TCP framing runs on it unchanged.
func tuneKCP(s *kcp.UDPSession, k config.KCP) {
	k = k.Resolved()
	s.SetStreamMode(true) //nolint:staticcheck // deprecated, but the byte-stream framing needs it
	s.SetWriteDelay(false)
	s.SetACKNoDelay(true)
	s.SetNoDelay(boolInt(*k.NoDelay), int(k.Interval/time.Millisecond), k.Resend, boolInt(*k.NoCongestion))
	s.SetWindowSize(k.SendWindow, k.RecvWindow)
	s.SetMtu(k.MTU)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// KCPServerOptions configures ListenKCP.
type KCPServerOptions struct {
	Token    string
	Channels []uint16
	KCP      config.KCP
	Log      *zap.Logger
	// Liveness overrides the 30s dead-peer timeout, for tests.
	Liveness time.Duration

	Compression config.Compression
	CompStats   *stats.CompressionCounters // optional
}

// KCPListener is the server side of the KCP transport.
type KCPListener struct {
	*baseListener
	ln    *kcp.Listener
	opts  KCPServerOptions
	known map[uint16]struct{}
	comp  *Compressor
}

// ListenKCP starts listening on a UDP port and handshaking in the background.
func ListenKCP(addr string, opts KCPServerOptions) (*KCPListener, error) {
	block, err := kcpBlock(opts.KCP.Key)
	if err != nil {
		return nil, err
	}
	comp, err := NewCompressor(opts.Compression, opts.CompStats)
	if err != nil {
		return nil, err
	}
	k := opts.KCP.Resolved()
	ln, err := kcp.ListenWithOptions(addr, block, k.DataShards, k.ParityShards)
	if err != nil {
		return nil, err
	}
	_ = ln.SetReadBuffer(kcpSocketBuffer)
	_ = ln.SetWriteBuffer(kcpSocketBuffer)
	if opts.Log == nil {
		opts.Log = zap.NewNop()
	}
	if opts.Liveness == 0 {
		opts.Liveness = livenessTimeout
	}
	l := &KCPListener{
		baseListener: newBaseListener(ln.Addr().String(), ln.Close),
		ln:           ln,
		opts:         opts,
		known:        knownChannels(opts.Channels),
		comp:         comp,
	}
	go l.acceptLoop()
	return l, nil
}

func (l *KCPListener) acceptLoop() {
	for {
		sess, err := l.ln.AcceptKCP()
		if err != nil {
			select {
			case <-l.done:
				return
			default:
			}
			l.opts.Log.Warn("accept failed", zap.Error(err))
			select {
			case <-time.After(100 * time.Millisecond):
			case <-l.done:
				return
			}
			continue
		}
		go l.handshake(sess)
	}
}

func (l *KCPListener) handshake(sess *kcp.UDPSession) {
	tuneKCP(sess, l.opts.KCP)
	log := l.opts.Log.With(zap.String("remote", sess.RemoteAddr().String()))

	_ = sess.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := serverHandshake(sess, l.opts.Token, l.known, l.comp.Algo(), log); err != nil {
		// Closing a KCP session drops unsent data, so keep it open until the
		// reply has had time to be delivered and acknowledged.
		_ = sess.SetReadDeadline(time.Now().Add(rejectLinger))
		_, _ = io.Copy(io.Discard, sess)
		_ = sess.Close()
		return
	}
	_ = sess.SetDeadline(time.Time{})
	l.deliver(newConnSession(sess, l.opts.Liveness, l.comp))
}

// KCPDialerOptions configures a KCP dialer.
type KCPDialerOptions struct {
	Addr     string
	Token    string
	Channels []uint16
	KCP      config.KCP
	// Liveness overrides the 30s dead-peer timeout, for tests.
	Liveness time.Duration

	Compression config.Compression
	CompStats   *stats.CompressionCounters // optional
}

// KCPDialer is the client side of the KCP transport.
type KCPDialer struct {
	opts KCPDialerOptions
	comp *Compressor
	err  error // compression setup error, reported by Dial
}

// NewKCPDialer returns a Dialer for the given options.
func NewKCPDialer(opts KCPDialerOptions) *KCPDialer {
	if opts.Liveness == 0 {
		opts.Liveness = livenessTimeout
	}
	comp, err := NewCompressor(opts.Compression, opts.CompStats)
	return &KCPDialer{opts: opts, comp: comp, err: err}
}

// Dial opens a KCP session and performs the handshake. KCP has no connect
// step, so an absent server shows up as a handshake timeout.
func (d *KCPDialer) Dial(ctx context.Context) (Session, error) {
	if d.err != nil {
		return nil, d.err
	}
	block, err := kcpBlock(d.opts.KCP.Key)
	if err != nil {
		return nil, err
	}
	k := d.opts.KCP.Resolved()
	sess, err := kcp.DialWithOptions(d.opts.Addr, block, k.DataShards, k.ParityShards)
	if err != nil {
		return nil, err
	}
	_ = sess.SetReadBuffer(kcpSocketBuffer)
	_ = sess.SetWriteBuffer(kcpSocketBuffer)
	tuneKCP(sess, d.opts.KCP)

	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	_ = sess.SetDeadline(time.Now().Add(handshakeTimeout))
	err = clientHandshake(sess, Hello{Token: d.opts.Token, Channels: d.opts.Channels, Compression: d.comp.Algo()})
	if !stop() || err != nil {
		_ = sess.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, net.ErrClosed) || err == nil {
			err = net.ErrClosed
		}
		return nil, err
	}
	_ = sess.SetDeadline(time.Time{})
	return newConnSession(sess, d.opts.Liveness, d.comp), nil
}
