package tunnel

import (
	"bufio"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// HeartbeatChannel is the reserved channel ID of keepalive frames. Configured
// channels are never 0, so receivers drop these frames silently.
const HeartbeatChannel uint16 = 0

// livenessTimeout is how long a transport without its own keepalive (KCP)
// waits for any frame before declaring the peer dead. QUIC uses it as its
// idle timeout. Heartbeats go out at a third of it.
const livenessTimeout = 30 * time.Second

// connSession implements Session over a reliable byte stream net.Conn: plain
// TCP, TLS or a KCP session. When liveness is positive both ends send
// heartbeat frames and fail Receive if no frame arrives within liveness.
type connSession struct {
	conn     net.Conn
	r        *bufio.Reader
	liveness time.Duration

	wmu  sync.Mutex
	wbuf []byte

	watchdog *time.Timer // closes conn after liveness without a frame
	expired  atomic.Bool

	done chan struct{}
	once sync.Once
}

// errPeerSilent is returned by Receive when the liveness timeout expires.
var errPeerSilent = errors.New("tunnel: no frame from peer within the liveness timeout")

func newConnSession(conn net.Conn, liveness time.Duration) *connSession {
	s := &connSession{
		conn:     conn,
		r:        bufio.NewReaderSize(conn, readBufferSize),
		liveness: liveness,
		done:     make(chan struct{}),
	}
	if liveness > 0 {
		// A watchdog instead of read deadlines: it does not depend on the
		// transport's deadline handling, which kcp-go can miss under load.
		s.watchdog = time.AfterFunc(liveness, func() {
			s.expired.Store(true)
			_ = s.shutdown() // not Close: s.watchdog may not be assigned yet
		})
		go s.heartbeat()
	}
	return s
}

func (s *connSession) heartbeat() {
	t := time.NewTicker(s.liveness / 3)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if err := s.Send(HeartbeatChannel, nil); err != nil {
				_ = s.Close()
				return
			}
		}
	}
}

func (s *connSession) Send(channel uint16, payload []byte) error {
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

func (s *connSession) Receive() (uint16, []byte, error) {
	for {
		ch, p, err := ReadFrame(s.r)
		if err != nil {
			if s.expired.Load() {
				err = errPeerSilent
			}
			return 0, nil, err
		}
		if s.watchdog != nil {
			s.watchdog.Reset(s.liveness)
		}
		if ch != HeartbeatChannel {
			return ch, p, nil
		}
	}
}

func (s *connSession) RemoteAddr() string { return s.conn.RemoteAddr().String() }

func (s *connSession) Close() error {
	if s.watchdog != nil {
		s.watchdog.Stop()
	}
	return s.shutdown()
}

func (s *connSession) shutdown() error {
	s.once.Do(func() { close(s.done) })
	return s.conn.Close()
}
