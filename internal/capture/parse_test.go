package capture

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

// frameSpec describes a crafted Ethernet frame.
type frameSpec struct {
	vlanTags int
	v6       bool
	src      netip.Addr
	srcPort  uint16
	dstPort  uint16
	payload  []byte
	// fragFlags is the IPv4 flags/fragment-offset field.
	fragFlags uint16
	proto     byte // IP protocol, default UDP
	padding   int  // trailing Ethernet padding bytes
	extHeader bool // IPv6: insert a hop-by-hop header
	fragment6 bool // IPv6: insert a fragment header
}

func udpSegment(spec frameSpec) []byte {
	b := make([]byte, udpHeaderLen+len(spec.payload))
	binary.BigEndian.PutUint16(b[0:], spec.srcPort)
	binary.BigEndian.PutUint16(b[2:], spec.dstPort)
	binary.BigEndian.PutUint16(b[4:], uint16(len(b)))
	copy(b[udpHeaderLen:], spec.payload)
	return b
}

func craftFrame(spec frameSpec) []byte {
	if spec.proto == 0 {
		spec.proto = protoUDP
	}
	frame := make([]byte, 12) // MACs
	for range spec.vlanTags {
		frame = binary.BigEndian.AppendUint16(frame, etherTypeVLAN)
		frame = binary.BigEndian.AppendUint16(frame, 100) // TCI
	}
	etherType := uint16(etherTypeIPv4)
	if spec.v6 {
		etherType = etherTypeIPv6
	}
	frame = binary.BigEndian.AppendUint16(frame, etherType)

	l4 := udpSegment(spec)
	if spec.v6 {
		next := spec.proto
		var ext []byte
		if spec.extHeader {
			ext = append(ext, next, 0, 0, 0, 0, 0, 0, 0) // 8-byte hop-by-hop
			next = ipv6HopByHop
		}
		if spec.fragment6 {
			ext = append(ext, next, 0, 0, 0, 0, 0, 0, 1)
			next = ipv6Fragment
		}
		hdr := make([]byte, ipv6Header)
		hdr[0] = 0x60
		binary.BigEndian.PutUint16(hdr[4:], uint16(len(ext)+len(l4)))
		hdr[6] = next
		hdr[7] = 64
		s := spec.src.As16()
		copy(hdr[8:], s[:])
		frame = append(frame, hdr...)
		frame = append(frame, ext...)
	} else {
		hdr := make([]byte, ipv4MinHeader)
		hdr[0] = 0x45
		binary.BigEndian.PutUint16(hdr[2:], uint16(ipv4MinHeader+len(l4)))
		binary.BigEndian.PutUint16(hdr[6:], spec.fragFlags)
		hdr[8] = 64
		hdr[9] = spec.proto
		s := spec.src.As4()
		copy(hdr[12:], s[:])
		copy(hdr[16:], []byte{10, 0, 0, 1})
		frame = append(frame, hdr...)
	}
	frame = append(frame, l4...)
	return append(frame, make([]byte, spec.padding)...)
}

func TestParse(t *testing.T) {
	v4 := netip.MustParseAddr("192.168.1.201")
	v6 := netip.MustParseAddr("fe80::1")
	payload := []byte("lidar-packet")

	tests := []struct {
		name string
		spec frameSpec
		src  netip.Addr
	}{
		{"ipv4", frameSpec{src: v4}, v4},
		{"ipv4 vlan", frameSpec{src: v4, vlanTags: 1}, v4},
		{"ipv4 double vlan", frameSpec{src: v4, vlanTags: 2}, v4},
		{"ipv4 ethernet padding", frameSpec{src: v4, padding: 20}, v4},
		{"ipv6", frameSpec{src: v6, v6: true}, v6},
		{"ipv6 vlan", frameSpec{src: v6, v6: true, vlanTags: 1}, v6},
		{"ipv6 hop-by-hop header", frameSpec{src: v6, v6: true, extHeader: true}, v6},
		{"ipv4 dont-fragment flag", frameSpec{src: v4, fragFlags: 0x4000}, v4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.srcPort, tc.spec.dstPort, tc.spec.payload = 5000, 2368, payload
			pkt, err := Parse(craftFrame(tc.spec))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if pkt.Src != tc.src || pkt.SrcPort != 5000 || pkt.DstPort != 2368 {
				t.Fatalf("got %v:%d -> %d", pkt.Src, pkt.SrcPort, pkt.DstPort)
			}
			if !bytes.Equal(pkt.Payload, payload) {
				t.Fatalf("payload %q, want %q", pkt.Payload, payload)
			}
		})
	}
}

func TestParseEmptyPayload(t *testing.T) {
	pkt, err := Parse(craftFrame(frameSpec{src: netip.MustParseAddr("1.2.3.4"), dstPort: 9}))
	if err != nil || len(pkt.Payload) != 0 {
		t.Fatalf("got %v, %v", pkt, err)
	}
}

func TestParseRejects(t *testing.T) {
	v4 := netip.MustParseAddr("192.168.1.201")
	v6 := netip.MustParseAddr("fe80::1")
	good := craftFrame(frameSpec{src: v4, dstPort: 2368, payload: []byte("abcdef")})

	tests := []struct {
		name  string
		frame []byte
		want  error
	}{
		{"ipv4 more fragments", craftFrame(frameSpec{src: v4, fragFlags: 0x2000}), ErrFragment},
		{"ipv4 later fragment", craftFrame(frameSpec{src: v4, fragFlags: 0x00b9}), ErrFragment},
		{"ipv4 vlan fragment", craftFrame(frameSpec{src: v4, vlanTags: 1, fragFlags: 0x2000}), ErrFragment},
		{"ipv6 fragment header", craftFrame(frameSpec{src: v6, v6: true, fragment6: true}), ErrFragment},
		{"ipv4 tcp", craftFrame(frameSpec{src: v4, proto: 6}), ErrNotUDP},
		{"ipv6 tcp", craftFrame(frameSpec{src: v6, v6: true, proto: 6}), ErrNotUDP},
		{"arp", append(make([]byte, 12), 0x08, 0x06, 0, 0), ErrNotUDP},
		{"empty", nil, ErrTruncated},
		{"short ethernet", good[:10], ErrTruncated},
		{"short vlan", append(make([]byte, 12), 0x81, 0x00, 0x00), ErrTruncated},
		{"short ip header", good[:ethHeaderLen+10], ErrTruncated},
		{"short udp header", good[:ethHeaderLen+ipv4MinHeader+4], ErrTruncated},
		{"udp length beyond frame", good[:len(good)-2], ErrTruncated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.frame); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
