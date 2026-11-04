package capture

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

// Parse errors. ErrNotUDP covers anything that is simply not a UDP datagram
// over IP and is expected on a busy interface.
var (
	ErrFragment  = errors.New("ip fragment")
	ErrNotUDP    = errors.New("not a udp packet")
	ErrTruncated = errors.New("truncated packet")
)

const (
	etherTypeIPv4 = 0x0800
	etherTypeIPv6 = 0x86dd
	etherTypeVLAN = 0x8100
	etherTypeQinQ = 0x88a8

	protoUDP = 17

	ethHeaderLen  = 14
	vlanTagLen    = 4
	ipv4MinHeader = 20
	ipv6Header    = 40
	udpHeaderLen  = 8

	// IPv6 extension headers that may precede the transport header.
	ipv6HopByHop = 0
	ipv6Routing  = 43
	ipv6Fragment = 44
	ipv6DestOpts = 60
)

// Parse decodes an Ethernet frame holding a UDP datagram. It understands
// 802.1Q/802.1ad VLAN tags, IPv4 and IPv6 (with the common extension
// headers). IP fragments are reported as ErrFragment because reassembly is
// not supported.
//
// The returned payload aliases frame; callers that keep it must copy it.
func Parse(frame []byte) (Packet, error) {
	if len(frame) < ethHeaderLen {
		return Packet{}, ErrTruncated
	}
	etherType := binary.BigEndian.Uint16(frame[12:])
	off := ethHeaderLen
	for etherType == etherTypeVLAN || etherType == etherTypeQinQ {
		if len(frame) < off+vlanTagLen {
			return Packet{}, ErrTruncated
		}
		etherType = binary.BigEndian.Uint16(frame[off+2:])
		off += vlanTagLen
	}

	var (
		src  netip.Addr
		l4   []byte
		err  error
		next = frame[off:]
	)
	switch etherType {
	case etherTypeIPv4:
		src, l4, err = parseIPv4(next)
	case etherTypeIPv6:
		src, l4, err = parseIPv6(next)
	default:
		return Packet{}, ErrNotUDP
	}
	if err != nil {
		return Packet{}, err
	}
	return parseUDP(src, l4)
}

func parseIPv4(b []byte) (netip.Addr, []byte, error) {
	if len(b) < ipv4MinHeader {
		return netip.Addr{}, nil, ErrTruncated
	}
	headerLen := int(b[0]&0x0f) * 4
	if b[0]>>4 != 4 || headerLen < ipv4MinHeader || len(b) < headerLen {
		return netip.Addr{}, nil, ErrTruncated
	}
	// Fragment offset (low 13 bits) or the more-fragments flag.
	if binary.BigEndian.Uint16(b[6:])&0x3fff != 0 {
		return netip.Addr{}, nil, ErrFragment
	}
	if b[9] != protoUDP {
		return netip.Addr{}, nil, ErrNotUDP
	}
	end := len(b)
	if total := int(binary.BigEndian.Uint16(b[2:])); total >= headerLen && total < end {
		end = total // drop Ethernet padding
	}
	src, _ := netip.AddrFromSlice(b[12:16])
	return src, b[headerLen:end], nil
}

func parseIPv6(b []byte) (netip.Addr, []byte, error) {
	if len(b) < ipv6Header {
		return netip.Addr{}, nil, ErrTruncated
	}
	src, _ := netip.AddrFromSlice(b[8:24])
	end := len(b)
	if plen := ipv6Header + int(binary.BigEndian.Uint16(b[4:])); plen < end {
		end = plen
	}
	next := b[6]
	rest := b[ipv6Header:end]
	for next != protoUDP {
		switch next {
		case ipv6Fragment:
			return netip.Addr{}, nil, ErrFragment
		case ipv6HopByHop, ipv6Routing, ipv6DestOpts:
			if len(rest) < 8 {
				return netip.Addr{}, nil, ErrTruncated
			}
			hdrLen := (int(rest[1]) + 1) * 8
			if len(rest) < hdrLen {
				return netip.Addr{}, nil, ErrTruncated
			}
			next, rest = rest[0], rest[hdrLen:]
		default:
			return netip.Addr{}, nil, ErrNotUDP
		}
	}
	return src, rest, nil
}

func parseUDP(src netip.Addr, b []byte) (Packet, error) {
	if len(b) < udpHeaderLen {
		return Packet{}, ErrTruncated
	}
	length := int(binary.BigEndian.Uint16(b[4:]))
	if length < udpHeaderLen || length > len(b) {
		return Packet{}, ErrTruncated
	}
	return Packet{
		Src:     src,
		SrcPort: binary.BigEndian.Uint16(b[0:]),
		DstPort: binary.BigEndian.Uint16(b[2:]),
		Payload: b[udpHeaderLen:length],
	}, nil
}
