package routing

import (
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"net"
)

func Match(cs []config.Channel, src *net.UDPAddr, dstPort int) (config.Channel, bool) {
	for _, c := range cs {
		if int(c.DestinationPort) != dstPort {
			continue
		}
		if c.SourceIP != "" && c.SourceIP != src.IP.String() {
			continue
		}
		if c.SourcePort != 0 && int(c.SourcePort) != src.Port {
			continue
		}
		return c, true
	}
	return config.Channel{}, false
}
