package firewall

import (
	"bytes"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"

	"prosvet/internal/platform"
	"prosvet/internal/strategy"
)

const nftTable = "prosvet"

// New picks the best available backend.
func New() (Firewall, error) {
	if p := platform.FindTool("nft"); p != "" {
		return &NFT{bin: p}, nil
	}
	if p := platform.FindTool("iptables"); p != "" {
		return newIPT(p), nil
	}
	return nil, fmt.Errorf("firewall: не найден ни nft, ни iptables")
}

// NFT is the nftables backend.
type NFT struct{ bin string }

func (n *NFT) Name() string { return "nftables" }

func loadModules() {
	if mp := platform.FindTool("modprobe"); mp != "" {
		for _, m := range []string{"nfnetlink_queue", "nft_queue", "nft_ct", "xt_NFQUEUE", "xt_connbytes", "xt_multiport"} {
			_ = exec.Command(mp, "-q", m).Run()
		}
	}
}

// Render returns the nft script for the given rules (exported for tests and
// for `prosvet export`).
func (n *NFT) Render(rules []strategy.PortRule, sets map[string][]netip.Prefix, o Options) string {
	var b strings.Builder
	// add+delete makes the replacement atomic and idempotent.
	fmt.Fprintf(&b, "add table inet %s\ndelete table inet %s\n", nftTable, nftTable)
	fmt.Fprintf(&b, "table inet %s {\n", nftTable)

	used := map[string]bool{}
	for _, r := range rules {
		if r.Set != "" {
			used[r.Set] = true
		}
	}
	if d := o.Direct; d != nil {
		used[d.Set] = true
	}
	for name := range used {
		v4, v6 := split(sets[name])
		writeSet(&b, name+"4", "ipv4_addr", v4)
		if o.IPv6 {
			writeSet(&b, name+"6", "ipv6_addr", v6)
		}
	}

	b.WriteString("\tchain post {\n\t\ttype filter hook postrouting priority mangle; policy accept;\n")
	fmt.Fprintf(&b, "\t\tmeta mark and 0x%x != 0 return\n", Mark)
	if o.Interface != "" {
		fmt.Fprintf(&b, "\t\toifname != %q return\n", o.Interface)
	}
	if len(o.SkipIfaces) > 0 {
		q := make([]string, len(o.SkipIfaces))
		for i, n := range o.SkipIfaces {
			q[i] = strconv.Quote(n)
		}
		fmt.Fprintf(&b, "\t\toifname { %s } return\n", strings.Join(q, ", "))
	}
	fmt.Fprintf(&b, "\t\tip daddr { %s } return\n", strings.Join(private4, ", "))
	if o.IPv6 {
		fmt.Fprintf(&b, "\t\tip6 daddr { %s } return\n", strings.Join(private6, ", "))
	} else {
		b.WriteString("\t\tmeta nfproto ipv6 return\n")
	}
	for _, r := range rules {
		ports := nftPorts(r.Ports)
		q := fmt.Sprintf("ct original packets 1-%d queue num %d bypass", r.Packets, o.QNum)
		if r.Set == "" {
			fmt.Fprintf(&b, "\t\t%s dport %s %s\n", r.Proto, ports, q)
			continue
		}
		fmt.Fprintf(&b, "\t\tip daddr @%s4 %s dport %s %s\n", r.Set, r.Proto, ports, q)
		if o.IPv6 {
			fmt.Fprintf(&b, "\t\tip6 daddr @%s6 %s dport %s %s\n", r.Set, r.Proto, ports, q)
		}
	}
	b.WriteString("\t}\n")
	if d := o.Direct; d != nil {
		writeDirect(&b, d)
	}
	b.WriteString("}\n")
	return b.String()
}

// writeDirect marks game packets (ours and the engine's decoys alike) in a
// route chain, which makes the kernel re-route them through the policy
// table; replies get the mark back from conntrack so reverse-path checks
// pass, and masquerade fixes the source address picked for the tunnel.
func writeDirect(b *strings.Builder, d *Direct) {
	m := fmt.Sprintf("0x%x", DirectMark)
	b.WriteString("\tchain direct_out {\n\t\ttype route hook output priority mangle; policy accept;\n")
	fmt.Fprintf(b, "\t\tip daddr @%s4 %s dport %s meta mark set meta mark or %s ct mark set ct mark or %s\n", d.Set, d.Proto, nftPorts(d.Ports), m, m)
	b.WriteString("\t}\n")
	b.WriteString("\tchain direct_in {\n\t\ttype filter hook prerouting priority mangle; policy accept;\n")
	fmt.Fprintf(b, "\t\tct mark and %s != 0 meta mark set meta mark or %s\n", m, m)
	b.WriteString("\t}\n")
	b.WriteString("\tchain direct_nat {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	fmt.Fprintf(b, "\t\tmeta mark and %s != 0 oifname %q masquerade\n", m, d.Iface)
	b.WriteString("\t}\n")
}

func writeSet(b *strings.Builder, name, typ string, ps []netip.Prefix) {
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s; flags interval; auto-merge;\n", name, typ)
	if len(ps) > 0 {
		b.WriteString("\t\telements = {")
		for i, p := range ps {
			if i%8 == 0 {
				b.WriteString("\n\t\t\t")
			} else {
				b.WriteString(" ")
			}
			b.WriteString(p.String())
			if i < len(ps)-1 {
				b.WriteString(",")
			}
		}
		b.WriteString("\n\t\t}\n")
	}
	b.WriteString("\t}\n")
}

func nftPorts(s string) string {
	if !strings.ContainsAny(s, ",") {
		return s
	}
	return "{ " + strings.ReplaceAll(s, ",", ", ") + " }"
}

func (n *NFT) Apply(rules []strategy.PortRule, sets map[string][]netip.Prefix, o Options) error {
	loadModules()
	script := n.Render(rules, sets, o)
	cmd := exec.Command(n.bin, "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nftables: %v: %s", err, firstLine(stderr.String()))
	}
	if o.Direct != nil {
		if err := applyDirectRoute(o.Direct); err != nil {
			_ = n.Clear()
			return err
		}
	} else {
		clearDirectRoute()
	}
	return nil
}

func (n *NFT) Clear() error {
	clearDirectRoute()
	if !n.Active() {
		return nil
	}
	out, err := exec.Command(n.bin, "delete", "table", "inet", nftTable).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nftables: %s", firstLine(string(out)))
	}
	return nil
}

func (n *NFT) Active() bool {
	return exec.Command(n.bin, "list", "table", "inet", nftTable).Run() == nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
