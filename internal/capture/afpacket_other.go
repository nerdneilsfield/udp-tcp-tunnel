//go:build !linux

package capture

import (
	"context"
	"errors"
)

// AFPacket is only functional on Linux; this stub lets other platforms
// compile and report a clear error.
type AFPacket struct{}

// NewAFPacket always fails off Linux.
func NewAFPacket(string, []FilterRule) (*AFPacket, error) {
	return nil, errors.New("afpacket capture is only supported on Linux")
}

// Run is unreachable because NewAFPacket never succeeds.
func (*AFPacket) Run(context.Context, func(Packet)) error { return nil }

// Stats is unreachable because NewAFPacket never succeeds.
func (*AFPacket) Stats() Stats { return Stats{} }

// Close is a no-op.
func (*AFPacket) Close() error { return nil }
