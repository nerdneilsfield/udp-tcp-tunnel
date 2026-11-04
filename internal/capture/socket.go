package capture

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
)

const (
	maxDatagram    = 65535
	socketRcvBuf   = 4 << 20
	socketModeName = "socket"
)

// Socket captures datagrams by binding ordinary UDP sockets, one per
// destination port. It must own the ports, so it cannot coexist with
// another receiver on the same address.
type Socket struct {
	conns     []*net.UDPConn
	closeOnce sync.Once
	closeErr  error
}

// NewSocket binds bind:port for every port.
func NewSocket(bind string, ports []uint16) (*Socket, error) {
	ip, err := netip.ParseAddr(bind)
	if err != nil {
		return nil, fmt.Errorf("%s capture: invalid bind address %q: %w", socketModeName, bind, err)
	}
	s := &Socket{}
	for _, port := range ports {
		conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, port)))
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("%s capture: listen udp %s:%d: %w", socketModeName, bind, port, err)
		}
		_ = conn.SetReadBuffer(socketRcvBuf) // best effort; the OS may clamp it
		s.conns = append(s.conns, conn)
	}
	return s, nil
}

// Run reads every socket until ctx is canceled.
func (s *Socket) Run(ctx context.Context, handle func(Packet)) error {
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()

	var (
		wg       sync.WaitGroup
		firstErr atomic.Pointer[error]
	)
	for _, conn := range s.conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := readLoop(ctx, conn, handle); err != nil {
				firstErr.CompareAndSwap(nil, &err)
				_ = s.Close()
			}
		}()
	}
	wg.Wait()
	if err := firstErr.Load(); err != nil {
		return *err
	}
	return nil
}

func readLoop(ctx context.Context, conn *net.UDPConn, handle func(Packet)) error {
	localPort := conn.LocalAddr().(*net.UDPAddr).AddrPort().Port()
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("%s capture: read: %w", socketModeName, err)
		}
		handle(Packet{
			Src:     from.Addr().Unmap(),
			SrcPort: from.Port(),
			DstPort: localPort,
			Payload: append([]byte(nil), buf[:n]...),
		})
	}
}

// Stats returns zero values: a plain socket reports no kernel drops.
func (s *Socket) Stats() Stats { return Stats{} }

// Close closes every socket.
func (s *Socket) Close() error {
	s.closeOnce.Do(func() {
		for _, conn := range s.conns {
			if err := conn.Close(); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}
