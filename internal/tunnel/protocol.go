// Package tunnel implements the wire protocol, the transport abstraction,
// the TCP/TLS transport and the tunnel client.
//
// Handshake (client -> server):
//
//	magic "UTT1" | version u8 | token_len u8 | token | n_channels u16 | n * channel_id u16
//
// Handshake reply (server -> client):
//
//	magic "UTT1" | version u8 | status u8
//
// After a successful handshake the server sends frames:
//
//	channel_id u16 BE | payload_length u32 BE | payload
//
// Compression is negotiated with protocol version 2: the hello and the reply
// each carry one more byte, the compression algorithm (see Algo). A client
// that does not compress sends a version 1 hello, which is the original
// format, and a version 1 server accepts it unchanged.
//
// Bit 31 of payload_length marks a compressed payload; the low bits are the
// number of payload bytes on the wire.
package tunnel

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Protocol constants.
const (
	Magic = "UTT1"
	// Version is the original handshake; VersionCompression adds the
	// compression algorithm byte to the hello and the reply.
	Version            = 1
	VersionCompression = 2
	MaxPayload         = 65535

	// FlagCompressed is set in payload_length for a compressed payload.
	FlagCompressed uint32 = 1 << 31

	// FrameHeaderLen is channel_id (2) + payload_length (4).
	FrameHeaderLen = 6
)

// Algo identifies a payload compression algorithm on the wire.
type Algo uint8

// Compression algorithms.
const (
	AlgoNone Algo = iota
	AlgoZstd
	AlgoS2
)

func (a Algo) String() string {
	switch a {
	case AlgoNone:
		return "none"
	case AlgoZstd:
		return "zstd"
	case AlgoS2:
		return "s2"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(a))
	}
}

// Status is the server's answer to a handshake.
type Status uint8

// Handshake statuses.
const (
	StatusOK Status = iota
	StatusBadVersion
	StatusAuthFailed
	StatusUnknownChannel
	StatusCompressionMismatch
)

// Protocol errors.
var (
	ErrBadMagic        = errors.New("tunnel: bad handshake magic")
	ErrBadVersion      = errors.New("tunnel: unsupported protocol version")
	ErrAuthFailed      = errors.New("tunnel: authentication failed")
	ErrUnknownChannel  = errors.New("tunnel: server does not know a requested channel")
	ErrPayloadTooLarge = errors.New("tunnel: payload exceeds maximum frame size")
	ErrTokenTooLong    = errors.New("tunnel: token longer than 255 bytes")

	// ErrCompressionMismatch means the two ends are configured with
	// different compression algorithms.
	ErrCompressionMismatch = errors.New("tunnel: compression mismatch between client and server")
	// ErrCompressionUnsupported means the server only speaks the original
	// protocol and cannot negotiate compression.
	ErrCompressionUnsupported = errors.New("tunnel: server does not support compression (protocol version 1)")
)

// CompressionMismatchError reports the algorithms of both ends when the
// server rejected the hello for a compression mismatch. Server is unknown
// (reported as AlgoNone with ServerKnown false) when the server replied with a
// version 1 reply.
type CompressionMismatchError struct {
	Client, Server Algo
	ServerKnown    bool
}

func (e *CompressionMismatchError) Error() string {
	if !e.ServerKnown {
		return fmt.Sprintf("%v: client uses %s, server requires another setting", ErrCompressionMismatch, e.Client)
	}
	return fmt.Sprintf("%v: client uses %s, server uses %s", ErrCompressionMismatch, e.Client, e.Server)
}

func (e *CompressionMismatchError) Unwrap() error { return ErrCompressionMismatch }

// Hello is the client's opening message.
type Hello struct {
	Token    string
	Channels []uint16
	// Compression is the algorithm the client will receive. Anything other
	// than AlgoNone makes WriteHello emit a version 2 hello.
	Compression Algo
	// Version is the hello version as parsed by ReadHello.
	Version uint8
}

// Check verifies a client hello against the server's token and channel set.
func (h Hello) Check(token string, known map[uint16]struct{}) Status {
	if subtle.ConstantTimeCompare([]byte(h.Token), []byte(token)) != 1 {
		return StatusAuthFailed
	}
	for _, id := range h.Channels {
		if _, ok := known[id]; !ok {
			return StatusUnknownChannel
		}
	}
	return StatusOK
}

// WriteHello sends the client handshake.
func WriteHello(w io.Writer, h Hello) error {
	if len(h.Token) > 255 {
		return ErrTokenTooLong
	}
	if len(h.Channels) > 0xffff {
		return errors.New("tunnel: too many channels")
	}
	buf := make([]byte, 0, len(Magic)+2+len(h.Token)+2+2*len(h.Channels))
	buf = append(buf, Magic...)
	version := byte(Version)
	if h.Compression != AlgoNone {
		version = VersionCompression
	}
	buf = append(buf, version, byte(len(h.Token)))
	buf = append(buf, h.Token...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(h.Channels)))
	for _, id := range h.Channels {
		buf = binary.BigEndian.AppendUint16(buf, id)
	}
	if version == VersionCompression {
		buf = append(buf, byte(h.Compression))
	}
	_, err := w.Write(buf)
	return err
}

// ReadHello parses the client handshake. A wrong version is reported as
// ErrBadVersion together with the hello parsed so far being unusable.
func ReadHello(r io.Reader) (Hello, error) {
	var head [len(Magic) + 2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return Hello{}, err
	}
	if string(head[:len(Magic)]) != Magic {
		return Hello{}, ErrBadMagic
	}
	version := head[len(Magic)]
	if version != Version && version != VersionCompression {
		return Hello{}, ErrBadVersion
	}
	token := make([]byte, head[len(Magic)+1])
	if _, err := io.ReadFull(r, token); err != nil {
		return Hello{}, err
	}
	var count [2]byte
	if _, err := io.ReadFull(r, count[:]); err != nil {
		return Hello{}, err
	}
	raw := make([]byte, 2*int(binary.BigEndian.Uint16(count[:])))
	if _, err := io.ReadFull(r, raw); err != nil {
		return Hello{}, err
	}
	h := Hello{Token: string(token), Channels: make([]uint16, len(raw)/2), Version: version}
	for i := range h.Channels {
		h.Channels[i] = binary.BigEndian.Uint16(raw[2*i:])
	}
	if version == VersionCompression {
		var algo [1]byte
		if _, err := io.ReadFull(r, algo[:]); err != nil {
			return Hello{}, err
		}
		h.Compression = Algo(algo[0])
	}
	return h, nil
}

// WriteReply sends the server's version 1 handshake answer.
func WriteReply(w io.Writer, s Status) error {
	_, err := w.Write(append([]byte(Magic), Version, byte(s)))
	return err
}

// WriteReplyAlgo sends the version 2 answer to a version 2 hello. algo is the
// server's own compression setting.
func WriteReplyAlgo(w io.Writer, s Status, algo Algo) error {
	_, err := w.Write(append([]byte(Magic), VersionCompression, byte(s), byte(algo)))
	return err
}

// ReadReply parses the server's answer and maps failures to errors. A
// version 2 answer is accepted and its algorithm byte ignored.
func ReadReply(r io.Reader) error {
	_, _, err := ReadReplyAlgo(r)
	return err
}

// ReadReplyAlgo parses the server's answer. For a version 2 answer it also
// returns the server's compression algorithm and true.
func ReadReplyAlgo(r io.Reader) (Algo, bool, error) {
	var b [len(Magic) + 2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return AlgoNone, false, err
	}
	if string(b[:len(Magic)]) != Magic {
		return AlgoNone, false, ErrBadMagic
	}
	version, status := b[len(Magic)], Status(b[len(Magic)+1])
	var algo Algo
	v2 := version == VersionCompression && (status == StatusOK || status == StatusCompressionMismatch)
	if v2 {
		var a [1]byte
		if _, err := io.ReadFull(r, a[:]); err != nil {
			return AlgoNone, false, err
		}
		algo = Algo(a[0])
	}
	switch status {
	case StatusOK:
		if version != Version && version != VersionCompression {
			return algo, v2, ErrBadVersion
		}
		return algo, v2, nil
	case StatusBadVersion:
		return algo, v2, ErrBadVersion
	case StatusAuthFailed:
		return algo, v2, ErrAuthFailed
	case StatusUnknownChannel:
		return algo, v2, ErrUnknownChannel
	case StatusCompressionMismatch:
		return algo, v2, ErrCompressionMismatch
	default:
		return algo, v2, fmt.Errorf("tunnel: unknown handshake status %d", b[len(Magic)+1])
	}
}

// AppendFrame appends the encoded frame to dst.
func AppendFrame(dst []byte, channel uint16, payload []byte) ([]byte, error) {
	return appendFrame(dst, channel, false, payload)
}

// appendFrame appends a frame whose payload may be marked as compressed.
func appendFrame(dst []byte, channel uint16, compressed bool, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return dst, ErrPayloadTooLarge
	}
	n := uint32(len(payload))
	if compressed {
		n |= FlagCompressed
	}
	dst = binary.BigEndian.AppendUint16(dst, channel)
	dst = binary.BigEndian.AppendUint32(dst, n)
	return append(dst, payload...), nil
}

// WriteFrame writes one frame with a single Write call.
func WriteFrame(w io.Writer, channel uint16, payload []byte) error {
	buf, err := AppendFrame(make([]byte, 0, FrameHeaderLen+len(payload)), channel, payload)
	if err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}

// ReadFrame reads one frame. The payload is freshly allocated. Frames
// longer than MaxPayload, and frames marked compressed, are rejected before
// any payload is read.
func ReadFrame(r io.Reader) (channel uint16, payload []byte, err error) {
	channel, compressed, payload, err := readFrame(r)
	if err == nil && compressed {
		return 0, nil, fmt.Errorf("%w: unexpected compressed frame", ErrPayloadTooLarge)
	}
	return channel, payload, err
}

// readFrame reads one frame and reports whether its payload is compressed.
func readFrame(r io.Reader) (channel uint16, compressed bool, payload []byte, err error) {
	var head [FrameHeaderLen]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return 0, false, nil, err
	}
	n := binary.BigEndian.Uint32(head[2:])
	compressed = n&FlagCompressed != 0
	n &^= FlagCompressed
	if n > MaxPayload {
		return 0, false, nil, fmt.Errorf("%w: %d bytes", ErrPayloadTooLarge, n)
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, false, nil, err
	}
	return binary.BigEndian.Uint16(head[:2]), compressed, payload, nil
}
