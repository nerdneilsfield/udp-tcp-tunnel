package tunnel

import (
	"encoding/binary"
	"fmt"
	"io"
)

const Magic = "UTT1"
const Version uint8 = 1
const MaxPayload = 65535

func WriteHandshake(w io.Writer, token string) error {
	b := []byte(token)
	if len(b) > 255 {
		return fmt.Errorf("token too long")
	}
	h := append([]byte(Magic), Version, byte(len(b)))
	h = append(h, b...)
	_, e := w.Write(h)
	return e
}
func ReadHandshake(r io.Reader, token string) error {
	h := make([]byte, 6)
	if _, e := io.ReadFull(r, h); e != nil {
		return e
	}
	if string(h[:4]) != Magic || h[4] != Version {
		return fmt.Errorf("invalid handshake")
	}
	b := make([]byte, h[5])
	if _, e := io.ReadFull(r, b); e != nil {
		return e
	}
	if string(b) != token {
		return fmt.Errorf("authentication failed")
	}
	return nil
}
func WriteFrame(w io.Writer, id uint16, p []byte) error {
	if len(p) > MaxPayload {
		return fmt.Errorf("payload too large")
	}
	h := make([]byte, 6)
	binary.BigEndian.PutUint16(h, id)
	binary.BigEndian.PutUint32(h[2:], uint32(len(p)))
	if _, e := w.Write(h); e != nil {
		return e
	}
	_, e := w.Write(p)
	return e
}
func ReadFrame(r io.Reader) (uint16, []byte, error) {
	h := make([]byte, 6)
	if _, e := io.ReadFull(r, h); e != nil {
		return 0, nil, e
	}
	n := binary.BigEndian.Uint32(h[2:])
	if n > MaxPayload {
		return 0, nil, fmt.Errorf("payload too large")
	}
	p := make([]byte, n)
	_, e := io.ReadFull(r, p)
	return binary.BigEndian.Uint16(h), p, e
}
