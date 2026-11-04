//go:build linux && cgo

package capture

import (
	"context"
	"github.com/google/gopacket/afpacket"
	"golang.org/x/net/bpf"
)

type AFPacket struct{ h *afpacket.TPacket }

func NewAFPacket(iface, filter string) (*AFPacket, error) {
	h, e := afpacket.NewTPacket(afpacket.OptInterface(iface), afpacket.OptPollTimeout(250000000))
	if e != nil {
		return nil, e
	}
	if filter != "" {
		_ = h.SetBPF([]bpf.RawInstruction{{Op: 0x28, Jt: 0, Jf: 0, K: 12}})
	}
	return &AFPacket{h}, nil
}
func (a *AFPacket) Run(ctx context.Context, fn func(Packet)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		data, _, e := a.h.ZeroCopyReadPacketData()
		if e != nil {
			if e == afpacket.ErrTimeout {
				continue
			}
			return e
		}
		p, e := Parse(data)
		if e == nil {
			fn(*p)
		}
	}
}
func (a *AFPacket) Close() error { a.h.Close(); return nil }
