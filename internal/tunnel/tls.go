package tunnel

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
)

// ServerTLSConfig builds the listener's TLS config, or nil when TLS is off.
func ServerTLSConfig(c config.ServerTLS) (*tls.Config, error) {
	if !c.Enabled {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load tls certificate: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ClientTLSConfig builds the dialer's TLS config, or nil when TLS is off.
// serverAddr is used to derive the server name when none is configured.
func ClientTLSConfig(c config.ClientTLS, serverAddr string) (*tls.Config, error) {
	if !c.Enabled {
		return nil, nil
	}
	conf := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         c.ServerName,
		InsecureSkipVerify: c.InsecureSkipVerify, //nolint:gosec // explicit opt-in from the config file
	}
	if conf.ServerName == "" {
		host, _, err := net.SplitHostPort(serverAddr)
		if err != nil {
			return nil, fmt.Errorf("server address %q: %w", serverAddr, err)
		}
		conf.ServerName = host
	}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read tls ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("tls ca_file contains no certificates")
		}
		conf.RootCAs = pool
	}
	return conf, nil
}
