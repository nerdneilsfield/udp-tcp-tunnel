package tunnel

import (
	"errors"
	"io"

	"go.uber.org/zap"
)

// serverHandshake reads the client hello from rw, checks it and writes the
// reply. It returns nil only when the client was accepted. Every transport
// runs it over its byte stream (a TCP/TLS connection, a KCP session or the
// first QUIC stream), so authentication is identical everywhere.
func serverHandshake(rw io.ReadWriter, token string, known map[uint16]struct{}, log *zap.Logger) error {
	hello, err := ReadHello(rw)
	if err != nil {
		log.Warn("handshake failed", zap.Error(err))
		if errors.Is(err, ErrBadVersion) {
			_ = WriteReply(rw, StatusBadVersion)
		}
		return err
	}
	status := hello.Check(token, known)
	werr := WriteReply(rw, status)
	if werr != nil || status != StatusOK {
		log.Warn("handshake rejected", zap.Uint8("status", uint8(status)), zap.Error(werr))
		if werr == nil {
			werr = errors.New("tunnel: handshake rejected")
		}
		return werr
	}
	return nil
}

// clientHandshake sends the hello over rw and maps the reply to an error.
func clientHandshake(rw io.ReadWriter, h Hello) error {
	if err := WriteHello(rw, h); err != nil {
		return err
	}
	return ReadReply(rw)
}
