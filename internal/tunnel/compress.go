package tunnel

import (
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
)

// minCompressLen is the smallest payload worth compressing; shorter ones are
// sent raw.
const minCompressLen = 16

// ErrDecompressedTooLarge reports a compressed payload that would expand to
// more than MaxPayload bytes (a decompression bomb) or exceeds the decoder's memory limit.
var ErrDecompressedTooLarge = errors.New("tunnel: decompressed payload exceeds maximum size")

// Compressor compresses and decompresses single payloads. Every payload is
// independent: no state is shared between calls, so a lost or reordered
// datagram never affects later ones. It is safe for concurrent use.
type Compressor struct {
	algo     Algo
	level    int
	zenc     *zstd.Encoder
	counters *stats.CompressionCounters
}

// AlgoFor maps a configured algorithm name to its wire ID.
func AlgoFor(name string) (Algo, error) {
	switch name {
	case "", config.CompressionNone:
		return AlgoNone, nil
	case config.CompressionZstd:
		return AlgoZstd, nil
	case config.CompressionS2:
		return AlgoS2, nil
	default:
		return AlgoNone, fmt.Errorf("unknown compression algorithm %q", name)
	}
}

// NewCompressor builds a Compressor from the config, or returns nil when
// compression is off. counters may be nil.
func NewCompressor(c config.Compression, counters *stats.CompressionCounters) (*Compressor, error) {
	c = c.Resolved()
	algo, err := AlgoFor(c.Algorithm)
	if err != nil {
		return nil, err
	}
	switch algo {
	case AlgoNone:
		return nil, nil
	case AlgoZstd:
		enc, err := zstdEncoder(c.Level)
		if err != nil {
			return nil, err
		}
		return &Compressor{algo: algo, level: c.Level, zenc: enc, counters: counters}, nil
	default:
		return &Compressor{algo: algo, level: c.Level, counters: counters}, nil
	}
}

// Algo returns the wire ID, AlgoNone for a nil Compressor.
func (c *Compressor) Algo() Algo {
	if c == nil {
		return AlgoNone
	}
	return c.algo
}

// Compress returns the bytes to put on the wire for src and whether they are
// compressed. When compression does not make src smaller, it returns src
// itself and false. dst is scratch space the result may be built in.
func (c *Compressor) Compress(dst, src []byte) ([]byte, bool) {
	if len(src) >= minCompressLen {
		var out []byte
		switch c.algo {
		case AlgoZstd:
			out = c.zenc.EncodeAll(src, dst[:0])
		case AlgoS2:
			dst = dst[:cap(dst)]
			switch c.level {
			case 1:
				out = s2.Encode(dst, src)
			case 2:
				out = s2.EncodeBetter(dst, src)
			default:
				out = s2.EncodeBest(dst, src)
			}
		}
		if len(out) < len(src) {
			c.counters.AddTx(len(src), len(out), false)
			return out, true
		}
	}
	c.counters.AddTx(len(src), len(src), true)
	return src, false
}

// Receive returns the original payload of a received frame body. A raw body
// is returned as is. Failures are counted; the caller drops the frame.
func (c *Compressor) Receive(compressed bool, body []byte) ([]byte, error) {
	if !compressed {
		c.counters.AddRx(len(body), len(body))
		return body, nil
	}
	out, err := c.decompress(body)
	switch {
	case errors.Is(err, ErrDecompressedTooLarge):
		c.counters.RxOversized()
		return nil, err
	case err != nil:
		c.counters.RxError()
		return nil, err
	}
	c.counters.AddRx(len(body), len(out))
	return out, nil
}

func (c *Compressor) decompress(src []byte) ([]byte, error) {
	switch c.algo {
	case AlgoZstd:
		// A declared size above the limit is refused before any allocation.
		// Small frames may omit it, so the decoder's maximum memory
		// (MaxPayload) is the hard cap on what any frame can expand to.
		var h zstd.Header
		if err := h.Decode(src); err != nil {
			return nil, fmt.Errorf("tunnel: bad zstd frame: %w", err)
		}
		if h.WindowSize > 1<<20 {
			return nil, ErrDecompressedTooLarge
		}
		hint := min(len(src)*4, MaxPayload)
		if h.HasFCS {
			if h.FrameContentSize > MaxPayload {
				return nil, ErrDecompressedTooLarge
			}
			hint = int(h.FrameContentSize)
		}
		dec, err := zstdDecoder()
		if err != nil {
			return nil, err
		}
		out, err := dec.DecodeAll(src, make([]byte, 0, hint))
		if errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded) {
			return nil, ErrDecompressedTooLarge
		}
		if err != nil {
			return nil, fmt.Errorf("tunnel: bad zstd frame: %w", err)
		}
		return out, nil
	case AlgoS2:
		n, err := s2.DecodedLen(src)
		if err != nil {
			return nil, fmt.Errorf("tunnel: bad s2 block: %w", err)
		}
		if n > MaxPayload {
			return nil, ErrDecompressedTooLarge
		}
		out, err := s2.Decode(make([]byte, n), src)
		if err != nil {
			return nil, fmt.Errorf("tunnel: bad s2 block: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("tunnel: unexpected compressed frame with algorithm %s", c.algo)
	}
}

// zstd encoders and the decoder are shared process-wide: they are safe for
// concurrent EncodeAll/DecodeAll and allocate sizable state, so they are
// created once per level and never closed.
var (
	zstdMu   sync.Mutex
	zstdEncs = map[int]*zstd.Encoder{}

	zstdDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
		return zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(0),
			zstd.WithDecoderMaxMemory(MaxPayload),
			zstd.WithDecoderMaxWindow(1<<20))
	})
)

func zstdEncoder(level int) (*zstd.Encoder, error) {
	zstdMu.Lock()
	defer zstdMu.Unlock()
	if e, ok := zstdEncs[level]; ok {
		return e, nil
	}
	e, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(zstdLevel(level))),
		zstd.WithWindowSize(128<<10), // payloads are at most 64 KiB
		zstd.WithEncoderCRC(false),
		zstd.WithEncoderConcurrency(0))
	if err != nil {
		return nil, err
	}
	zstdEncs[level] = e
	return e, nil
}

// zstdLevel maps the config level 1..4 (fastest, default, better, best) to
// reference zstd levels.
func zstdLevel(level int) int {
	switch level {
	case 1:
		return 1
	case 2:
		return 3
	case 3:
		return 7
	default:
		return 11
	}
}
