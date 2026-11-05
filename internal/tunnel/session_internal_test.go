package tunnel

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
)

// A frame that fails to decompress is dropped and counted; the frames
// after it on the same stream are delivered.
func TestConnSessionDropsBadCompressedFrames(t *testing.T) {
	cs := &stats.CompressionCounters{}
	comp, err := NewCompressor(config.Compression{Algorithm: "s2"}, cs)
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer a.Close()
	rx := newConnSession(b, 0, comp)
	defer rx.Close()

	good := bytes.Repeat([]byte("good payload "), 40)
	body, ok := comp.Compress(nil, good)
	if !ok {
		t.Fatal("test payload did not compress")
	}
	go func() {
		var buf []byte
		buf, _ = appendFrame(buf, 1, true, bytes.Repeat([]byte{0xff}, 32)) // corrupt
		buf, _ = appendFrame(buf, 1, true, []byte{})                       // empty compressed body
		buf, _ = appendFrame(buf, 2, true, body)
		_, _ = a.Write(buf)
	}()

	_ = b.SetReadDeadline(time.Now().Add(5 * time.Second))
	ch, p, err := rx.Receive()
	if err != nil || ch != 2 || !bytes.Equal(p, good) {
		t.Fatalf("Receive: channel %d, %d bytes, %v", ch, len(p), err)
	}
	if s := cs.Snapshot(); s.RxErrors != 2 || s.RxBombs != 0 {
		t.Fatalf("counters: %+v", s)
	}
}

// A compressed frame on a session that did not negotiate compression is a
// protocol error, not something to guess at.
func TestConnSessionRejectsUnexpectedCompressedFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	rx := newConnSession(b, 0, nil)
	defer rx.Close()
	go func() {
		buf, _ := appendFrame(nil, 1, true, []byte("x"))
		_, _ = a.Write(buf)
	}()
	_ = b.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := rx.Receive(); err == nil {
		t.Fatal("unexpected compressed frame accepted")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
