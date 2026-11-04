//go:build linux && !cgo

package capture

import (
	"context"
	"fmt"
)

type AFPacket struct{}

func NewAFPacket(string, string) (*AFPacket, error) {
	return nil, fmt.Errorf("afpacket capture requires a cgo-enabled Linux build")
}
func (*AFPacket) Run(context.Context, func(Packet)) error { return nil }
func (*AFPacket) Close() error                            { return nil }
