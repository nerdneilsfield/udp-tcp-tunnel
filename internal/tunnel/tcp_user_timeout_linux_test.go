//go:build linux

package tunnel

import (
	"net"
	"syscall"
	"testing"
	"time"
)

func TestTuneTCPSetsUserTimeoutAndKeepAlive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			errCh <- acceptErr
			return
		}
		defer c.Close()
		tuneTCP(c)
		errCh <- nil
		_, _ = c.Read(make([]byte, 1))
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tuneTCP(client)

	if acceptErr := <-errCh; acceptErr != nil {
		t.Fatal(acceptErr)
	}

	tc := client.(*net.TCPConn)
	raw, err := tc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var gotUserTimeout, idle, interval, count int
	var getErr error
	err = raw.Control(func(fd uintptr) {
		gotUserTimeout, getErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeoutOpt)
		if getErr != nil {
			return
		}
		idle, getErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE)
		if getErr != nil {
			return
		}
		interval, getErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL)
		if getErr != nil {
			return
		}
		count, getErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT)
	})
	if err != nil {
		t.Fatal(err)
	}
	if getErr != nil {
		t.Fatal(getErr)
	}
	if want := int(userTimeout / time.Millisecond); gotUserTimeout != want {
		t.Fatalf("TCP_USER_TIMEOUT=%d ms, want %d", gotUserTimeout, want)
	}
	if idle != int(keepAliveIdle/time.Second) {
		t.Fatalf("TCP_KEEPIDLE=%d, want %d", idle, keepAliveIdle/time.Second)
	}
	if interval != int(keepAliveInterval/time.Second) {
		t.Fatalf("TCP_KEEPINTVL=%d, want %d", interval, keepAliveInterval/time.Second)
	}
	if count != keepAliveCount {
		t.Fatalf("TCP_KEEPCNT=%d, want %d", count, keepAliveCount)
	}
}
