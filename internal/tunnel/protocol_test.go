package tunnel

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var b bytes.Buffer
	if e := WriteFrame(&b, 3, []byte("abc")); e != nil {
		t.Fatal(e)
	}
	id, p, e := ReadFrame(&b)
	if e != nil || id != 3 || string(p) != "abc" {
		t.Fatalf("%d %q %v", id, p, e)
	}
}
func TestBadFrame(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{0, 1, 0xff, 0xff, 0xff, 0xff})
	if _, _, e := ReadFrame(&b); e == nil {
		t.Fatal("accepted oversized frame")
	}
}
