package tuner

import (
	"context"
	"strings"
	"sync"
	"testing"

	"prosvet/internal/probe"
	"prosvet/internal/strategy"
)

type fakeRunner struct {
	mu      sync.Mutex
	sel     map[string]strategy.Candidate
	applies int
}

func (f *fakeRunner) Apply(_ context.Context, sel map[string]strategy.Candidate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sel = sel
	f.applies++
	return nil
}

// simulated network: which candidate makes which group work.
func simulate(f *fakeRunner, tried map[string][]string) func(context.Context, []probe.Target, probe.Options, int) []probe.Result {
	return func(_ context.Context, ts []probe.Target, _ probe.Options, _ int) []probe.Result {
		f.mu.Lock()
		sel := f.sel
		f.mu.Unlock()
		out := make([]probe.Result, len(ts))
		seen := map[string]bool{}
		for i, t := range ts {
			c := sel[t.Group]
			if !seen[t.Group] {
				tried[t.Group] = append(tried[t.Group], c.ID)
				seen[t.Group] = true
			}
			r := probe.Result{Target: t.ID, Group: t.Group, Class: probe.ClassReset}
			switch t.Group {
			case "youtube":
				switch {
				case c.Base == "mdisorder-midsld":
					r.Class = probe.ClassOK
				case strings.Contains(c.Args, "fooling=ts"):
					r.Class = probe.ClassLeak
				}
			case "discord":
				if c.ID == "fake-badseq@max.ru" {
					r.Class = probe.ClassOK
				}
			case "ubisoft", "game-udp":
				r.Class = probe.ClassOK
			case "cloud":
				r.Class = probe.ClassIP
			}
			r.OK = r.Class == probe.ClassOK
			if r.OK {
				r.Latency = 50e6
			}
			out[i] = r
		}
		return out
	}
}

func TestTuner(t *testing.T) {
	f := &fakeRunner{}
	tried := map[string][]string{}
	old := runProbes
	runProbes = simulate(f, tried)
	defer func() { runProbes = old }()

	var groups []strategy.Group
	for _, id := range []string{"youtube", "discord", "ubisoft", "game-udp", "cloud"} {
		g, _ := strategy.GroupByID(id)
		groups = append(groups, g)
	}
	rep, err := Run(context.Background(), f, Options{Groups: groups}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]func(string) bool{
		"youtube":  func(p string) bool { return p == "mdisorder-midsld" },
		"discord":  func(p string) bool { return p == "fake-badseq@max.ru" },
		"ubisoft":  func(p string) bool { return p == strategy.None },
		"game-udp": func(p string) bool { return p != strategy.None }, // PreferActive
	}
	for g, ok := range want {
		if !ok(rep.Picks[g]) {
			t.Errorf("%s: pick %q", g, rep.Picks[g])
		}
	}
	for _, gr := range rep.Groups {
		if gr.Group == "cloud" && !strings.Contains(gr.Note, "по IP") {
			t.Errorf("cloud note = %q", gr.Note)
		}
		if gr.Group == "youtube" && gr.Blocking != probe.ClassReset {
			t.Errorf("youtube blocking = %q", gr.Blocking)
		}
	}
	// After the first leaking ts-recipe, other ts-recipes must be skipped.
	ts := 0
	for _, id := range tried["youtube"] {
		c, _ := strategy.ParseCandidateID(id)
		if strings.Contains(c.Args, "fooling=ts") {
			ts++
		}
	}
	if ts > 1 {
		t.Errorf("leaky fooling method tried %d times for youtube", ts)
	}
	if len(rep.Leaky["youtube"]) == 0 {
		t.Error("leak not recorded")
	}
	// Ubisoft works without bypass: in quick mode only the baseline runs.
	if n := len(tried["ubisoft"]); n > 1+2*2 { // baseline + verification passes
		t.Errorf("ubisoft probed %d times", n)
	}
	t.Logf("rounds=%d applies=%d", rep.Rounds, f.applies)
}
