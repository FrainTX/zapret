package tuner

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"prosvet/internal/probe"
	"prosvet/internal/strategy"
)

// A network where no catalogue recipe gets through, and partial success
// gives the evolution a gradient: decoys disguised with "datanoack" pass
// half the probes, and cutting at the SNI extension on top of that passes
// all. Neither is in any template, so only breeding can find it. The game
// UDP group rewards cheap recipes with lower ping.
func TestBreedFindsNewRecipe(t *testing.T) {
	f := &fakeRunner{}
	old := runProbes
	runProbes = func(_ context.Context, ts []probe.Target, _ probe.Options, _ int) []probe.Result {
		f.mu.Lock()
		sel := f.sel
		f.mu.Unlock()
		out := make([]probe.Result, len(ts))
		for i, tg := range ts {
			c := sel[tg.Group]
			r := probe.Result{Target: tg.ID, Group: tg.Group, Class: probe.ClassReset}
			switch tg.Group {
			case "youtube", "discord":
				dn := strings.Contains(c.Args, "fooling=datanoack")
				sx := strings.Contains(c.Args, "sniext")
				if dn && (sx || i%2 == 0) {
					r.Class = probe.ClassOK
				}
			case "game-udp":
				if c.ID != strategy.None {
					r.Class = probe.ClassOK
				}
			}
			r.OK = r.Class == probe.ClassOK
			if r.OK {
				r.Latency = 40e6
				if tg.Group == "game-udp" {
					// every decoy repeat costs a little
					r.Latency = int64ToDur(30 + strategy.Cost(c.Args))
				}
			}
			out[i] = r
		}
		return out
	}
	defer func() { runProbes = old }()

	var groups []strategy.Group
	for _, id := range []string{"youtube", "discord", "game-udp"} {
		g, _ := strategy.GroupByID(id)
		groups = append(groups, g)
	}
	seed := uint64(42)
	if v := os.Getenv("PROSVET_SEED"); v != "" {
		seed, _ = strconv.ParseUint(v, 10, 64)
	}
	rep, err := Run(context.Background(), f, Options{Groups: groups, Synth: 8, Brood: 8, Seed: seed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range rep.Groups {
		t.Logf("%s: pick=%s ok=%d/%d lat=%d cost=%d", g.Group, g.Pick, g.Best.OK, g.Best.Total, g.Best.Latency, g.Best.Cost)
	}
	yt := rep.Groups[0]
	if !strategy.IsBred(yt.Pick) || yt.Best.Ratio() != 1 {
		t.Fatalf("breeding did not find the hidden recipe: %+v", yt.Best)
	}
	if _, ok := rep.Bred[yt.Pick]; !ok {
		t.Fatal("winning bred recipe not kept in report")
	}
	if rep.Universal == "" || len(rep.UniversalGroups) != 2 {
		t.Fatalf("no universal recipe: %q %v", rep.Universal, rep.UniversalGroups)
	}
	if rep.Picks["youtube"] != rep.Universal || rep.Picks["discord"] != rep.Universal {
		t.Fatalf("picks do not use the universal recipe: %v", rep.Picks)
	}
	gu := rep.Groups[2]
	c, _ := strategy.ParseCandidateID(gu.Pick)
	if strategy.Cost(c.Args) > 6 {
		t.Errorf("game pick is expensive (%d extra packets): %s", strategy.Cost(c.Args), c.Args)
	}
	t.Logf("universal: %s", rep.Universal)
	u, _ := strategy.ParseCandidateID(rep.Universal)
	t.Logf("  %s", u.Args)
}

func int64ToDur(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }
