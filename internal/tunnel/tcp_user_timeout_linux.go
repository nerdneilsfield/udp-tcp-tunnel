//go:build linux

package tunnel

import (
	"net"
	"syscall"
	"time"
)

// tcpUserTimeoutOpt is linux/tcp.h TCP_USER_TIMEOUT.
const tcpUserTimeoutOpt = 0x12

// setTCPUserTimeout caps how long transmitted data may stay unacknowledged
// before the kernel aborts the connection (RFC 5482). Without it, a peer that
// vanishes while we still have room in the send buffer can take tcp_retries2
// (~8–15 minutes) to fail.
func setTCPUserTimeout(conn *net.TCPConn, d time.Duration) {
	if d <= 0 {
		return
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	ms := int(d / time.Millisecond)
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeoutOpt, ms)
	})
}
