package tunnel

import "context"

// Session is an authenticated, framed, bidirectional link between the
// server and one client. Implementations: tcp.go, kcp.go and quic.go.
type Session interface {
	// Send transmits one datagram on a channel. It is safe for concurrent use.
	Send(channel uint16, payload []byte) error
	// Receive blocks for the next datagram.
	Receive() (channel uint16, payload []byte, err error)
	// RemoteAddr describes the peer for logging.
	RemoteAddr() string
	// Close terminates the session and unblocks Send and Receive.
	Close() error
}

// Listener accepts authenticated sessions on the server side.
type Listener interface {
	// Accept blocks until a client has completed the handshake.
	Accept(ctx context.Context) (Session, error)
	// Addr is the address being listened on.
	Addr() string
	Close() error
}

// Dialer establishes an authenticated session on the client side.
type Dialer interface {
	Dial(ctx context.Context) (Session, error)
}
