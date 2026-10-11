package engine

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"prosvet/internal/strategy"
)

func TestInstallIdempotent(t *testing.T) {
	dir := t.TempDir()
	eng, fk := filepath.Join(dir, "engine"), filepath.Join(dir, "fakes")
	if err := Install(eng, fk); err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(filepath.Join(fk, "stock_stun.bin"))
	if err := Install(eng, fk); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(filepath.Join(fk, "stock_stun.bin"))
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("unchanged file was rewritten")
	}
	// Simulate an antivirus mangling the engine: Install must repair it.
	if Bundled() {
		bin := filepath.Join(eng, BinaryName())
		os.WriteFile(bin, []byte("quarantined"), 0o755)
		if err := Install(eng, fk); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(bin); string(b) == "quarantined" {
			t.Error("tampered engine was not restored")
		}
	}
}

// TestDryRunCatalog feeds every candidate of every group through the real
// engine's argument parser. This is what keeps a typo like
// "--dpi-desync=fake multisplit" from ever shipping.
func TestDryRunCatalog(t *testing.T) {
	if runtime.GOOS != "linux" || !Bundled() {
		t.Skip("needs a runnable bundled engine")
	}
	dir := t.TempDir()
	eng, fk := filepath.Join(dir, "engine"), filepath.Join(dir, "fakes")
	if err := Install(eng, fk); err != nil {
		t.Fatal(err)
	}
	files, err := NewFiles(fk, filepath.Join(dir, "lists"))
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteLists(ListOptions{Custom: []string{"example.com"}}); err != nil {
		t.Fatal(err)
	}
	bin, err := Locate(eng)
	if err != nil {
		t.Fatal(err)
	}
	if err := DryRun(context.Background(), bin, []string{"--qnum=1", "--dpi-desync=fake"}); err != nil {
		t.Skipf("engine cannot dry-run here: %v", err)
	}
	env := files.Env("linux", 216)
	checked := 0
	for _, g := range strategy.Groups {
		cands := strategy.Candidates(g.Kind)
		for _, c := range cands {
			for _, v := range append([]strategy.Candidate{c}, strategy.Variants(c.Base)...) {
				plan, err := strategy.Compose(env, []strategy.Group{g}, func(strategy.Group) strategy.Candidate { return v })
				if err != nil {
					t.Fatalf("%s/%s: %v", g.ID, v.ID, err)
				}
				if len(plan.Args) == 0 {
					continue
				}
				if err := DryRun(context.Background(), bin, plan.Args); err != nil {
					t.Errorf("%s/%s: %v\nargs: %s", g.ID, v.ID, err, strings.Join(plan.Args, " "))
				}
				checked++
			}
		}
	}
	t.Logf("%d engine command lines accepted", checked)
}

// TestDryRunBred breeds hundreds of recipes per kind and feeds every one of
// them to the real engine: synthesis must never produce a command line nfqws
// refuses.
func TestDryRunBred(t *testing.T) {
	if runtime.GOOS != "linux" || !Bundled() {
		t.Skip("needs a runnable bundled engine")
	}
	dir := t.TempDir()
	eng, fk := filepath.Join(dir, "engine"), filepath.Join(dir, "fakes")
	if err := Install(eng, fk); err != nil {
		t.Fatal(err)
	}
	files, _ := NewFiles(fk, filepath.Join(dir, "lists"))
	if err := files.WriteLists(ListOptions{}); err != nil {
		t.Fatal(err)
	}
	bin, _ := Locate(eng)
	if err := DryRun(context.Background(), bin, []string{"--qnum=1", "--dpi-desync=fake"}); err != nil {
		t.Skipf("engine cannot dry-run here: %v", err)
	}
	env := files.Env("linux", 216)
	r := rand.New(rand.NewPCG(7, 7))
	checked := 0
	for _, g := range strategy.Groups {
		cs := strategy.Candidates(g.Kind)[1:]
		seen := map[string]bool{}
		for i := 0; i < 150; i++ {
			a := strategy.ParseGenome(g.Kind, cs[r.IntN(len(cs))].Args)
			b := strategy.ParseGenome(g.Kind, cs[r.IntN(len(cs))].Args)
			child, ok := strategy.Repair(strategy.Mutate(strategy.Cross(a, b, r), r))
			if !ok || seen[child.Args()] {
				continue
			}
			seen[child.Args()] = true
			c := strategy.Candidate{ID: "x", Args: child.Args()}
			plan, err := strategy.Compose(env, []strategy.Group{g}, func(strategy.Group) strategy.Candidate { return c })
			if err != nil {
				t.Fatalf("%s: %v: %s", g.ID, err, c.Args)
			}
			if err := DryRun(context.Background(), bin, plan.Args); err != nil {
				t.Errorf("%s: %v\nrecipe: %s", g.ID, err, c.Args)
			}
			checked++
		}
	}
	t.Logf("%d bred command lines accepted", checked)
}
