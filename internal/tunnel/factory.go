package tunnel

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
)

// Listen starts the server-side listener for cfg.Transport.
func Listen(cfg config.Server, log *zap.Logger) (Listener, error) {
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
		l, err := ListenQUIC(cfg.Listen, QUICServerOptions{Token: cfg.Token, Channels: ids, TLS: tlsConf, Log: log})
		if err != nil {
			return nil, err
		}
		return l, nil
	case config.TransportKCP:
		l, err := ListenKCP(cfg.Listen, KCPServerOptions{Token: cfg.Token, Channels: ids, KCP: cfg.KCP, Log: log})
		if err != nil {
			return nil, err
		}
		return l, nil
	case config.TransportTCP, "":
		tlsConf, err := ServerTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		l, err := ListenTCP(cfg.Listen, TCPServerOptions{Token: cfg.Token, Channels: ids, TLS: tlsConf, Log: log})
		if err != nil {
			return nil, err
		}
		return l, nil
	default:
		return nil, fmt.Errorf("unknown transport %q", cfg.Transport)
	}
}

// NewDialer builds the client-side dialer for one upstream.
func NewDialer(u config.Upstream) (Dialer, error) {
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
		return NewQUICDialer(QUICDialerOptions{Addr: u.Server, Token: u.Token, Channels: ids, TLS: tlsConf}), nil
	case config.TransportKCP:
		return NewKCPDialer(KCPDialerOptions{Addr: u.Server, Token: u.Token, Channels: ids, KCP: k}), nil
	case config.TransportTCP, "":
		return NewTCPDialer(TCPDialerOptions{Addr: u.Server, Token: u.Token, Channels: ids, TLS: tlsConf}), nil
	default:
		return nil, fmt.Errorf("unknown transport %q", u.Transport)
	}
}
