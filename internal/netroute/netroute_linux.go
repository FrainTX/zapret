package netroute

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type route struct {
	iface  string
	gw     netip.Addr
	metric int
}

// Detect reads the IPv4 main table from /proc/net/route.
func Detect() Info {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return Info{}
	}
	defer f.Close()
	var defs []route
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 8 || fs[1] != "00000000" || fs[7] != "00000000" {
			continue // only 0.0.0.0/0
		}
		r := route{iface: fs[0]}
		if b, err := hex.DecodeString(fs[2]); err == nil && len(b) == 4 {
			var a [4]byte
			binary.LittleEndian.PutUint32(a[:], binary.BigEndian.Uint32(b))
			if gw := netip.AddrFrom4(a); !gw.IsUnspecified() {
				r.gw = gw
			}
		}
		r.metric, _ = strconv.Atoi(fs[6])
		defs = append(defs, r)
	}
	if len(defs) == 0 {
		return Info{Tunnels: tunnels()}
	}
	best := defs[0]
	for _, r := range defs[1:] {
		if r.metric < best.metric {
			best = r
		}
	}
	in := Info{Iface: best.iface, VPN: isTunnel(best.iface), Tunnels: tunnels()}
	if in.VPN {
		in.App = AppName(best.iface)
		if in.App == "" {
			in.App = "VPN"
		}
		var phys *route
		for i, r := range defs {
			if !isTunnel(r.iface) && r.gw.IsValid() && (phys == nil || r.metric < phys.metric) {
				phys = &defs[i]
			}
		}
		if phys != nil {
			in.Phys, in.Gateway = phys.iface, phys.gw
		}
	}
	return in
}

// isTunnel: TUN devices have tun_flags, WireGuard has type 65534 (NONE),
// PPP 512, and anything without a MAC address is not a physical NIC.
func isTunnel(iface string) bool {
	base := filepath.Join("/sys/class/net", iface)
	if _, err := os.Stat(filepath.Join(base, "tun_flags")); err == nil {
		return true
	}
	if b, err := os.ReadFile(filepath.Join(base, "type")); err == nil {
		switch strings.TrimSpace(string(b)) {
		case "65534", "512", "776", "778", "823": // none, ppp, sit, gre, ip6gre
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(base, "device")); err == nil {
		return false // backed by hardware
	}
	return AppName(iface) != ""
}

// tunnels lists tunnel interfaces that are up, sorted.
func tunnels() string {
	ents, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if n == "lo" || !isTunnel(n) || strings.ContainsAny(n, "\" ,") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join("/sys/class/net", n, "operstate")); err == nil && strings.TrimSpace(string(b)) == "down" {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
