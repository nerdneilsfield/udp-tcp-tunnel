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
	Magic      = "UTT1"
	Version    = 1
	MaxPayload = 65535

	// FrameHeaderLen is channel_id (2) + payload_length (4).
	FrameHeaderLen = 6
)

// Status is the server's answer to a handshake.
type Status uint8

// Handshake statuses.
const (
	StatusOK Status = iota
	StatusBadVersion
	StatusAuthFailed
	StatusUnknownChannel
)

// Protocol errors.
var (
	ErrBadMagic        = errors.New("tunnel: bad handshake magic")
	ErrBadVersion      = errors.New("tunnel: unsupported protocol version")
	ErrAuthFailed      = errors.New("tunnel: authentication failed")
	ErrUnknownChannel  = errors.New("tunnel: server does not know a requested channel")
	ErrPayloadTooLarge = errors.New("tunnel: payload exceeds maximum frame size")
	ErrTokenTooLong    = errors.New("tunnel: token longer than 255 bytes")
)

// Hello is the client's opening message.
type Hello struct {
	Token    string
	Channels []uint16
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
	buf = append(buf, Version, byte(len(h.Token)))
	buf = append(buf, h.Token...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(h.Channels)))
	for _, id := range h.Channels {
		buf = binary.BigEndian.AppendUint16(buf, id)
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
	if head[len(Magic)] != Version {
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
	h := Hello{Token: string(token), Channels: make([]uint16, len(raw)/2)}
	for i := range h.Channels {
		h.Channels[i] = binary.BigEndian.Uint16(raw[2*i:])
	}
	return h, nil
}

// WriteReply sends the server's handshake answer.
func WriteReply(w io.Writer, s Status) error {
	_, err := w.Write(append([]byte(Magic), Version, byte(s)))
	return err
}

// ReadReply parses the server's answer and maps failures to errors.
func ReadReply(r io.Reader) error {
	var b [len(Magic) + 2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return err
	}
	if string(b[:len(Magic)]) != Magic {
		return ErrBadMagic
	}
	switch Status(b[len(Magic)+1]) {
	case StatusOK:
		if b[len(Magic)] != Version {
			return ErrBadVersion
		}
		return nil
	case StatusBadVersion:
		return ErrBadVersion
	case StatusAuthFailed:
		return ErrAuthFailed
	case StatusUnknownChannel:
		return ErrUnknownChannel
	default:
		return fmt.Errorf("tunnel: unknown handshake status %d", b[len(Magic)+1])
	}
}

// AppendFrame appends the encoded frame to dst.
func AppendFrame(dst []byte, channel uint16, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return dst, ErrPayloadTooLarge
	}
	dst = binary.BigEndian.AppendUint16(dst, channel)
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
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
// longer than MaxPayload are rejected before any payload is read.
func ReadFrame(r io.Reader) (channel uint16, payload []byte, err error) {
	var head [FrameHeaderLen]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[2:])
	if n > MaxPayload {
		return 0, nil, fmt.Errorf("%w: %d bytes", ErrPayloadTooLarge, n)
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return binary.BigEndian.Uint16(head[:2]), payload, nil
}
