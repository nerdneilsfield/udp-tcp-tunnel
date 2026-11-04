package capture

import (
	"context"
	"net"
	"time"
)

type Packet struct {
	Source          *net.UDPAddr
	DestinationPort int
	Payload         []byte
}
type Capture interface {
	Run(context.Context, func(Packet)) error
	Close() error
}
type Socket struct{ Conn *net.UDPConn }

func NewSocket(addr string) (*Socket, error) {
	a, e := net.ResolveUDPAddr("udp", addr)
	if e != nil {
		return nil, e
	}
	c, e := net.ListenUDP("udp", a)
	return &Socket{c}, e
}
func (s *Socket) Run(ctx context.Context, fn func(Packet)) error {
	b := make([]byte, 65535)
	for {
		_ = s.Conn.SetReadDeadline(time.Now().Add(time.Second))
		n, a, e := s.Conn.ReadFromUDP(b)
		if e != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}
		p := append([]byte(nil), b[:n]...)
		fn(Packet{a, s.Conn.LocalAddr().(*net.UDPAddr).Port, p})
	}
}
func (s *Socket) Close() error { return s.Conn.Close() }
