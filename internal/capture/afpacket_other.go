//go:build !linux

package capture

import (
	"context"
	"fmt"
)

type AFPacket struct{}

func NewAFPacket(string, string) (*AFPacket, error) {
	return nil, fmt.Errorf("afpacket capture is only supported on Linux")
}
func (*AFPacket) Run(context.Context, func(Packet)) error { return nil }
func (*AFPacket) Close() error                            { return nil }
