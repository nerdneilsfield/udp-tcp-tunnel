package stats

import (
	"go.uber.org/zap"
	"sync"
	"time"
)

type Counter struct {
	mu                     sync.Mutex
	Packets, Bytes         uint64
	Drops                  map[string]uint64
	last                   time.Time
	lastPackets, lastBytes uint64
}

func New() *Counter                   { return &Counter{Drops: map[string]uint64{}, last: time.Now()} }
func (c *Counter) Add(n int)          { c.mu.Lock(); c.Packets++; c.Bytes += uint64(n); c.mu.Unlock() }
func (c *Counter) Drop(reason string) { c.mu.Lock(); c.Drops[reason]++; c.mu.Unlock() }
func (c *Counter) Log(l *zap.Logger, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	d := now.Sub(c.last).Seconds()
	if d <= 0 {
		return
	}
	pps := float64(c.Packets-c.lastPackets) / d
	mbps := float64(c.Bytes-c.lastBytes) * 8 / d / 1e6
	l.Info("tunnel stats", zap.String("channel", name), zap.Float64("packets_per_second", pps), zap.Float64("mbps", mbps), zap.Uint64("packets", c.Packets), zap.Uint64("bytes", c.Bytes), zap.Any("drops", c.Drops))
	c.last = now
	c.lastPackets = c.Packets
	c.lastBytes = c.Bytes
}
