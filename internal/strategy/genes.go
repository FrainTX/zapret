package strategy

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Recipes are bred like organisms: a recipe is split into genes (how the
// packet is cut, how decoys are disguised, what they carry, how often they
// are repeated…), working recipes are crossed and mutated, and the children
// are tested on the live network. This is how Просвет builds recipes that
// exist in no catalogue — and keeps the ones that work everywhere with the
// fewest extra packets.

// Gene groups. Options of one group always travel together, because they
// only make sense as a set (a seqovl size without its pattern, say).
const (
	geneMode  = "mode"
	geneFool  = "fool"
	geneSplit = "split"
	geneFake  = "fake"
	geneRep   = "rep"
	geneCut   = "cut"
	geneMisc  = "misc"
)

var geneOf = map[string]string{
	"--dpi-desync":                      geneMode,
	"--dpi-desync-fooling":              geneFool,
	"--dpi-desync-badseq-increment":     geneFool,
	"--dpi-desync-autottl":              geneFool,
	"--dpi-desync-ttl":                  geneFool,
	"--dpi-desync-split-pos":            geneSplit,
	"--dpi-desync-split-seqovl":         geneSplit,
	"--dpi-desync-split-seqovl-pattern": geneSplit,
	"--dpi-desync-fakedsplit-pattern":   geneSplit,
	"--dpi-desync-hostfakesplit-mod":    geneSplit,
	"--dpi-desync-ipfrag-pos-udp":       geneSplit,
	"--dpi-desync-udplen-increment":     geneSplit,
	"--dpi-desync-fake-tls":             geneFake,
	"--dpi-desync-fake-tls-mod":         geneFake,
	"--dpi-desync-fake-http":            geneFake,
	"--dpi-desync-fake-quic":            geneFake,
	"--dpi-desync-fake-unknown":         geneFake,
	"--dpi-desync-fake-unknown-udp":     geneFake,
	"--dpi-desync-fake-discord":         geneFake,
	"--dpi-desync-fake-stun":            geneFake,
	"--dpi-desync-repeats":              geneRep,
	"--dpi-desync-cutoff":               geneCut,
	"--dpi-desync-any-protocol":         geneCut,
}

// optOrder gives synthesized recipes a canonical, readable option order.
var optOrder = []string{
	"--dpi-desync", "--dpi-desync-split-pos", "--dpi-desync-split-seqovl", "--dpi-desync-split-seqovl-pattern",
	"--dpi-desync-fakedsplit-pattern", "--dpi-desync-hostfakesplit-mod", "--dpi-desync-ipfrag-pos-udp",
	"--dpi-desync-udplen-increment", "--dpi-desync-fooling", "--dpi-desync-badseq-increment",
	"--dpi-desync-autottl", "--dpi-desync-ttl", "--dpi-desync-repeats", "--dpi-desync-any-protocol",
	"--dpi-desync-cutoff", "--dpi-desync-fake-tls", "--dpi-desync-fake-tls-mod", "--dpi-desync-fake-http",
	"--dpi-desync-fake-quic", "--dpi-desync-fake-unknown", "--dpi-desync-fake-unknown-udp",
	"--dpi-desync-fake-discord", "--dpi-desync-fake-stun",
}

type opt struct{ name, val string }

// Genome is a recipe taken apart.
type Genome struct {
	Kind  Kind
	opts  []opt
	other []string // options outside the gene table (kept as-is, "misc")
}

// ParseGenome splits a symbolic recipe into genes.
func ParseGenome(k Kind, args string) Genome {
	g := Genome{Kind: k}
	for _, f := range strings.Fields(args) {
		name, val, _ := strings.Cut(f, "=")
		if _, ok := geneOf[name]; ok {
			g.opts = append(g.opts, opt{name, val})
		} else {
			g.other = append(g.other, f)
		}
	}
	return g
}

func (g Genome) clone() Genome {
	return Genome{Kind: g.Kind, opts: slices.Clone(g.opts), other: slices.Clone(g.other)}
}

func (g Genome) get(name string) (string, bool) {
	for _, o := range g.opts {
		if o.name == name {
			return o.val, true
		}
	}
	return "", false
}

func (g *Genome) set(name, val string) {
	g.del(name)
	g.opts = append(g.opts, opt{name, val})
}

func (g *Genome) del(names ...string) {
	g.opts = slices.DeleteFunc(g.opts, func(o opt) bool { return slices.Contains(names, o.name) })
}

func (g *Genome) delGene(gene string) {
	g.opts = slices.DeleteFunc(g.opts, func(o opt) bool { return geneOf[o.name] == gene })
}

func (g Genome) gene(gene string) []opt {
	var out []opt
	for _, o := range g.opts {
		if geneOf[o.name] == gene {
			out = append(out, o)
		}
	}
	return out
}

// Modes returns the --dpi-desync modes.
func (g Genome) Modes() []string {
	v, _ := g.get("--dpi-desync")
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// Args renders the genome in canonical order.
func (g Genome) Args() string {
	rank := func(name string) int {
		if i := slices.Index(optOrder, name); i >= 0 {
			return i
		}
		return len(optOrder)
	}
	opts := slices.Clone(g.opts)
	sort.SliceStable(opts, func(i, j int) bool { return rank(opts[i].name) < rank(opts[j].name) })
	var b []string
	for _, o := range opts {
		b = append(b, o.name+"="+o.val)
	}
	return strings.Join(append(b, g.other...), " ")
}

var (
	tcpFirst  = []string{"fake"}
	tcpSecond = []string{"multisplit", "multidisorder", "fakedsplit", "fakeddisorder", "hostfakesplit"}
	splitPos  = []string{"1", "2", "1,midsld", "2,midsld", "midsld", "sniext+1", "2,sniext+1", "1,sniext+1", "host+1", "1,host+2", "3"}
	repeats   = []int{1, 2, 3, 4, 6, 8, 11}
	foolings  = []string{"ts", "badseq", "md5sig", "datanoack", "autottl"}
	badseqInc = []string{"2", "1000", "10000000"}
	cutoffs   = []string{"n2", "n3", "n4"}
)

func isUDP(k Kind) bool { return k == KindQUIC || k == KindVoice || k == KindGameUDP }

// sniOf finds the decoy hostname a recipe uses.
func (g Genome) sniOf() string {
	for _, o := range g.opts {
		for _, p := range []string{"@tls:", "@quic:", "@http:"} {
			if strings.HasPrefix(o.val, p) {
				return o.val[len(p):]
			}
		}
		for _, part := range strings.Split(o.val, ",") {
			if h, ok := strings.CutPrefix(part, "sni="); ok {
				return h
			}
			if h, ok := strings.CutPrefix(part, "host="); ok {
				return h
			}
		}
	}
	return ""
}

// withSNI replaces the decoy hostname everywhere in the genome.
func (g *Genome) withSNI(host string) {
	old := g.sniOf()
	if old == "" || !validSNI(host) {
		return
	}
	for i, o := range g.opts {
		g.opts[i].val = strings.ReplaceAll(o.val, old, host)
	}
}

func sniPool(k Kind) []string {
	switch k {
	case KindQUIC, KindVoice:
		return QUICSNIPool
	case KindGameUDP, KindGameTCP:
		return GameSNIPool
	}
	return TLSSNIPool
}

// Cross takes each gene group from one of the two parents.
func Cross(a, b Genome, r *rand.Rand) Genome {
	child := a.clone()
	for _, gene := range []string{geneMode, geneFool, geneSplit, geneFake, geneRep, geneCut} {
		if r.IntN(2) == 0 {
			continue
		}
		child.delGene(gene)
		child.opts = append(child.opts, b.gene(gene)...)
	}
	if sni := a.sniOf(); sni != "" && r.IntN(2) == 0 {
		child.withSNI(sni)
	} else if sni := b.sniOf(); sni != "" {
		child.withSNI(sni)
	}
	return child
}

// Mutate changes one random gene.
func Mutate(g Genome, r *rand.Rand) Genome {
	g = g.clone()
	sni := g.sniOf()
	if sni == "" {
		sni = sniPool(g.Kind)[0]
	}
	pick := func(xs []string) string { return xs[r.IntN(len(xs))] }
	udp := isUDP(g.Kind)
	switch r.IntN(7) {
	case 0: // repeats: fewer is cheaper, more is sturdier
		g.set("--dpi-desync-repeats", strconv.Itoa(repeats[r.IntN(len(repeats))]))
	case 1: // decoy hostname
		g.withSNI(pick(sniPool(g.Kind)))
	case 2: // how decoys are kept from the server
		if udp {
			if g.Kind == KindGameUDP {
				g.set("--dpi-desync-cutoff", pick(cutoffs))
			} else {
				g.set("--dpi-desync-repeats", strconv.Itoa(repeats[r.IntN(len(repeats))]))
			}
			break
		}
		g.ensureMode(r, true, false)
		g.delGene(geneFool)
		switch f := pick(foolings); f {
		case "autottl":
			g.set("--dpi-desync-autottl", "2")
		case "badseq":
			g.set("--dpi-desync-fooling", "badseq")
			g.set("--dpi-desync-badseq-increment", pick(badseqInc))
		default:
			g.set("--dpi-desync-fooling", f)
		}
	case 3: // where the packet is cut
		if udp {
			if slices.Contains(g.Modes(), "udplen") {
				g.set("--dpi-desync-udplen-increment", pick([]string{"2", "4", "8"}))
			}
			break
		}
		g.ensureMode(r, false, true)
		g.set("--dpi-desync-split-pos", pick(splitPos))
	case 4: // the cutting technique itself
		if udp {
			modes := []string{"fake", "fake,udplen"}
			if g.Kind == KindQUIC {
				modes = append(modes, "ipfrag2", "fake,ipfrag2")
			}
			g.set("--dpi-desync", pick(modes))
			break
		}
		var ms []string
		if r.IntN(3) > 0 {
			ms = append(ms, pick(tcpFirst))
		}
		if len(ms) == 0 || r.IntN(4) > 0 {
			ms = append(ms, pick(tcpSecond))
		}
		g.set("--dpi-desync", strings.Join(ms, ","))
	case 5: // overlap the first segment with a decoy
		if udp {
			g.set("--dpi-desync-repeats", strconv.Itoa(repeats[r.IntN(len(repeats))]))
			break
		}
		if _, ok := g.get("--dpi-desync-split-seqovl"); ok {
			g.del("--dpi-desync-split-seqovl", "--dpi-desync-split-seqovl-pattern")
		} else {
			g.set("--dpi-desync-split-seqovl", "@tls:"+sni)
			g.set("--dpi-desync-split-seqovl-pattern", "@tls:"+sni)
		}
	case 6: // randomise the decoy ClientHello
		if udp {
			g.withSNI(pick(sniPool(g.Kind)))
			break
		}
		if _, ok := g.get("--dpi-desync-fake-tls-mod"); ok {
			g.del("--dpi-desync-fake-tls-mod")
		} else {
			g.set("--dpi-desync-fake-tls-mod", "rnd,dupsid,sni="+sni)
		}
	}
	return g
}

// ensureMode adds a decoy-sending or a cutting mode when a mutation of the
// matching gene would otherwise be dropped by Repair as ineffective.
func (g *Genome) ensureMode(r *rand.Rand, fake, split bool) {
	ms := g.Modes()
	hasFake := slices.ContainsFunc(ms, func(m string) bool {
		return m == "fake" || m == "fakedsplit" || m == "fakeddisorder" || m == "hostfakesplit"
	})
	hasSplit := slices.ContainsFunc(ms, func(m string) bool {
		return m == "multisplit" || m == "multidisorder" || m == "fakedsplit" || m == "fakeddisorder"
	})
	if fake && !hasFake {
		ms = append([]string{"fake"}, slices.DeleteFunc(ms, func(m string) bool { return m == "syndata" })...)
	}
	if split && !hasSplit {
		second := []string{"multisplit", "multidisorder", "fakedsplit"}[r.IntN(3)]
		ms = append(slices.DeleteFunc(ms, func(m string) bool { return m == "hostfakesplit" }), second)
	}
	if len(ms) > 0 {
		g.set("--dpi-desync", strings.Join(ms, ","))
	}
}

// Repair makes a genome consistent: options that the chosen modes ignore
// are dropped, the ones they need are added. It returns false when the
// genome cannot be made into a sensible recipe.
func Repair(g Genome) (Genome, bool) {
	g = g.clone()
	modes := g.Modes()
	if len(modes) == 0 {
		return g, false
	}
	has := func(ms ...string) bool {
		for _, m := range ms {
			if slices.Contains(modes, m) {
				return true
			}
		}
		return false
	}
	sni := g.sniOf()
	if sni == "" {
		sni = sniPool(g.Kind)[0]
	}
	udp := isUDP(g.Kind)
	if udp {
		if has(tcpSecond...) || has("syndata") {
			return g, false
		}
		g.del("--dpi-desync-fooling", "--dpi-desync-badseq-increment", "--dpi-desync-autottl", "--dpi-desync-split-pos",
			"--dpi-desync-split-seqovl", "--dpi-desync-split-seqovl-pattern", "--dpi-desync-fake-tls", "--dpi-desync-fake-tls-mod",
			"--dpi-desync-fake-http", "--dpi-desync-fake-unknown")
		if !has("udplen") {
			g.del("--dpi-desync-udplen-increment")
		}
		if !has("ipfrag2") {
			g.del("--dpi-desync-ipfrag-pos-udp")
		}
		if has("fake") {
			switch g.Kind {
			case KindQUIC:
				if _, ok := g.get("--dpi-desync-fake-quic"); !ok {
					g.set("--dpi-desync-fake-quic", "@quic:"+sni)
				}
			case KindGameUDP:
				if _, ok := g.get("--dpi-desync-fake-unknown-udp"); !ok {
					g.set("--dpi-desync-fake-unknown-udp", "@quic:"+sni)
				}
			}
		} else {
			g.delGene(geneFake)
			g.del("--dpi-desync-repeats")
		}
		if g.Kind == KindGameUDP {
			// Game flows never end: without a cutoff every packet of a match
			// would be preceded by decoys.
			g.set("--dpi-desync-any-protocol", "1")
			if _, ok := g.get("--dpi-desync-cutoff"); !ok {
				g.set("--dpi-desync-cutoff", "n2")
			}
		}
	} else {
		fakes := has("fake", "fakedsplit", "fakeddisorder", "hostfakesplit")
		splits := has("multisplit", "multidisorder", "fakedsplit", "fakeddisorder")
		if !fakes {
			g.delGene(geneFool)
			g.delGene(geneFake)
			g.del("--dpi-desync-repeats")
		} else if len(g.gene(geneFool)) == 0 {
			g.set("--dpi-desync-fooling", "ts")
		}
		if !has("fake") {
			g.del("--dpi-desync-fake-tls", "--dpi-desync-fake-tls-mod", "--dpi-desync-fake-http", "--dpi-desync-fake-unknown")
		} else if _, ok := g.get("--dpi-desync-fake-tls"); !ok {
			if _, mod := g.get("--dpi-desync-fake-tls-mod"); !mod {
				g.set("--dpi-desync-fake-tls", "@tls:"+sni)
			}
		}
		if !splits {
			g.del("--dpi-desync-split-pos", "--dpi-desync-split-seqovl", "--dpi-desync-split-seqovl-pattern")
		} else if _, ok := g.get("--dpi-desync-split-pos"); !ok {
			g.set("--dpi-desync-split-pos", "1")
		}
		if !has("multisplit") {
			// zapret only honours seqovl for multisplit (and disorder with a
			// position past the overlap, which we do not try to compute).
			g.del("--dpi-desync-split-seqovl", "--dpi-desync-split-seqovl-pattern")
		}
		if _, ok := g.get("--dpi-desync-split-seqovl"); ok {
			if _, ok := g.get("--dpi-desync-split-seqovl-pattern"); !ok {
				g.set("--dpi-desync-split-seqovl-pattern", "@tls:"+sni)
			}
		}
		if !has("fakedsplit", "fakeddisorder") {
			g.del("--dpi-desync-fakedsplit-pattern")
		}
		if !has("hostfakesplit") {
			g.del("--dpi-desync-hostfakesplit-mod")
		} else if _, ok := g.get("--dpi-desync-hostfakesplit-mod"); !ok {
			g.set("--dpi-desync-hostfakesplit-mod", "host="+sni)
		}
		if v, _ := g.get("--dpi-desync-fooling"); v != "badseq" {
			g.del("--dpi-desync-badseq-increment")
		}
		if g.Kind == KindGameTCP {
			g.set("--dpi-desync-any-protocol", "1")
			if _, ok := g.get("--dpi-desync-cutoff"); !ok {
				g.set("--dpi-desync-cutoff", "n3")
			}
		}
	}
	if Validate(g.Args()) != nil {
		return g, false
	}
	return g, true
}

// ---- registry of bred recipes --------------------------------------------

// Bred is a synthesized recipe, persisted in the config.
type Bred struct {
	Kind    Kind     `json:"kind"`
	Args    string   `json:"args"`
	Parents []string `json:"parents,omitempty"`
	Gen     int      `json:"gen,omitempty"`
}

var (
	bredMu sync.RWMutex
	bred   = map[string]Bred{}
)

// BredID is the stable id of a synthesized recipe: a hash of what it does.
func BredID(k Kind, args string) string {
	h := sha256.Sum256([]byte(string(k) + "\x00" + args))
	return "syn-" + hex.EncodeToString(h[:4])
}

// Register makes a synthesized recipe known (ParseCandidateID, Candidates).
// It returns its candidate, or false if the recipe is not acceptable.
func Register(b Bred) (Candidate, bool) {
	if Validate(b.Args) != nil || strings.TrimSpace(b.Args) == "" {
		return Candidate{}, false
	}
	id := BredID(b.Kind, b.Args)
	bredMu.Lock()
	bred[id] = b
	bredMu.Unlock()
	return bredCandidate(id, b), true
}

// BredRecipes returns every registered synthesized recipe by id.
func BredRecipes() map[string]Bred {
	bredMu.RLock()
	defer bredMu.RUnlock()
	out := make(map[string]Bred, len(bred))
	for k, v := range bred {
		out[k] = v
	}
	return out
}

func lookupBred(id string) (Candidate, bool) {
	bredMu.RLock()
	b, ok := bred[id]
	bredMu.RUnlock()
	if !ok {
		return Candidate{}, false
	}
	return bredCandidate(id, b), true
}

func bredCandidate(id string, b Bred) Candidate {
	g := ParseGenome(b.Kind, b.Args)
	return Candidate{ID: id, Base: id, Label: "выведен Просветом: " + strings.Join(Tags(b.Args), " · "), SNI: g.sniOf(), Args: b.Args}
}

// bredFor lists registered recipes of a kind, in stable order.
func bredFor(k Kind) []Candidate {
	bredMu.RLock()
	defer bredMu.RUnlock()
	var ids []string
	for id, b := range bred {
		if b.Kind == k {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, bredCandidate(id, bred[id]))
	}
	return out
}

// IsBred reports whether a candidate id is a synthesized recipe.
func IsBred(id string) bool { return strings.HasPrefix(id, "syn-") }
