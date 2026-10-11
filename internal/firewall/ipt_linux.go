package firewall

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"

	"prosvet/internal/platform"
	"prosvet/internal/strategy"
)

const iptChain = "PROSVET"

// IPT is the iptables (+ optional ipset) backend for systems without nft.
type IPT struct {
	v4, v6, ipset string
}

func newIPT(bin string) *IPT {
	return &IPT{v4: bin, v6: platform.FindTool("ip6tables"), ipset: platform.FindTool("ipset")}
}

func (t *IPT) Name() string {
	if t.ipset != "" {
		return "iptables + ipset"
	}
	return "iptables"
}

func run(bin string, args ...string) error {
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", bin, strings.Join(args, " "), firstLine(string(out)))
	}
	return nil
}

// iptPorts converts zapret port syntax to multiport syntax (ranges with ':').
// multiport accepts at most 15 ports (a range counts as two).
func iptPorts(s string) [][]string {
	parts := strings.Split(s, ",")
	var groups [][]string
	var cur []string
	weight := 0
	for _, p := range parts {
		w := 1
		if strings.Contains(p, "-") {
			w = 2
		}
		if weight+w > 15 {
			groups = append(groups, cur)
			cur, weight = nil, 0
		}
		cur = append(cur, strings.Replace(p, "-", ":", 1))
		weight += w
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

func (t *IPT) Apply(rules []strategy.PortRule, sets map[string][]netip.Prefix, o Options) error {
	loadModules()
	_ = t.Clear()
	families := []struct {
		bin     string
		v6      bool
		private []string
	}{{t.v4, false, private4}}
	if o.IPv6 && t.v6 != "" {
		families = append(families, struct {
			bin     string
			v6      bool
			private []string
		}{t.v6, true, private6})
	}
	for _, fam := range families {
		suffix := "4"
		if fam.v6 {
			suffix = "6"
		}
		if err := run(fam.bin, "-t", "mangle", "-N", iptChain); err != nil {
			return err
		}
		base := []string{"-t", "mangle", "-A", iptChain}
		if err := run(fam.bin, append(base, "-m", "mark", "--mark", fmt.Sprintf("0x%x/0x%x", Mark, Mark), "-j", "RETURN")...); err != nil {
			return err
		}
		if o.Interface != "" {
			if err := run(fam.bin, append(base, "!", "-o", o.Interface, "-j", "RETURN")...); err != nil {
				return err
			}
		}
		for _, n := range o.SkipIfaces {
			if err := run(fam.bin, append(base, "-o", n, "-j", "RETURN")...); err != nil {
				return err
			}
		}
		for _, p := range fam.private {
			if err := run(fam.bin, append(base, "-d", p, "-j", "RETURN")...); err != nil {
				return err
			}
		}
		for _, r := range rules {
			var match []string
			if r.Set != "" && t.ipset != "" {
				setName := "prosvet_" + r.Set + suffix
				if err := t.loadSet(setName, fam.v6, sets[r.Set]); err != nil {
					return err
				}
				match = []string{"-m", "set", "--match-set", setName, "dst"}
			}
			for _, ports := range iptPorts(r.Ports) {
				args := append(append([]string{}, base...), "-p", r.Proto)
				args = append(args, match...)
				args = append(args, "-m", "multiport", "--dports", strings.Join(ports, ","),
					"-m", "connbytes", "--connbytes-dir=original", "--connbytes-mode=packets", "--connbytes", "1:"+strconv.Itoa(r.Packets),
					"-j", "NFQUEUE", "--queue-num", strconv.Itoa(o.QNum), "--queue-bypass")
				if err := run(fam.bin, args...); err != nil {
					return err
				}
			}
		}
		if err := run(fam.bin, "-t", "mangle", "-I", "POSTROUTING", "-j", iptChain); err != nil {
			return err
		}
	}
	return nil
}

func (t *IPT) loadSet(name string, v6 bool, ps []netip.Prefix) error {
	family := "inet"
	if v6 {
		family = "inet6"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "create %s hash:net family %s maxelem 262144 -exist\nflush %s\n", name, family, name)
	for _, p := range ps {
		if p.Addr().Is4() != v6 {
			fmt.Fprintf(&b, "add %s %s\n", name, p)
		}
	}
	cmd := exec.Command(t.ipset, "restore", "-exist")
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ipset: %s", firstLine(string(out)))
	}
	return nil
}

func (t *IPT) Clear() error {
	for _, bin := range []string{t.v4, t.v6} {
		if bin == "" {
			continue
		}
		for exec.Command(bin, "-t", "mangle", "-D", "POSTROUTING", "-j", iptChain).Run() == nil {
		}
		_ = exec.Command(bin, "-t", "mangle", "-F", iptChain).Run()
		_ = exec.Command(bin, "-t", "mangle", "-X", iptChain).Run()
	}
	if t.ipset != "" {
		for _, s := range []string{"cloud4", "cloud6", "game4", "game6"} {
			_ = exec.Command(t.ipset, "destroy", "prosvet_"+s).Run()
		}
	}
	return nil
}

func (t *IPT) Active() bool {
	return exec.Command(t.v4, "-t", "mangle", "-n", "-L", iptChain).Run() == nil
}
