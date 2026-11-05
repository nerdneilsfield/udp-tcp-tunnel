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
func serverHandshake(rw io.ReadWriter, token string, known map[uint16]struct{}, algo Algo, log *zap.Logger) error {
	hello, err := ReadHello(rw)
	if err != nil {
		log.Warn("handshake failed", zap.Error(err))
		if errors.Is(err, ErrBadVersion) {
			_ = WriteReply(rw, StatusBadVersion)
		}
		return err
	}
	status := hello.Check(token, known)
	if status == StatusOK && hello.Compression != algo {
		status = StatusCompressionMismatch
	}
	var werr error
	if hello.Version == VersionCompression {
		werr = WriteReplyAlgo(rw, status, algo)
	} else {
		werr = WriteReply(rw, status)
	}
	if werr != nil || status != StatusOK {
		log.Warn("handshake rejected", zap.Uint8("status", uint8(status)),
			zap.String("client_compression", hello.Compression.String()),
			zap.String("server_compression", algo.String()),
			zap.Error(werr))
		if werr == nil {
			werr = errors.New("tunnel: handshake rejected")
		}
		return werr
	}
	return nil
}

// clientHandshake sends the hello over rw and maps the reply to an error.
// A client that asks for compression needs a server that agrees on the
// algorithm; every mismatch is reported as a *CompressionMismatchError or
// ErrCompressionUnsupported.
func clientHandshake(rw io.ReadWriter, h Hello) error {
	if err := WriteHello(rw, h); err != nil {
		return err
	}
	serverAlgo, v2, err := ReadReplyAlgo(rw)
	switch {
	case errors.Is(err, ErrCompressionMismatch):
		return &CompressionMismatchError{Client: h.Compression, Server: serverAlgo, ServerKnown: v2}
	case err != nil:
		if h.Compression != AlgoNone && errors.Is(err, ErrBadVersion) {
			return ErrCompressionUnsupported
		}
		return err
	case h.Compression != AlgoNone && (!v2 || serverAlgo != h.Compression):
		return &CompressionMismatchError{Client: h.Compression, Server: serverAlgo, ServerKnown: v2}
	}
	return nil
}
