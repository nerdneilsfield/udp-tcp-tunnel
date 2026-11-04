// Package routing maps captured UDP packets to tunnel channels.
package routing

import (
	"net/netip"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
)

type rule struct {
	id          uint16
	sourceIP    netip.Addr // invalid means "any"
	sourcePort  uint16     // zero means "any"
	destPort    uint16
	specificity int
}

// Router selects a channel for a packet.
type Router struct {
	rules []rule
}

// New builds a router from validated server channels.
func New(channels []config.ServerChannel) *Router {
	r := &Router{}
	for _, ch := range channels {
		ru := rule{id: ch.ID, sourcePort: ch.SourcePort, destPort: ch.DestinationPort}
		if addr, err := netip.ParseAddr(ch.SourceIP); err == nil {
			ru.sourceIP = addr.Unmap()
			ru.specificity += 2
		}
		if ch.SourcePort != 0 {
			ru.specificity++
		}
		r.rules = append(r.rules, ru)
	}
	return r
}

// Match returns the id of the most specific channel matching the packet.
// A rule with a source IP beats one with only a source port, which beats
// one with only a destination port; ties go to the earlier channel.
func (r *Router) Match(src netip.Addr, srcPort, dstPort uint16) (uint16, bool) {
	src = src.Unmap()
	best := -1
	var id uint16
	for _, ru := range r.rules {
		if ru.destPort != dstPort {
			continue
		}
		if ru.sourceIP.IsValid() && ru.sourceIP != src {
			continue
		}
		if ru.sourcePort != 0 && ru.sourcePort != srcPort {
			continue
		}
		if ru.specificity > best {
			best, id = ru.specificity, ru.id
		}
	}
	return id, best >= 0
}
