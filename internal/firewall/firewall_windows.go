package firewall

import (
	"net/netip"

	"prosvet/internal/strategy"
)

// WinDivert is configured by winws itself (--wf-tcp/--wf-udp), so there is
// nothing to install on Windows.
type winDivert struct{}

// New returns the no-op Windows backend.
func New() (Firewall, error) { return winDivert{}, nil }

func (winDivert) Name() string { return "WinDivert" }
func (winDivert) Apply([]strategy.PortRule, map[string][]netip.Prefix, Options) error {
	return nil
}
func (winDivert) Clear() error { return nil }
func (winDivert) Active() bool { return true }
