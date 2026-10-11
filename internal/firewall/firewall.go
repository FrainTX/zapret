// Package firewall diverts the right packets to the engine. On Linux this is
// an nftables table (or iptables chain) that queues only the first packets of
// each matching connection to NFQUEUE; on Windows WinDivert inside winws does
// the job and this package is a no-op.
package firewall

import (
	"net/netip"

	"prosvet/internal/strategy"
)

// Mark is the fwmark nfqws puts on packets it generates. They must never be
// queued again. It is the nfqws default (--dpi-desync-fwmark).
const Mark = 0x40000000

// DirectMark tags packets that must leave through the physical interface
// rather than a VPN tunnel (see Direct).
const DirectMark = 0x10000000

// DirectTable is the routing table (and rule priority) used for Direct.
const DirectTable = 216

// Direct sends game UDP around a VPN tunnel: straight to the physical
// gateway, where the engine handles the DPI. Every packet saves the round
// trip to the VPN server — usually tens to hundreds of milliseconds.
type Direct struct {
	Iface   string     // physical interface, e.g. "eno1"
	Gateway netip.Addr // its gateway
	Set     string     // destination IP set ("game")
	Proto   string     // "udp"
	Ports   string     // destination ports
}

// Options control rule generation.
type Options struct {
	QNum      int
	IPv6      bool
	Interface string  // optional egress interface restriction
	Direct    *Direct // optional VPN bypass for games (Linux nftables only)
	// SkipIfaces are tunnel interfaces whose traffic is never queued: a
	// VPN encrypts it, so DPI cannot see it, and decoys injected into a
	// userspace tunnel stack (xray, sing-box) would be taken as real data.
	SkipIfaces []string
}

// Firewall installs and removes the diversion rules.
type Firewall interface {
	Name() string
	Apply(rules []strategy.PortRule, sets map[string][]netip.Prefix, o Options) error
	Clear() error
	// Active reports whether our rules are currently installed.
	Active() bool
}

// private ranges never sent to the engine (mirrors ipset-exclude).
var private4 = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4"}
var private6 = []string{"::1/128", "fc00::/7", "fe80::/10", "ff00::/8"}

func split(ps []netip.Prefix) (v4, v6 []netip.Prefix) {
	for _, p := range ps {
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	return
}
