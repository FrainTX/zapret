package firewall

import (
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"prosvet/internal/platform"
	"prosvet/internal/strategy"
)

func testRules() ([]strategy.PortRule, map[string][]netip.Prefix) {
	rules := []strategy.PortRule{
		{Proto: "tcp", Ports: "80,443,2053,2083,2087,2096,8443", Packets: 6},
		{Proto: "udp", Ports: "443", Packets: 6},
		{Proto: "udp", Ports: "19294-19344,50000-50100", Packets: 12},
		{Proto: "udp", Ports: "1024-65535", Set: "game", Packets: 12},
	}
	sets := map[string][]netip.Prefix{"game": {
		netip.MustParsePrefix("3.0.0.0/9"), netip.MustParsePrefix("20.33.0.0/16"), netip.MustParsePrefix("2a05:d000::/25"),
	}}
	return rules, sets
}

func TestNFTRender(t *testing.T) {
	n := &NFT{}
	rules, sets := testRules()
	s := n.Render(rules, sets, Options{QNum: 216})
	for _, want := range []string{
		"delete table inet prosvet",
		"set game4 {",
		"3.0.0.0/9, 20.33.0.0/16",
		"tcp dport { 80, 443, 2053, 2083, 2087, 2096, 8443 } ct original packets 1-6 queue num 216 bypass",
		"ip daddr @game4 udp dport 1024-65535 ct original packets 1-12 queue num 216 bypass",
		"meta nfproto ipv6 return",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "game6") {
		t.Error("IPv6 set rendered with IPv6 disabled")
	}
	s6 := n.Render(rules, sets, Options{QNum: 216, IPv6: true})
	if !strings.Contains(s6, "ip6 daddr @game6") || !strings.Contains(s6, "2a05:d000::/25") {
		t.Errorf("IPv6 rules missing:\n%s", s6)
	}
}

// TestNFTSyntax asks nft itself to check the script when allowed to.
func TestNFTSyntax(t *testing.T) {
	bin := platform.FindTool("nft")
	if bin == "" {
		t.Skip("nft not installed")
	}
	rules, sets := testRules()
	script := (&NFT{}).Render(rules, sets, Options{QNum: 216, IPv6: true})
	// Skip the add/delete prologue: checking a delete of a missing table fails.
	script = script[strings.Index(script, "table inet prosvet {"):]
	cmd := exec.Command(bin, "-c", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "Operation not permitted") || strings.Contains(string(out), "ermission") {
			t.Skip("nft -c needs privileges here")
		}
		t.Fatalf("nft rejected the script: %v\n%s\n%s", err, out, script)
	}
}

func TestIPTPorts(t *testing.T) {
	g := iptPorts("80,443,1024-65535")
	if len(g) != 1 || strings.Join(g[0], ",") != "80,443,1024:65535" {
		t.Errorf("got %v", g)
	}
	many := iptPorts("1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16")
	if len(many) != 2 {
		t.Errorf("multiport limit not respected: %v", many)
	}
}

// TestNFTApplyNetns installs and removes the real table. Run it inside an
// unprivileged network namespace:
//
//	go test -c ./internal/firewall && unshare -rn ./firewall.test -test.run Netns
func TestNFTApplyNetns(t *testing.T) {
	if !isRootInNS() {
		t.Skip("run under `unshare -rn`")
	}
	fw, err := New()
	if err != nil {
		t.Fatal(err)
	}
	rules, sets := testRules()
	for i := 0; i < 2; i++ { // second Apply must replace, not duplicate
		if err := fw.Apply(rules, sets, Options{QNum: 216, IPv6: true}); err != nil {
			t.Fatal(err)
		}
	}
	if !fw.Active() {
		t.Fatal("table not active after Apply")
	}
	out, _ := exec.Command(platform.FindTool("nft"), "list", "chain", "inet", "prosvet", "post").Output()
	if n := strings.Count(string(out), "queue"); n != 5 {
		t.Errorf("expected 5 queue rules, got %d:\n%s", n, out)
	}
	if err := fw.Clear(); err != nil {
		t.Fatal(err)
	}
	if fw.Active() {
		t.Fatal("table still active after Clear")
	}
}
