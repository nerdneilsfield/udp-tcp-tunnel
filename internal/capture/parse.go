package capture

import (
	"encoding/binary"
	"fmt"
	"net"
)

var ErrFragment = fmt.Errorf("ip fragment")

func Parse(data []byte) (*Packet, error) {
	if len(data) < 14 {
		return nil, fmt.Errorf("short ethernet")
	}
	off := 14
	typ := binary.BigEndian.Uint16(data[12:14])
	if typ == 0x8100 || typ == 0x88a8 {
		if len(data) < 18 {
			return nil, fmt.Errorf("short vlan")
		}
		typ = binary.BigEndian.Uint16(data[16:18])
		off = 18
	}
	var src net.IP
	if typ == 0x0800 {
		if len(data) < off+20 {
			return nil, fmt.Errorf("short ipv4")
		}
		ih := int(data[off]&15) * 4
		if ih < 20 || len(data) < off+ih {
			return nil, fmt.Errorf("bad ipv4")
		}
		f := binary.BigEndian.Uint16(data[off+6:])
		if f&0x1fff != 0 || f&0x2000 != 0 {
			return nil, ErrFragment
		}
		if data[off+9] != 17 {
			return nil, fmt.Errorf("not udp")
		}
		src = net.IP(data[off+12 : off+16])
		off += ih
	} else if typ == 0x86dd {
		if len(data) < off+40 {
			return nil, fmt.Errorf("short ipv6")
		}
		if data[off+6] != 17 {
			return nil, fmt.Errorf("not udp")
		}
		src = net.IP(data[off+8 : off+24])
		off += 40
	} else {
		return nil, fmt.Errorf("not ip")
	}
	if len(data) < off+8 {
		return nil, fmt.Errorf("short udp")
	}
	n := int(binary.BigEndian.Uint16(data[off+4:]))
	if n < 8 || len(data) < off+n {
		return nil, fmt.Errorf("bad udp")
	}
	return &Packet{Source: &net.UDPAddr{IP: src, Port: int(binary.BigEndian.Uint16(data[off:]))}, DestinationPort: int(binary.BigEndian.Uint16(data[off+2:])), Payload: append([]byte(nil), data[off+8:off+n]...)}, nil
}
