package tuner

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"prosvet/internal/probe"
	"prosvet/internal/strategy"
)

type roundFunc func(stage int, cand map[*groupState]strategy.Candidate) error
type nextFunc func(st *groupState) (strategy.Candidate, bool)

// ranked returns a group's trials best first (no baseline).
func (st *groupState) ranked() []Trial {
	var r []Trial
	for _, t := range st.trials {
		if t.Candidate != strategy.None {
			r = append(r, t)
		}
	}
	slices.SortStableFunc(r, func(a, b Trial) int {
		if better(st.g, a, b) {
			return -1
		}
		if better(st.g, b, a) {
			return 1
		}
		return 0
	})
	return r
}

// worthBreeding: groups that work untouched, or are blocked by IP (no
// packet trick can help), are left alone.
func (st *groupState) worthBreeding() bool {
	if st.best == nil {
		return false
	}
	if st.best.Candidate == strategy.None && st.best.Ratio() == 1 && !st.g.PreferActive {
		return false
	}
	if st.best.OK == 0 {
		switch dominant(st.best.Classes) {
		case probe.ClassIP, probe.ClassDNS:
			return false
		}
	}
	return true
}

// breed runs o.Synth generations. Each generation, every group gets
// o.Brood children of its best recipes — crossed, mutated, repaired and
// validated — and tests them, one child per group per engine run.
func breed(ctx context.Context, o Options, states []*groupState, round roundFunc, next nextFunc, emit func(Event), rep *Report) error {
	seed := o.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	r := rand.New(rand.NewPCG(seed, seed>>17|1))
	brood := o.Brood
	if brood <= 0 {
		brood = 5
	}
	emit(Event{Type: "stage", Stage: 3, Message: "вывожу новые рецепты: скрещиваю и мутирую лучшие"})
	for gen := 1; gen <= o.Synth; gen++ {
		any := false
		for _, st := range states {
			st.queue = nil
			st.done = true
			if !st.worthBreeding() {
				continue
			}
			// In a quick run, a group that already works fully is only
			// refined when it is a game: there every millisecond counts.
			game := st.g.Kind == strategy.KindGameUDP || st.g.Kind == strategy.KindGameTCP
			if !o.Deep && !game && st.best.Ratio() == 1 && st.best.Candidate != strategy.None {
				continue
			}
			parents := st.ranked()
			working := parents[:0:0]
			for _, t := range parents {
				if t.OK > 0 {
					working = append(working, t)
				}
			}
			lost := len(working) == 0
			if !lost {
				parents = working
			} else {
				// Nothing works yet: start from a spread of techniques (one
				// recipe per fooling family / cutting mode) rather than the
				// first few in the list, and mutate harder.
				parents = diverse(parents)
			}
			if len(parents) > 4 && !lost {
				parents = parents[:4]
			}
			if len(parents) == 0 {
				continue
			}
			genomes := make([]strategy.Genome, 0, len(parents))
			for _, p := range parents {
				if c, ok := strategy.ParseCandidateID(p.Candidate); ok {
					genomes = append(genomes, strategy.ParseGenome(st.g.Kind, c.Args))
				}
			}
			if len(genomes) == 0 {
				continue
			}
			for tries := 0; len(st.queue) < brood && tries < brood*40; tries++ {
				a := genomes[r.IntN(len(genomes))]
				child := a
				// Mostly cross the best two; always mutate at least once so
				// a lone parent still explores.
				if len(genomes) > 1 && r.IntN(3) > 0 {
					child = strategy.Cross(a, genomes[r.IntN(len(genomes))], r)
				}
				child = strategy.Mutate(child, r)
				if r.IntN(3) == 0 || lost {
					child = strategy.Mutate(child, r)
				}
				fixed, ok := strategy.Repair(child)
				if !ok {
					continue
				}
				args := fixed.Args()
				id := strategy.BredID(st.g.Kind, args)
				if st.tried[id] || slices.ContainsFunc(st.queue, func(c strategy.Candidate) bool { return c.ID == id }) {
					continue
				}
				if fool := foolingOf(args); fool != "" && st.leaky[fool] {
					continue
				}
				var ps []string
				for _, p := range parents {
					ps = append(ps, p.Candidate)
				}
				c, ok := strategy.Register(strategy.Bred{Kind: st.g.Kind, Args: args, Parents: ps, Gen: gen})
				if ok {
					st.queue = append(st.queue, c)
				}
			}
			if len(st.queue) > 0 {
				st.done = false
				any = true
			}
		}
		if !any {
			break
		}
		emit(Event{Type: "generation", Stage: 3, Round: gen, Rounds: o.Synth, Message: fmt.Sprintf("поколение %d из %d", gen, o.Synth)})
		for {
			cand := map[*groupState]strategy.Candidate{}
			for _, st := range states {
				if st.done {
					continue
				}
				if c, ok := next(st); ok {
					cand[st] = c
				} else {
					st.done = true
				}
			}
			if len(cand) == 0 {
				break
			}
			emit(Event{Type: "round", Stage: 3, Round: rep.Rounds + 1})
			if err := round(3, cand); err != nil {
				return err
			}
		}
	}
	return nil
}

// universal looks for a single recipe that serves every TLS group needing
// a bypass as well as each group's own best does. Candidates are the
// groups' winners and crosses of them; each is tried on all groups at once.
func universal(ctx context.Context, o Options, states []*groupState, round roundFunc, emit func(Event), rep *Report) (string, []string, error) {
	var tls []*groupState
	for _, st := range states {
		if st.g.Kind == strategy.KindTLS && st.worthBreeding() && st.best != nil && st.best.OK > 0 && st.best.Candidate != strategy.None {
			tls = append(tls, st)
		}
	}
	if len(tls) < 2 {
		return "", nil, nil
	}
	seed := o.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	r := rand.New(rand.NewPCG(seed^0x9e3779b97f4a7c15, 3))
	var pool []strategy.Candidate
	seen := map[string]bool{}
	add := func(c strategy.Candidate) {
		if c.ID != "" && !seen[c.ID] {
			seen[c.ID] = true
			pool = append(pool, c)
		}
	}
	var genomes []strategy.Genome
	for _, st := range tls {
		if c, ok := strategy.ParseCandidateID(st.best.Candidate); ok {
			add(c)
			genomes = append(genomes, strategy.ParseGenome(strategy.KindTLS, c.Args))
		}
	}
	for tries := 0; len(pool) < len(tls)+3 && tries < 200 && len(genomes) > 1; tries++ {
		child, ok := strategy.Repair(strategy.Cross(genomes[r.IntN(len(genomes))], genomes[r.IntN(len(genomes))], r))
		if !ok {
			continue
		}
		if c, ok := strategy.Register(strategy.Bred{Kind: strategy.KindTLS, Args: child.Args(), Gen: o.Synth + 1}); ok {
			add(c)
		}
	}
	emit(Event{Type: "stage", Stage: 4, Message: "ищу один рецепт, который работает везде"})

	type score struct {
		c       strategy.Candidate
		minR    float64
		latency int
		cost    int
		ok      bool
	}
	var best *score
	for _, c := range pool {
		cand := map[*groupState]strategy.Candidate{}
		for _, st := range tls {
			cand[st] = c
		}
		before := map[*groupState]Trial{}
		for _, st := range tls {
			before[st] = *st.best
		}
		emit(Event{Type: "round", Stage: 4, Round: rep.Rounds + 1})
		if err := round(4, cand); err != nil {
			return "", nil, err
		}
		s := score{c: c, minR: 1, cost: strategy.Cost(c.Args), ok: true}
		for _, st := range tls {
			t := st.trials[len(st.trials)-1]
			s.minR = min(s.minR, t.Ratio())
			s.latency += t.Latency
			// It must be as good as the group's own champion.
			if better(st.g, before[st], t) {
				s.ok = false
			}
		}
		if !s.ok {
			continue
		}
		if best == nil || s.minR > best.minR || s.minR == best.minR && (s.latency < best.latency-5*len(tls) || s.latency <= best.latency+5*len(tls) && s.cost < best.cost) {
			cp := s
			best = &cp
		}
	}
	if best == nil {
		return "", nil, nil
	}
	var ids []string
	for _, st := range tls {
		ids = append(ids, st.g.ID)
	}
	emit(Event{Type: "universal", Stage: 4, Candidate: best.c.ID, Label: best.c.Label, Tags: strategy.Tags(best.c.Args), Message: fmt.Sprintf("один рецепт для %d групп", len(ids))})
	return best.c.ID, ids, nil
}

// diverse keeps one trial per technique family.
func diverse(ts []Trial) []Trial {
	seen := map[string]bool{}
	var out []Trial
	for _, t := range ts {
		c, ok := strategy.ParseCandidateID(t.Candidate)
		if !ok {
			continue
		}
		key := foolingOf(c.Args) + "|" + strings.Join(strategy.ParseGenome("", c.Args).Modes(), ",")
		if !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	return out
}
