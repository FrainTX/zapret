// Package netroute finds out how this machine reaches the internet: through
// a VPN tunnel or directly, and which physical gateway is available. Game
// traffic that crosses a VPN pays the round trip to the VPN server on every
// packet — usually the single biggest ping cost.
package netroute

import (
	"net/netip"
	"strings"
)

// Info describes the default route.
type Info struct {
	// Iface is the interface the default route uses.
	Iface string `json:"iface"`
	// VPN is set when Iface is a tunnel (TUN/TAP/WireGuard/Wintun).
	VPN bool `json:"vpn"`
	// App is a guess at the VPN client by interface name ("Happ", "sing-box"…).
	App string `json:"app,omitempty"`
	// Phys/Gateway is a direct route around the tunnel, if one exists.
	Phys    string     `json:"phys,omitempty"`
	Gateway netip.Addr `json:"gateway"`
	PhysIdx int        `json:"-"`
	// Tunnels lists every tunnel interface that is up, comma-separated:
	// traffic leaving through them is encrypted by the VPN, so DPI never
	// sees it and the engine must leave it alone.
	Tunnels string `json:"tunnels,omitempty"`
}

// TunnelList splits Tunnels.
func (i Info) TunnelList() []string {
	if i.Tunnels == "" {
		return nil
	}
	return strings.Split(i.Tunnels, ",")
}

// CanBypass reports whether traffic can be sent around the VPN.
func (i Info) CanBypass() bool { return i.VPN && i.Phys != "" && i.Gateway.IsValid() }

var apps = []struct{ sub, name string }{
	{"happ", "Happ"}, {"xray", "Xray"}, {"v2ray", "v2rayN"}, {"sing", "sing-box"},
	{"hiddify", "Hiddify"}, {"nekoray", "NekoRay/Throne"}, {"throne", "NekoRay/Throne"},
	{"clash", "Clash"}, {"mihomo", "Clash Meta"}, {"meta", "Clash Meta"},
	{"amnezia", "AmneziaVPN"}, {"awg", "AmneziaWG"}, {"outline", "Outline"},
	{"wg", "WireGuard"}, {"wireguard", "WireGuard"}, {"proton", "Proton VPN"},
	{"nord", "NordVPN"}, {"tailscale", "Tailscale"}, {"zerotier", "ZeroTier"},
	{"openvpn", "OpenVPN"}, {"tap", "OpenVPN"}, {"tun", "VPN"}, {"utun", "VPN"},
}

// AppName guesses the VPN client from an interface name or description.
func AppName(s string) string {
	l := strings.ToLower(s)
	for _, a := range apps {
		if strings.Contains(l, a.sub) {
			return a.name
		}
	}
	return ""
}

// GameExes are the executables to exclude from a VPN client's tunnel.
var GameExes = []string{"RainbowSix.exe", "RainbowSix_Vulkan.exe", "RainbowSix_BE.exe", "ForHonor.exe", "upc.exe"}
