//go:build linux

package capture

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gopacket/gopacket/afpacket"
)

const afpacketPollTimeout = 200 * time.Millisecond

// AFPacket passively sniffs UDP packets with an AF_PACKET socket and a
// TPACKET_V3 mmap ring, leaving the real receiver on the same port
// untouched. It needs CAP_NET_RAW and neither cgo nor libpcap.
type AFPacket struct {
	tp        *afpacket.TPacket
	fragments atomic.Uint64
	closeOnce sync.Once
}

// NewAFPacket opens the ring on iface and attaches the kernel filter.
func NewAFPacket(iface string, rules []FilterRule) (*AFPacket, error) {
	prog, err := CompileFilter(rules)
	if err != nil {
		return nil, fmt.Errorf("afpacket capture: %w", err)
	}
	tp, err := afpacket.NewTPacket(
		afpacket.OptInterface(iface),
		afpacket.TPacketVersion3,
		afpacket.OptPollTimeout(afpacketPollTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("afpacket capture on %q (needs root or CAP_NET_RAW): %w", iface, err)
	}
	if err := tp.SetBPF(prog); err != nil {
		tp.Close()
		return nil, fmt.Errorf("afpacket capture: attach bpf filter: %w", err)
	}
	_ = tp.InitSocketStats() // start kernel counters from zero
	return &AFPacket{tp: tp}, nil
}

// Run reads the ring until ctx is canceled.
func (a *AFPacket) Run(ctx context.Context, handle func(Packet)) error {
	for ctx.Err() == nil {
		// data points into the ring and is only valid until the next read.
		data, _, err := a.tp.ZeroCopyReadPacketData()
		if err != nil {
			if errors.Is(err, afpacket.ErrTimeout) {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("afpacket capture: read: %w", err)
		}
		pkt, err := Parse(data)
		switch {
		case errors.Is(err, ErrFragment):
			a.fragments.Add(1)
			continue
		case err != nil:
			continue
		}
		// Copy out of the ring before handing the payload to async code.
		pkt.Payload = append([]byte(nil), pkt.Payload...)
		handle(pkt)
	}
	return nil
}

// Stats reports cumulative kernel drops and ignored fragments.
func (a *AFPacket) Stats() Stats {
	st := Stats{Fragments: a.fragments.Load()}
	if _, v3, err := a.tp.SocketStats(); err == nil {
		st.KernelDrops = uint64(v3.Drops())
	}
	return st
}

// Close releases the ring and socket.
func (a *AFPacket) Close() error {
	a.closeOnce.Do(a.tp.Close)
	return nil
}
