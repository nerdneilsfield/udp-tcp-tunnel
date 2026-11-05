package tunnel

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/stats"
)

// Listen starts the server-side listener for cfg.Transport. cs receives the
// compression counters and may be nil.
func Listen(cfg config.Server, log *zap.Logger, cs *stats.CompressionCounters) (Listener, error) {
	ids := make([]uint16, len(cfg.Channels))
	for i, ch := range cfg.Channels {
		ids[i] = ch.ID
	}
	switch cfg.Transport {
	case config.TransportQUIC:
		tlsConf, err := ServerTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		l, err := ListenQUIC(cfg.Listen, QUICServerOptions{Token: cfg.Token, Channels: ids, TLS: tlsConf, Log: log, Compression: cfg.Compression, CompStats: cs})
		if err != nil {
			return nil, err
		}
		return l, nil
	case config.TransportKCP:
		l, err := ListenKCP(cfg.Listen, KCPServerOptions{Token: cfg.Token, Channels: ids, KCP: cfg.KCP, Log: log, Compression: cfg.Compression, CompStats: cs})
		if err != nil {
			return nil, err
		}
		return l, nil
	case config.TransportTCP, "":
		tlsConf, err := ServerTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		l, err := ListenTCP(cfg.Listen, TCPServerOptions{Token: cfg.Token, Channels: ids, TLS: tlsConf, Log: log, Compression: cfg.Compression, CompStats: cs})
		if err != nil {
			return nil, err
		}
		return l, nil
	default:
		return nil, fmt.Errorf("unknown transport %q", cfg.Transport)
	}
}

// NewDialer builds the client-side dialer for one upstream. cs receives the
// compression counters and may be nil.
func NewDialer(u config.Upstream, cs *stats.CompressionCounters) (Dialer, error) {
	var comp config.Compression
	if u.Compression != nil {
		comp = *u.Compression
	}
	tlsCfg := config.ClientTLS{}
	if u.TLS != nil {
		tlsCfg = *u.TLS
	}
	tlsConf, err := ClientTLSConfig(tlsCfg, u.Server)
	if err != nil {
		return nil, err
	}
	ids := make([]uint16, len(u.Channels))
	for i, ch := range u.Channels {
		ids[i] = ch.ID
	}
	var k config.KCP
	if u.KCP != nil {
		k = *u.KCP
	}
	switch u.Transport {
	case config.TransportQUIC:
		return NewQUICDialer(QUICDialerOptions{Addr: u.Server, Token: u.Token, Channels: ids, TLS: tlsConf, Compression: comp, CompStats: cs}), nil
	case config.TransportKCP:
		return NewKCPDialer(KCPDialerOptions{Addr: u.Server, Token: u.Token, Channels: ids, KCP: k, Compression: comp, CompStats: cs}), nil
	case config.TransportTCP, "":
		return NewTCPDialer(TCPDialerOptions{Addr: u.Server, Token: u.Token, Channels: ids, TLS: tlsConf, Compression: comp, CompStats: cs}), nil
	default:
		return nil, fmt.Errorf("unknown transport %q", u.Transport)
	}
}
