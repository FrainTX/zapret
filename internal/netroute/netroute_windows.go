package netroute

import (
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ifTypeEthernet = 6
	ifTypeWiFi     = 71
	ifTypePPP      = 23
	ifTypeTunnel   = 131
	ifTypePropVirt = 53
)

// Detect asks Windows which interface reaches 8.8.8.8 and whether a
// default route through a physical adapter exists next to it.
func Detect() Info {
	var idx uint32
	if err := windows.GetBestInterfaceEx(&windows.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}}, &idx); err != nil {
		return Info{}
	}
	name, typ, desc := ifInfo(idx)
	in := Info{Iface: name}
	if typ == ifTypeTunnel || typ == ifTypePropVirt || typ == ifTypePPP || AppName(name+" "+desc) != "" && typ != ifTypeEthernet && typ != ifTypeWiFi {
		in.VPN = true
		in.App = AppName(name + " " + desc)
		if in.App == "" {
			in.App = "VPN"
		}
	}
	if !in.VPN {
		return in
	}
	var tab *windows.MibIpForwardTable2
	if windows.GetIpForwardTable2(windows.AF_INET, &tab) != nil {
		return in
	}
	defer windows.FreeMibTable(unsafe.Pointer(tab))
	rows := unsafe.Slice(&tab.Table[0], tab.NumEntries)
	best := uint32(1 << 31)
	for _, r := range rows {
		if r.DestinationPrefix.PrefixLength != 0 || r.InterfaceIndex == idx {
			continue
		}
		n, t, _ := ifInfo(r.InterfaceIndex)
		if t != ifTypeEthernet && t != ifTypeWiFi {
			continue
		}
		if r.NextHop.Family != windows.AF_INET || r.Metric >= best {
			continue
		}
		// SOCKADDR_IN: the address follows the port, in network order.
		gw := *(*[4]byte)(unsafe.Pointer(&r.NextHop.Data[0]))
		if gw == [4]byte{} {
			continue
		}
		best = r.Metric
		in.Phys, in.Gateway, in.PhysIdx = n, netip.AddrFrom4(gw), int(r.InterfaceIndex)
	}
	return in
}

func ifInfo(idx uint32) (name string, typ uint32, desc string) {
	row := windows.MibIfRow2{InterfaceIndex: idx}
	if windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row) == nil {
		typ = row.Type
		desc = windows.UTF16ToString(row.Description[:])
		name = windows.UTF16ToString(row.Alias[:])
	}
	if name == "" {
		if ifc, err := net.InterfaceByIndex(int(idx)); err == nil {
			name = ifc.Name
		}
	}
	return
}
