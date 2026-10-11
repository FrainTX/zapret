package netroute

import "syscall"

// Bind pins sockets to one network interface, so a VPN that is the default
// route does not carry them. The zero value follows the routing table.
type Bind struct {
	Iface string // Linux: SO_BINDTODEVICE (unprivileged since 5.7)
	Index int    // Windows: IP_UNICAST_IF
}

// IsZero reports whether the bind follows the routing table.
func (b Bind) IsZero() bool { return b.Iface == "" && b.Index == 0 }

// Control is a net.Dialer.Control / net.ListenConfig.Control hook.
func (b Bind) Control(network, address string, c syscall.RawConn) error {
	if b.IsZero() {
		return nil
	}
	return b.control(network, address, c)
}

// Direct returns the bind around the VPN, or the zero Bind without one.
func (i Info) Direct() Bind {
	if !i.VPN || i.Phys == "" {
		return Bind{}
	}
	return Bind{Iface: i.Phys, Index: i.PhysIdx}
}
