// Package stats keeps per-channel traffic counters and logs them periodically.
package stats

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Reason says why a packet was dropped.
type Reason int

// Drop reasons.
const (
	QueueFull Reason = iota
	UnknownChannel
	Oversized
	Fragment
	CaptureDrop
	CaptureFreeze
	NoClient
	WriteError
	numReasons
)

var reasonNames = [numReasons]string{
	QueueFull:      "queue_full",
	UnknownChannel: "unknown_channel",
	Oversized:      "oversized",
	Fragment:       "fragment",
	CaptureDrop:    "capture_drop",
	CaptureFreeze:  "capture_freeze",
	NoClient:       "no_client",
	WriteError:     "write_error",
}

// String returns the name used in log fields.
func (r Reason) String() string { return reasonNames[r] }

// Counters hold traffic totals and drop counts. All methods are safe for
// concurrent use.
type Counters struct {
	packets atomic.Uint64
	bytes   atomic.Uint64
	drops   [numReasons]atomic.Uint64
}

// Add records one forwarded packet of n payload bytes.
func (c *Counters) Add(n int) {
	c.packets.Add(1)
	c.bytes.Add(uint64(n))
}

// Drop records one dropped packet.
func (c *Counters) Drop(r Reason) { c.drops[r].Add(1) }

// SetDrops overwrites a drop counter with an absolute value, for sources
// such as the kernel that report cumulative totals.
func (c *Counters) SetDrops(r Reason, total uint64) { c.drops[r].Store(total) }

// Drops returns the current count for a reason.
func (c *Counters) Drops(r Reason) uint64 { return c.drops[r].Load() }

// Packets returns the total forwarded packets.
func (c *Counters) Packets() uint64 { return c.packets.Load() }

// Bytes returns the total forwarded payload bytes.
func (c *Counters) Bytes() uint64 { return c.bytes.Load() }

// Snapshot is a point-in-time view of one set of counters.
type Snapshot struct {
	Upstream string
	ID       uint16
	Name     string
	Packets  uint64
	Bytes    uint64
	PPS      float64
	Mbps     float64
	Drops    [numReasons]uint64
}

// DropCount returns the drops recorded for a reason.
func (s Snapshot) DropCount(r Reason) uint64 { return s.Drops[r] }

type channelKey struct {
	upstream string
	id       uint16
}

type channel struct {
	upstream    string
	id          uint16
	name        string
	counters    Counters
	lastPackets uint64
	lastBytes   uint64
}

// Registry owns the counters of every channel plus a "global" set for
// drops that cannot be attributed to a channel (unknown channel, fragments,
// kernel capture drops and ring freezes). On the client, each upstream has
// its own global set; the server uses the empty upstream name.
type Registry struct {
	side        string
	log         *zap.Logger
	channels    map[channelKey]*channel
	order       []*channel
	globals     map[string]*Counters
	globalOrder []string
	last        time.Time
}

// New builds a registry. side is "server" or "client" and appears in logs.
func New(side string, log *zap.Logger) *Registry {
	return &Registry{
		side:     side,
		log:      log,
		channels: make(map[channelKey]*channel),
		globals:  make(map[string]*Counters),
		last:     time.Now(),
	}
}

// AddChannel registers a channel with no upstream label. It must be called
// before Run and before the registry is shared between goroutines.
func (r *Registry) AddChannel(id uint16, name string) *Counters {
	return r.AddUpstreamChannel("", id, name)
}

// AddUpstreamChannel registers a channel under an upstream name. On the
// client, upstream is the configured upstream name; on the server it is empty.
func (r *Registry) AddUpstreamChannel(upstream string, id uint16, name string) *Counters {
	key := channelKey{upstream: upstream, id: id}
	ch := &channel{upstream: upstream, id: id, name: name}
	r.channels[key] = ch
	r.order = append(r.order, ch)
	sort.Slice(r.order, func(i, j int) bool {
		if r.order[i].upstream != r.order[j].upstream {
			return r.order[i].upstream < r.order[j].upstream
		}
		return r.order[i].id < r.order[j].id
	})
	_ = r.UpstreamGlobal(upstream)
	return &ch.counters
}

// Channel returns the counters for a registered channel with no upstream
// label, or nil.
func (r *Registry) Channel(id uint16) *Counters {
	return r.UpstreamChannel("", id)
}

// UpstreamChannel returns the counters for a registered channel, or nil.
func (r *Registry) UpstreamChannel(upstream string, id uint16) *Counters {
	if ch, ok := r.channels[channelKey{upstream: upstream, id: id}]; ok {
		return &ch.counters
	}
	return nil
}

// Global returns the counters for unattributed drops with no upstream label.
func (r *Registry) Global() *Counters { return r.UpstreamGlobal("") }

// UpstreamGlobal returns the counters for unattributed drops of one upstream.
func (r *Registry) UpstreamGlobal(upstream string) *Counters {
	if c, ok := r.globals[upstream]; ok {
		return c
	}
	c := &Counters{}
	r.globals[upstream] = c
	r.globalOrder = append(r.globalOrder, upstream)
	sort.Strings(r.globalOrder)
	return c
}

// Snapshot computes per-channel rates since the previous call and advances
// the baseline. Global entries are returned separately, one per upstream.
func (r *Registry) Snapshot(now time.Time) (channels []Snapshot, globals []Snapshot) {
	elapsed := now.Sub(r.last).Seconds()
	r.last = now

	for _, ch := range r.order {
		snap := snapshotOf(&ch.counters)
		snap.Upstream, snap.ID, snap.Name = ch.upstream, ch.id, ch.name
		if elapsed > 0 {
			snap.PPS = float64(snap.Packets-ch.lastPackets) / elapsed
			snap.Mbps = float64(snap.Bytes-ch.lastBytes) * 8 / elapsed / 1e6
		}
		ch.lastPackets, ch.lastBytes = snap.Packets, snap.Bytes
		channels = append(channels, snap)
	}
	for _, upstream := range r.globalOrder {
		snap := snapshotOf(r.globals[upstream])
		snap.Upstream = upstream
		globals = append(globals, snap)
	}
	return channels, globals
}

func snapshotOf(c *Counters) Snapshot {
	s := Snapshot{Packets: c.Packets(), Bytes: c.Bytes()}
	for i := range s.Drops {
		s.Drops[i] = c.Drops(Reason(i))
	}
	return s
}

// Log writes one line per channel plus one global line per upstream.
func (r *Registry) Log(now time.Time) {
	channels, globals := r.Snapshot(now)
	for _, s := range channels {
		fields := []zap.Field{
			zap.String("side", r.side),
		}
		if s.Upstream != "" {
			fields = append(fields, zap.String("upstream", s.Upstream))
		}
		fields = append(fields,
			zap.Uint16("channel", s.ID),
			zap.String("name", s.Name),
			zap.Float64("pps", round2(s.PPS)),
			zap.Float64("mbps", round2(s.Mbps)),
			zap.Uint64("packets", s.Packets),
			zap.Uint64("bytes", s.Bytes),
		)
		r.log.Info("channel stats", append(fields, dropFields(s)...)...)
	}
	for _, g := range globals {
		fields := []zap.Field{zap.String("side", r.side)}
		if g.Upstream != "" {
			fields = append(fields, zap.String("upstream", g.Upstream))
		}
		r.log.Info("global drops", append(fields, dropFields(g)...)...)
	}
}

func dropFields(s Snapshot) []zap.Field {
	fields := make([]zap.Field, 0, numReasons)
	for i := range s.Drops {
		fields = append(fields, zap.Uint64("drop_"+Reason(i).String(), s.Drops[i]))
	}
	return fields
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// Run logs every interval until ctx is canceled. beforeLog, if not nil, runs
// before each report so callers can refresh externally sourced counters.
func (r *Registry) Run(ctx context.Context, interval time.Duration, beforeLog func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if beforeLog != nil {
				beforeLog()
			}
			r.Log(now)
		}
	}
}
