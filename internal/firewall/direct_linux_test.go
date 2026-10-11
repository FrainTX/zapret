package firewall

import (
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDirectRender(t *testing.T) {
	d := &Direct{Iface: "eno1", Gateway: netip.MustParseAddr("192.168.1.1"), Set: "game", Proto: "udp", Ports: "1024-65535"}
	s := (&NFT{}).Render(nil, map[string][]netip.Prefix{"game": {netip.MustParsePrefix("52.0.0.0/8")}}, Options{QNum: 216, Direct: d})
	for _, want := range []string{
		"set game4 {",
		"type route hook output priority mangle",
		"ip daddr @game4 udp dport 1024-65535 meta mark set meta mark or 0x10000000 ct mark set ct mark or 0x10000000",
		"ct mark and 0x10000000 != 0 meta mark set meta mark or 0x10000000",
		`meta mark and 0x10000000 != 0 oifname "eno1" masquerade`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
}

// TestDirectNetns runs inside a throwaway network namespace prepared by
// scripts (see PROSVET_DIRECT_NS): a TUN device is the default route (the
// "VPN", nobody reads it), a veth leads to a gateway namespace that echoes
// UDP on 52.0.0.1:7000. Without Direct the echo never comes back; with it,
// it does.
func TestDirectNetns(t *testing.T) {
	if os.Getenv("PROSVET_DIRECT_NS") == "" || !isRootInNS() {
		t.Skip("set PROSVET_DIRECT_NS=1 inside the prepared namespace")
	}
	echo := func() error {
		c, err := net.Dial("udp4", "52.0.0.1:7000")
		if err != nil {
			return err
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(700 * time.Millisecond))
		if _, err := c.Write([]byte("ping")); err != nil {
			return err
		}
		b := make([]byte, 16)
		_, err = c.Read(b)
		return err
	}
	if echo() == nil {
		t.Fatal("echo works without Direct: the namespace is not set up like a VPN")
	}
	n, _ := New()
	nft, ok := n.(*NFT)
	if !ok {
		t.Skip("nft missing")
	}
	sets := map[string][]netip.Prefix{"game": {netip.MustParsePrefix("52.0.0.0/8")}}
	d := &Direct{Iface: "veth0", Gateway: netip.MustParseAddr("192.168.50.1"), Set: "game", Proto: "udp", Ports: "1024-65535"}
	if err := nft.Apply(nil, sets, Options{QNum: 216, Direct: d}); err != nil {
		t.Fatal(err)
	}
	defer nft.Clear()
	if err := echo(); err != nil {
		t.Fatalf("echo through Direct: %v", err)
	}
	if err := nft.Clear(); err != nil {
		t.Fatal(err)
	}
	if echo() == nil {
		t.Fatal("echo still works after Clear: route not removed")
	}
}
