package strategy

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Env tells the composer how to turn symbolic names into real paths.
type Env struct {
	OS   string // "linux" or "windows"
	QNum int    // NFQUEUE number (Linux)
	// Resolve returns the on-disk path and size of a decoy reference,
	// generating the file if necessary.
	Resolve func(ref string) (path string, size int, err error)
	// Hostlist and IPSet return paths of list files by name. Names are the
	// ones used in Group.Hostlists/IPSets plus "exclude", "exclude-user" and
	// "ipset-exclude".
	Hostlist func(name string) string
	IPSet    func(name string) string
}

// PortRule is what the Linux firewall needs to know about a group: which
// packets to hand to the engine.
type PortRule struct {
	Proto   string `json:"proto"`
	Ports   string `json:"ports"`
	Set     string `json:"set,omitempty"` // restrict to an IP set (kernel side)
	Packets int    `json:"packets"`
}

// Plan is a fully rendered engine configuration.
type Plan struct {
	Args      []string          `json:"args"`
	Rules     []PortRule        `json:"rules"`
	Groups    []string          `json:"groups"`
	Selection map[string]string `json:"selection"` // group -> candidate id
}

// Picker chooses the candidate for a group.
type Picker func(g Group) Candidate

// Compose renders the profiles of the given groups into an engine command
// line. Groups whose candidate is None are left out entirely.
func Compose(env Env, groups []Group, pick Picker) (*Plan, error) {
	p := &Plan{Selection: map[string]string{}}
	var profiles [][]string
	var tcp, udp []PortRange
	for _, g := range groups {
		c := pick(g)
		p.Selection[g.ID] = c.ID
		if c.ID == None || strings.TrimSpace(c.Args) == "" {
			continue
		}
		if err := Validate(c.Args); err != nil {
			return nil, fmt.Errorf("%s: %w", g.ID, err)
		}
		prof, err := profile(env, g, c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", g.ID, err)
		}
		profiles = append(profiles, prof)
		p.Groups = append(p.Groups, g.ID)
		ranges, err := ParsePorts(g.Ports)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", g.ID, err)
		}
		if g.Proto == "tcp" {
			tcp = append(tcp, ranges...)
		} else {
			udp = append(udp, ranges...)
		}
		rule := PortRule{Proto: g.Proto, Ports: g.Ports, Packets: QueuePackets(c.Args, g.Packets)}
		if len(g.IPSets) > 0 {
			rule.Set = g.IPSets[0]
		}
		p.Rules = append(p.Rules, rule)
	}
	if len(profiles) == 0 {
		return p, nil
	}
	switch env.OS {
	case "windows":
		if len(tcp) > 0 {
			p.Args = append(p.Args, "--wf-tcp="+FormatPorts(MergePorts(tcp)))
		}
		if len(udp) > 0 {
			p.Args = append(p.Args, "--wf-udp="+FormatPorts(MergePorts(udp)))
		}
	default:
		p.Args = append(p.Args, "--qnum="+strconv.Itoa(env.QNum))
	}
	for i, prof := range profiles {
		if i > 0 {
			p.Args = append(p.Args, "--new")
		}
		p.Args = append(p.Args, prof...)
	}
	p.Rules = mergeRules(p.Rules)
	return p, nil
}

func profile(env Env, g Group, c Candidate) ([]string, error) {
	var a []string
	if g.Proto == "tcp" {
		a = append(a, "--filter-tcp="+g.Ports)
	} else {
		a = append(a, "--filter-udp="+g.Ports)
	}
	if g.L7 != "" {
		a = append(a, "--filter-l7="+g.L7)
	}
	for _, h := range g.Hostlists {
		a = append(a, "--hostlist="+env.Hostlist(h))
	}
	for _, s := range g.IPSets {
		a = append(a, "--ipset="+env.IPSet(s))
	}
	if g.Exclude {
		if len(g.Hostlists) > 0 {
			a = append(a, "--hostlist-exclude="+env.Hostlist("exclude"), "--hostlist-exclude="+env.Hostlist("exclude-user"))
		}
		a = append(a, "--ipset-exclude="+env.IPSet("ipset-exclude"))
	}
	a = append(a, strings.Fields(g.Extra)...)
	resolved, err := Render(env, c.Args)
	if err != nil {
		return nil, err
	}
	return append(a, resolved...), nil
}

// Render resolves decoy references in a symbolic recipe.
func Render(env Env, args string) ([]string, error) {
	var out []string
	for _, f := range strings.Fields(args) {
		name, val, ok := strings.Cut(f, "=")
		if !ok || !strings.HasPrefix(val, "@") {
			out = append(out, f)
			continue
		}
		path, size, err := env.Resolve(val)
		if err != nil {
			return nil, fmt.Errorf("decoy %s: %w", val, err)
		}
		if name == "--dpi-desync-split-seqovl" {
			out = append(out, name+"="+strconv.Itoa(size))
		} else {
			out = append(out, name+"="+path)
		}
	}
	return out, nil
}

// RulesFor returns firewall rules covering every given group, regardless of
// the recipe chosen for it. Used while tuning, when recipes change each round.
func RulesFor(groups []Group) []PortRule {
	var rules []PortRule
	for _, g := range groups {
		r := PortRule{Proto: g.Proto, Ports: g.Ports, Packets: g.Packets}
		if len(g.IPSets) > 0 {
			r.Set = g.IPSets[0]
		}
		rules = append(rules, r)
	}
	return mergeRules(rules)
}

func mergeRules(rules []PortRule) []PortRule {
	type key struct {
		proto, set string
		packets    int
	}
	byKey := map[key][]PortRange{}
	var order []key
	for _, r := range rules {
		k := key{r.Proto, r.Set, r.Packets}
		pr, _ := ParsePorts(r.Ports)
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], pr...)
	}
	out := make([]PortRule, 0, len(order))
	for _, k := range order {
		out = append(out, PortRule{Proto: k.proto, Set: k.set, Packets: k.packets, Ports: FormatPorts(MergePorts(byKey[k]))})
	}
	return out
}

// PortRange is an inclusive port interval.
type PortRange struct{ Lo, Hi int }

// ParsePorts parses "80,443,1024-65535".
func ParsePorts(s string) ([]PortRange, error) {
	var out []PortRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 1 || a > 65535 {
			return nil, fmt.Errorf("bad port %q", part)
		}
		b := a
		if isRange {
			b, err = strconv.Atoi(hi)
			if err != nil || b < a || b > 65535 {
				return nil, fmt.Errorf("bad port range %q", part)
			}
		}
		out = append(out, PortRange{a, b})
	}
	return out, nil
}

// MergePorts sorts and merges overlapping/adjacent ranges.
func MergePorts(in []PortRange) []PortRange {
	r := slices.Clone(in)
	slices.SortFunc(r, func(a, b PortRange) int { return a.Lo - b.Lo })
	var out []PortRange
	for _, x := range r {
		if n := len(out); n > 0 && x.Lo <= out[n-1].Hi+1 {
			if x.Hi > out[n-1].Hi {
				out[n-1].Hi = x.Hi
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// FormatPorts renders ranges in zapret syntax.
func FormatPorts(r []PortRange) string {
	parts := make([]string, len(r))
	for i, x := range r {
		if x.Lo == x.Hi {
			parts[i] = strconv.Itoa(x.Lo)
		} else {
			parts[i] = strconv.Itoa(x.Lo) + "-" + strconv.Itoa(x.Hi)
		}
	}
	return strings.Join(parts, ",")
}
