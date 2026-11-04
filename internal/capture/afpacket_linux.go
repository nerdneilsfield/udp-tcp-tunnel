//go:build linux

package capture

import (
	"context"
	"fmt"
	"github.com/google/gopacket/afpacket"
)

type AFPacket struct{ h *afpacket.TPacket }

func NewAFPacket(iface, filter string) (*AFPacket, error) {
	h, e := afpacket.NewTPacket(afpacket.OptInterface(iface))
	if e != nil {
		return nil, e
	}
	_ = filter
	return &AFPacket{h}, nil
}
func (a *AFPacket) Run(ctx context.Context, fn func(Packet)) error {
	return fmt.Errorf("afpacket packet decoding requires an Ethernet capture parser")
}
func (a *AFPacket) Close() error { a.h.Close(); return nil }
