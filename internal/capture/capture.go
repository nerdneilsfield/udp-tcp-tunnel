// Package capture obtains UDP datagrams for the server, either by owning a
// UDP socket or by passively sniffing them with AF_PACKET.
package capture

import (
	"context"
	"net/netip"
)

// Packet is one captured UDP datagram.
type Packet struct {
	Src     netip.Addr
	SrcPort uint16
	DstPort uint16
	// Payload is owned by the receiver of the packet: captures copy it out
	// of any reusable buffer (such as the AF_PACKET ring) before delivery.
	Payload []byte
}

// Stats are cumulative counters reported by a capture.
type Stats struct {
	// KernelDrops counts packets the kernel dropped because the capture
	// could not keep up (AF_PACKET only).
	KernelDrops uint64
	// QueueFreezes counts TPACKET_V3 ring freezes (AF_PACKET only). A
	// rising count means userspace was not releasing blocks fast enough.
	QueueFreezes uint64
	// Fragments counts IP fragments that were seen and ignored.
	Fragments uint64
}

// Capture produces packets until its context is canceled. It is an
// interface so tests can inject packets.
type Capture interface {
	// Run delivers every packet to handle and returns nil when ctx is
	// canceled. handle must not block for long.
	Run(ctx context.Context, handle func(Packet)) error
	// Stats returns cumulative counters.
	Stats() Stats
	// Close releases the underlying resources. It is safe to call twice.
	Close() error
}
