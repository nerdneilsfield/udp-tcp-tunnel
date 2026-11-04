package capture

import (
	"encoding/binary"
	"testing"
)

func TestParseIPv4UDP(t *testing.T) {
	b := make([]byte, 14+20+8+3)
	binary.BigEndian.PutUint16(b[12:], 0x0800)
	b[14] = 0x45
	b[14+9] = 17
	copy(b[26:], []byte{1, 2, 3, 4})
	copy(b[30:], []byte{5, 6, 7, 8})
	binary.BigEndian.PutUint16(b[34:], 100)
	binary.BigEndian.PutUint16(b[36:], 2368)
	binary.BigEndian.PutUint16(b[38:], 11)
	copy(b[42:], []byte("abc"))
	p, e := Parse(b)
	if e != nil || string(p.Payload) != "abc" {
		t.Fatalf("%v %#v", e, p)
	}
}
