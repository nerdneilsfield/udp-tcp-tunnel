package tunnel

import (
	"context"
	"net"
	"sync"
)

// baseListener holds what every Listener shares: a queue of authenticated
// sessions, an address and an idempotent Close.
type baseListener struct {
	addr    string
	closeFn func() error // closes the underlying listener

	ready chan Session
	done  chan struct{}
	once  sync.Once
}

func newBaseListener(addr string, closeFn func() error) *baseListener {
	return &baseListener{
		addr:    addr,
		closeFn: closeFn,
		ready:   make(chan Session),
		done:    make(chan struct{}),
	}
}

// Addr returns the bound address, useful when listening on port 0.
func (l *baseListener) Addr() string { return l.addr }

// Accept returns the next client that passed the handshake.
func (l *baseListener) Accept(ctx context.Context) (Session, error) {
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
func (l *baseListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.closeFn()
	})
	return err
}

// deliver hands an authenticated session to Accept, or closes it when the
// listener has been closed first.
func (l *baseListener) deliver(s Session) {
	select {
	case l.ready <- s:
	case <-l.done:
		_ = s.Close()
	}
}
