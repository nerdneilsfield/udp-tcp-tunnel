package stats

import (
	"sync/atomic"

	"go.uber.org/zap"
)

// CompressionCounters count payload bytes before and after compression for
// one server or one client upstream. All methods are safe for concurrent use
// and on a nil receiver (compression off).
//
// "tx" is what this side compresses and sends, "rx" what it receives and
// decompresses. Bytes counted are UDP payload bytes; frame headers are not.
type CompressionCounters struct {
	txIn, txOut       atomic.Uint64 // original payload bytes, bytes on the wire
	txRaw             atomic.Uint64 // frames sent uncompressed
	rxIn, rxOut       atomic.Uint64 // bytes on the wire, payload bytes after decompression
	rxErrors, rxBombs atomic.Uint64
}

// AddTx records one sent payload of in original bytes that took out bytes
// on the wire; raw is true when it was sent uncompressed.
func (c *CompressionCounters) AddTx(in, out int, raw bool) {
	if c == nil {
		return
	}
	c.txIn.Add(uint64(in))
	c.txOut.Add(uint64(out))
	if raw {
		c.txRaw.Add(1)
	}
}

// AddRx records one received payload of in wire bytes that became out bytes.
func (c *CompressionCounters) AddRx(in, out int) {
	if c == nil {
		return
	}
	c.rxIn.Add(uint64(in))
	c.rxOut.Add(uint64(out))
}

// RxError records a frame that failed to decompress.
func (c *CompressionCounters) RxError() {
	if c != nil {
		c.rxErrors.Add(1)
	}
}

// RxOversized records a frame whose decompressed size exceeded the limit.
func (c *CompressionCounters) RxOversized() {
	if c != nil {
		c.rxBombs.Add(1)
	}
}

// CompressionSnapshot is a point-in-time view of CompressionCounters.
type CompressionSnapshot struct {
	TxIn, TxOut, TxRaw uint64
	RxIn, RxOut        uint64
	RxErrors, RxBombs  uint64
}

// Snapshot reads the counters.
func (c *CompressionCounters) Snapshot() CompressionSnapshot {
	return CompressionSnapshot{
		TxIn: c.txIn.Load(), TxOut: c.txOut.Load(), TxRaw: c.txRaw.Load(),
		RxIn: c.rxIn.Load(), RxOut: c.rxOut.Load(),
		RxErrors: c.rxErrors.Load(), RxBombs: c.rxBombs.Load(),
	}
}

// ratio is original size divided by wire size; 0 when nothing was counted.
func ratio(original, wire uint64) float64 {
	if wire == 0 {
		return 0
	}
	return round2(float64(original) / float64(wire))
}

type compressionEntry struct {
	algorithm string
	counters  *CompressionCounters
}

// SetCompression enables compression stats for an upstream (empty on the
// server) and returns its counters, or nil when algorithm is "none" or empty.
// It must be called before Run.
func (r *Registry) SetCompression(upstream, algorithm string) *CompressionCounters {
	if algorithm == "" || algorithm == "none" {
		return nil
	}
	e, ok := r.compression[upstream]
	if !ok {
		e = &compressionEntry{counters: &CompressionCounters{}}
		r.compression[upstream] = e
		r.compressionOrder = append(r.compressionOrder, upstream)
	}
	e.algorithm = algorithm
	return e.counters
}

// Compression returns the counters of an upstream, or nil.
func (r *Registry) Compression(upstream string) *CompressionCounters {
	if e, ok := r.compression[upstream]; ok {
		return e.counters
	}
	return nil
}

func (r *Registry) logCompression() {
	for _, upstream := range r.compressionOrder {
		e := r.compression[upstream]
		s := e.counters.Snapshot()
		fields := []zap.Field{zap.String("side", r.side)}
		if upstream != "" {
			fields = append(fields, zap.String("upstream", upstream))
		}
		if t := r.transports[upstream]; t != "" {
			fields = append(fields, zap.String("transport", t))
		}
		fields = append(fields,
			zap.String("algorithm", e.algorithm),
			zap.Uint64("tx_bytes_in", s.TxIn),
			zap.Uint64("tx_bytes_out", s.TxOut),
			zap.Float64("tx_ratio", ratio(s.TxIn, s.TxOut)),
			zap.Uint64("tx_raw_frames", s.TxRaw),
			zap.Uint64("rx_bytes_in", s.RxIn),
			zap.Uint64("rx_bytes_out", s.RxOut),
			zap.Float64("rx_ratio", ratio(s.RxOut, s.RxIn)),
			zap.Uint64("rx_decompress_errors", s.RxErrors),
			zap.Uint64("rx_oversized", s.RxBombs),
		)
		r.log.Info("compression stats", fields...)
	}
}
