//go:build !linux

package tunnel

import (
	"net"
	"time"
)

func setTCPUserTimeout(*net.TCPConn, time.Duration) {}
