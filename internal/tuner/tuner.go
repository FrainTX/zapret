// Package tuner finds, per traffic group, the least invasive recipe that
// makes the group's services reachable on the current network.
//
// Unlike a fixed list of all-in-one presets, groups are tuned independently
// but in the same engine run: in round i every group tries its i-th
// candidate, each one confined to its own hostlist/ipset profile. N rounds
// therefore explore N candidates for every group at once, and the final
// configuration combines the best recipe of each.
package tuner

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"prosvet/internal/probe"
	"prosvet/internal/strategy"
)

// Runner applies a selection to the live engine.
type Runner interface {
	Apply(ctx context.Context, sel map[string]strategy.Candidate) error
}

// Options configure a tuning session.
type Options struct {
	// Groups to tune. Untestable groups are ignored.
	Groups []strategy.Group
	// Fixed are other active groups whose current recipe must be kept.
	Fixed map[string]strategy.Candidate
	// Deep explores decoy hostname variants even when a working recipe was
	// found in the first stage.
	Deep bool
	// Custom domains for the "sites" probes.
	Custom []string
	// Synth is the number of breeding generations (0 = no synthesis).
	Synth int
	// Brood is how many children each group tests per generation.
	Brood int
	// Seed makes breeding reproducible in tests (0 = random).
	Seed uint64
	// Probe timeouts.
	Probe probe.Options
	// TopK templates get decoy-hostname variants in stage two.
	TopK int
	// Verify is the number of confirmation passes for the final pick.
	Verify int
}

// Trial is one candidate's outcome for one group.
type Trial struct {
	Candidate string         `json:"candidate"`
	Base      string         `json:"base"`
	Label     string         `json:"label"`
	Tags      []string       `json:"tags"`
	OK        int            `json:"ok"`
	Total     int            `json:"total"`
	Latency   int            `json:"latency"` // median ms of successful probes
	Cost      int            `json:"cost"`    // extra packets per flow (strategy.Cost)
	Classes   map[string]int `json:"classes"`
	Stage     int            `json:"stage"`
}

// Ratio is the success share.
func (t Trial) Ratio() float64 {
	if t.Total == 0 {
		return 0
	}
	return float64(t.OK) / float64(t.Total)
}

// GroupReport summarises the tuning of one group.
type GroupReport struct {
	Group    string   `json:"group"`
	Title    string   `json:"title"`
	Pick     string   `json:"pick"`
	PickTags []string `json:"pickTags"`
	Baseline Trial    `json:"baseline"`
	Best     Trial    `json:"best"`
	Trials   []Trial  `json:"trials"`
	Verified bool     `json:"verified"`
	// Blocking is the dominant block class without bypass.
	Blocking string `json:"blocking"`
	Note     string `json:"note,omitempty"`
}

// Report is the result of a session.
type Report struct {
	Started  time.Time           `json:"started"`
	Finished time.Time           `json:"finished"`
	Rounds   int                 `json:"rounds"`
	Deep     bool                `json:"deep"`
	Groups   []GroupReport       `json:"groups"`
	Picks    map[string]string   `json:"picks"`
	Leaky    map[string][]string `json:"leaky,omitempty"` // group -> fooling methods that leaked
	// Bred are the synthesized recipes worth keeping (picked somewhere or
	// universal), by id.
	Bred map[string]strategy.Bred `json:"bred,omitempty"`
	// Universal is one recipe that works for every TLS group at once.
	Universal       string   `json:"universal,omitempty"`
	UniversalGroups []string `json:"universalGroups,omitempty"`
}

// Event reports progress to the UI.
type Event struct {
	Type      string         `json:"type"` // stage, round, trial, verify, done
	Stage     int            `json:"stage,omitempty"`
	Round     int            `json:"round,omitempty"`
	Rounds    int            `json:"rounds,omitempty"`
	Group     string         `json:"group,omitempty"`
	Candidate string         `json:"candidate,omitempty"`
	Label     string         `json:"label,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	OK        int            `json:"ok,omitempty"`
	Total     int            `json:"total,omitempty"`
	Latency   int            `json:"latency,omitempty"`
	Classes   map[string]int `json:"classes,omitempty"`
	Best      string         `json:"best,omitempty"`
	Message   string         `json:"message,omitempty"`
}

// runProbes is swapped out in tests.
var runProbes = probe.RunAll

type groupState struct {
	g       strategy.Group
	targets []probe.Target
	queue   []strategy.Candidate
	tried   map[string]bool
	trials  []Trial
	best    *Trial
	done    bool
	leaky   map[string]bool
}

func (s *groupState) consider(t Trial) {
	s.trials = append(s.trials, t)
	if s.best == nil || better(s.g, t, *s.best) {
		cp := t
		s.best = &cp
	}
}

// better decides whether a beats b for group g.
func better(g strategy.Group, a, b Trial) bool {
	ra, rb := a.Ratio(), b.Ratio()
	if ra != rb {
		return ra > rb
	}
	if ra == 0 {
		return false
	}
	aNone, bNone := a.Candidate == strategy.None, b.Candidate == strategy.None
	if aNone != bNone {
		// Equal success: do nothing unless the group asks for an active
		// recipe (game UDP, where our probes cannot see the real servers).
		if g.PreferActive {
			return !aNone
		}
		return aNone
	}
	if g.Kind == strategy.KindGameUDP || g.Kind == strategy.KindGameTCP {
		// Games: every millisecond counts. Lower ping wins at a 3 ms margin;
		// within it the recipe that injects fewer packets wins.
		if a.Latency > 0 && b.Latency > 0 && abs(a.Latency-b.Latency) > 3 {
			return a.Latency < b.Latency
		}
		return a.Cost < b.Cost
	}
	// Lower latency wins if clearly lower (15% and 20 ms margin).
	if a.Latency > 0 && b.Latency > 0 {
		if float64(a.Latency) < float64(b.Latency)*0.85 && b.Latency-a.Latency > 20 {
			return true
		}
	}
	return false // earlier (simpler) candidate keeps the lead
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	sort.Ints(xs)
	return xs[len(xs)/2]
}

func summarize(c strategy.Candidate, rs []probe.Result, stage int) Trial {
	t := Trial{Candidate: c.ID, Base: c.Base, Label: c.Label, Tags: strategy.Tags(c.Args), Cost: strategy.Cost(c.Args), Total: len(rs), Classes: map[string]int{}, Stage: stage}
	var lat []int
	for _, r := range rs {
		t.Classes[r.Class]++
		if r.OK {
			t.OK++
			lat = append(lat, int(r.Latency.Milliseconds()))
		}
	}
	t.Latency = median(lat)
	return t
}

// foolingOf extracts the fooling method of fake-based recipes ("ts",
// "badseq", "md5sig", "autottl"), or "" if the recipe sends no decoys that
// rely on being dropped before the server.
func foolingOf(args string) string {
	if !strings.Contains(args, "--dpi-desync=fake") && !strings.Contains(args, ",fake") && !strings.Contains(args, "hostfakesplit") && !strings.Contains(args, "fakedsplit") {
		return ""
	}
	for _, f := range strings.Fields(args) {
		if v, ok := strings.CutPrefix(f, "--dpi-desync-fooling="); ok {
			return v
		}
		if strings.HasPrefix(f, "--dpi-desync-autottl") {
			return "autottl"
		}
	}
	return "none"
}

// Run executes a tuning session.
func Run(ctx context.Context, rn Runner, o Options, emit func(Event)) (*Report, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	if o.TopK == 0 {
		o.TopK = 3
	}
	if o.Verify == 0 {
		o.Verify = 2
	}
	if o.Probe.Timeout == 0 {
		o.Probe = probe.Options{Timeout: 5 * time.Second, Connect: 2500 * time.Millisecond}
	}
	rep := &Report{Started: time.Now(), Deep: o.Deep, Picks: map[string]string{}, Leaky: map[string][]string{}}

	var states []*groupState
	for _, g := range o.Groups {
		if !g.Testable {
			continue
		}
		ts := probe.Targets(g.ID, o.Custom)
		if len(ts) == 0 {
			continue
		}
		st := &groupState{g: g, targets: ts, tried: map[string]bool{}, leaky: map[string]bool{}}
		st.queue = strategy.Candidates(g.Kind) // starts with None
		states = append(states, st)
	}
	if len(states) == 0 {
		return nil, fmt.Errorf("tuner: нет групп, которые можно проверить")
	}

	selection := func(pick func(*groupState) strategy.Candidate) map[string]strategy.Candidate {
		sel := map[string]strategy.Candidate{}
		for k, v := range o.Fixed {
			sel[k] = v
		}
		for _, st := range states {
			sel[st.g.ID] = pick(st)
		}
		return sel
	}

	// round runs one engine configuration and records the trials.
	round := func(stage int, cand map[*groupState]strategy.Candidate) error {
		rep.Rounds++
		sel := selection(func(st *groupState) strategy.Candidate {
			if c, ok := cand[st]; ok {
				return c
			}
			if st.best != nil {
				// Keep groups that are not being tested on their best so far
				// (or nothing): it does not influence other groups' results.
				if c, ok := strategy.ParseCandidateID(st.best.Candidate); ok {
					return c
				}
			}
			return strategy.NoneCandidate()
		})
		if err := rn.Apply(ctx, sel); err != nil {
			return err
		}
		var targets []probe.Target
		for st := range cand {
			targets = append(targets, st.targets...)
		}
		results := runProbes(ctx, targets, o.Probe, 16)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		byGroup := map[string][]probe.Result{}
		for _, r := range results {
			byGroup[r.Group] = append(byGroup[r.Group], r)
		}
		for st, c := range cand {
			t := summarize(c, byGroup[st.g.ID], stage)
			st.tried[c.ID] = true
			st.consider(t)
			// A decoy that reaches the server breaks every recipe relying on
			// the same fooling method; skip them for this group.
			if fool := foolingOf(c.Args); fool != "" && t.Classes[probe.ClassLeak]*2 >= t.Total && t.Total > 0 {
				if !st.leaky[fool] {
					st.leaky[fool] = true
					rep.Leaky[st.g.ID] = append(rep.Leaky[st.g.ID], fool)
				}
			}
			emit(Event{Type: "trial", Stage: stage, Round: rep.Rounds, Group: st.g.ID, Candidate: c.ID, Label: c.Label,
				Tags: t.Tags, OK: t.OK, Total: t.Total, Latency: t.Latency, Classes: t.Classes, Best: st.best.Candidate})
		}
		return nil
	}

	next := func(st *groupState) (strategy.Candidate, bool) {
		for len(st.queue) > 0 {
			c := st.queue[0]
			st.queue = st.queue[1:]
			if st.tried[c.ID] {
				continue
			}
			if fool := foolingOf(c.Args); fool != "" && st.leaky[fool] {
				continue
			}
			return c, true
		}
		return strategy.Candidate{}, false
	}

	perfect := func(st *groupState) bool {
		return st.best != nil && st.best.Ratio() == 1 && !(st.g.PreferActive && st.best.Candidate == strategy.None)
	}

	// ---- Stage 1: baseline + every template with its default decoy ----
	emit(Event{Type: "stage", Stage: 1, Message: "проверяю без обхода и перебираю рецепты"})
	stage1Max := 0
	for _, st := range states {
		stage1Max = max(stage1Max, len(st.queue))
	}
	for {
		cand := map[*groupState]strategy.Candidate{}
		for _, st := range states {
			if st.done {
				continue
			}
			if !o.Deep && perfect(st) {
				st.done = true
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
		emit(Event{Type: "round", Stage: 1, Round: rep.Rounds + 1, Rounds: stage1Max})
		if err := round(1, cand); err != nil {
			return nil, err
		}
	}

	// ---- Stage 2: decoy hostname variants of the best templates ----
	var stage2 bool
	for _, st := range states {
		if st.best != nil && st.best.Candidate == strategy.None && st.best.Ratio() == 1 {
			continue // works without bypass
		}
		if !o.Deep && perfect(st) {
			continue
		}
		// Rank distinct templates by their best trial.
		bestBy := map[string]Trial{}
		for _, t := range st.trials {
			if t.Candidate == strategy.None {
				continue
			}
			if cur, ok := bestBy[t.Base]; !ok || better(st.g, t, cur) {
				bestBy[t.Base] = t
			}
		}
		var ranked []Trial
		for _, t := range bestBy {
			if t.OK > 0 || o.Deep {
				ranked = append(ranked, t)
			}
		}
		slices.SortStableFunc(ranked, func(a, b Trial) int {
			if better(st.g, a, b) {
				return -1
			}
			if better(st.g, b, a) {
				return 1
			}
			return strings.Compare(a.Base, b.Base)
		})
		if len(ranked) == 0 {
			// Nothing worked at all. Decoy hostnames are the main remaining
			// lever, so try one representative of every fooling family that
			// has not leaked (plain seqovl patterns count as a family too).
			families := map[string]bool{}
			for _, tpl := range strategy.Templates {
				if tpl.Kind != st.g.Kind || !tpl.UsesSNI() {
					continue
				}
				fam := foolingOf(tpl.Args)
				if fam == "" {
					fam = "pattern"
				}
				if st.leaky[fam] || families[fam] {
					continue
				}
				families[fam] = true
				ranked = append(ranked, Trial{Base: tpl.ID})
			}
		}
		topK := o.TopK
		if len(bestBy) == 0 || ranked[0].OK == 0 {
			topK = len(ranked)
		}
		st.queue = nil
		for i, t := range ranked {
			if i >= topK {
				break
			}
			st.queue = append(st.queue, strategy.Variants(t.Base)...)
		}
		if len(st.queue) > 0 {
			st.done = false
			stage2 = true
		}
	}
	if stage2 {
		emit(Event{Type: "stage", Stage: 2, Message: "подбираю имя-приманку из белого списка"})
		for {
			cand := map[*groupState]strategy.Candidate{}
			for _, st := range states {
				if st.done {
					continue
				}
				if !o.Deep && perfect(st) {
					st.done = true
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
			emit(Event{Type: "round", Stage: 2, Round: rep.Rounds + 1})
			if err := round(2, cand); err != nil {
				return nil, err
			}
		}
	}

	// ---- Stage 3: breed new recipes from what worked ----
	if o.Synth > 0 {
		if err := breed(ctx, o, states, round, next, emit, rep); err != nil {
			return nil, err
		}
	}

	// ---- Stage 4: one recipe for every TLS group ----
	preferred := map[*groupState]string{}
	if o.Synth > 0 {
		u, groups, err := universal(ctx, o, states, round, emit, rep)
		if err != nil {
			return nil, err
		}
		if u != "" {
			rep.Universal, rep.UniversalGroups = u, groups
			for _, st := range states {
				if slices.Contains(groups, st.g.ID) {
					preferred[st] = u
				}
			}
		}
	}

	// ---- Verification: run the combined configuration a few times ----
	emit(Event{Type: "stage", Stage: 5, Message: "проверяю итоговую связку"})
	ranking := map[*groupState][]Trial{}
	for _, st := range states {
		r := append([]Trial(nil), st.trials...)
		slices.SortStableFunc(r, func(a, b Trial) int {
			if better(st.g, a, b) {
				return -1
			}
			if better(st.g, b, a) {
				return 1
			}
			return 0
		})
		// The universal recipe wins every tie: one profile for everything.
		if u := preferred[st]; u != "" {
			for i, t := range r {
				if t.Candidate == u && !better(st.g, r[0], t) {
					r = append([]Trial{t}, append(r[:i:i], r[i+1:]...)...)
					break
				}
			}
		}
		ranking[st] = r
	}
	pickIdx := map[*groupState]int{}
	verified := map[*groupState]bool{}
	for attempt := 0; attempt < 3; attempt++ {
		cand := map[*groupState]strategy.Candidate{}
		for _, st := range states {
			r := ranking[st]
			if len(r) == 0 {
				continue
			}
			c, _ := strategy.ParseCandidateID(r[pickIdx[st]].Candidate)
			cand[st] = c
		}
		okAll := map[*groupState]int{}
		totalAll := map[*groupState]int{}
		for pass := 0; pass < o.Verify; pass++ {
			sel := selection(func(st *groupState) strategy.Candidate { return cand[st] })
			if err := rn.Apply(ctx, sel); err != nil {
				return nil, err
			}
			var targets []probe.Target
			for _, st := range states {
				targets = append(targets, st.targets...)
			}
			for _, r := range runProbes(ctx, targets, o.Probe, 16) {
				for _, st := range states {
					if st.g.ID == r.Group {
						totalAll[st]++
						if r.OK {
							okAll[st]++
						}
					}
				}
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		retry := false
		for _, st := range states {
			r := ranking[st]
			if len(r) == 0 {
				continue
			}
			want := r[pickIdx[st]].Ratio()
			got := 0.0
			if totalAll[st] > 0 {
				got = float64(okAll[st]) / float64(totalAll[st])
			}
			emit(Event{Type: "verify", Group: st.g.ID, Candidate: cand[st].ID, OK: okAll[st], Total: totalAll[st]})
			// Flaky pick: fall back to the next best one.
			if got+0.01 < want && pickIdx[st]+1 < len(r) && r[pickIdx[st]+1].Ratio() > 0 {
				pickIdx[st]++
				retry = true
				continue
			}
			verified[st] = got > 0 || want == 0
		}
		if !retry {
			break
		}
	}

	for _, st := range states {
		gr := GroupReport{Group: st.g.ID, Title: st.g.Title, Trials: st.trials}
		for _, t := range st.trials {
			if t.Candidate == strategy.None {
				gr.Baseline = t
				gr.Blocking = dominant(t.Classes)
				break
			}
		}
		if r := ranking[st]; len(r) > 0 {
			gr.Best = r[pickIdx[st]]
			gr.Pick = gr.Best.Candidate
			if c, ok := strategy.ParseCandidateID(gr.Pick); ok {
				gr.PickTags = strategy.Tags(c.Args)
			}
		}
		gr.Verified = verified[st]
		switch {
		case gr.Best.OK == 0:
			gr.Note = "ни один рецепт не помог — вероятна блокировка по IP; поможет только VPN/прокси"
		case gr.Pick == strategy.None:
			gr.Note = "работает без обхода — не трогаем"
		case gr.Best.Ratio() < 1:
			gr.Note = "частично: некоторые узлы недоступны"
		}
		rep.Picks[st.g.ID] = gr.Pick
		rep.Groups = append(rep.Groups, gr)
	}
	keep := map[string]bool{rep.Universal: true}
	for _, p := range rep.Picks {
		keep[p] = true
	}
	for id, b := range strategy.BredRecipes() {
		if keep[id] {
			if rep.Bred == nil {
				rep.Bred = map[string]strategy.Bred{}
			}
			rep.Bred[id] = b
		}
	}
	rep.Finished = time.Now()
	emit(Event{Type: "done", Message: fmt.Sprintf("готово за %s, прогонов: %d", rep.Finished.Sub(rep.Started).Round(time.Second), rep.Rounds)})
	return rep, nil
}

func dominant(classes map[string]int) string {
	best, n := probe.ClassOK, 0
	for c, k := range classes {
		if c != probe.ClassOK && k > n {
			best, n = c, k
		}
	}
	if n == 0 {
		return probe.ClassOK
	}
	return best
}
