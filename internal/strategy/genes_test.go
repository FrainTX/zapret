package strategy

import (
	"math/rand/v2"
	"testing"
)

// Breed is shared with the engine's dry-run test.
func TestGenomeRoundTrip(t *testing.T) {
	for _, tpl := range Templates {
		c := tpl.Instantiate("")
		g := ParseGenome(tpl.Kind, c.Args)
		back := ParseGenome(tpl.Kind, g.Args())
		if g.Args() != back.Args() {
			t.Errorf("%s: unstable canonical form", tpl.ID)
		}
		if _, ok := Repair(g); !ok {
			t.Errorf("%s: catalogue recipe rejected by Repair: %s", tpl.ID, c.Args)
		}
	}
}

func TestBreedValid(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, k := range []Kind{KindTLS, KindQUIC, KindVoice, KindGameUDP, KindGameTCP} {
		cs := Candidates(k)[1:]
		seen := map[string]bool{}
		for i := 0; i < 400; i++ {
			a := ParseGenome(k, cs[r.IntN(len(cs))].Args)
			b := ParseGenome(k, cs[r.IntN(len(cs))].Args)
			child := Mutate(Cross(a, b, r), r)
			fixed, ok := Repair(child)
			if !ok {
				continue
			}
			args := fixed.Args()
			if err := Validate(args); err != nil {
				t.Fatalf("%s: %v: %s", k, err, args)
			}
			if k == KindGameUDP && Cutoff(args) == 0 {
				t.Fatalf("game recipe without cutoff: %s", args)
			}
			seen[args] = true
		}
		if len(seen) < 20 {
			t.Errorf("%s: only %d distinct children", k, len(seen))
		}
		t.Logf("%s: %d distinct children", k, len(seen))
	}
}

func TestRegisterBred(t *testing.T) {
	args := "--dpi-desync=fake,multisplit --dpi-desync-split-pos=2 --dpi-desync-fooling=ts --dpi-desync-repeats=3 --dpi-desync-fake-tls=@tls:vk.com"
	c, ok := Register(Bred{Kind: KindTLS, Args: args})
	if !ok || !IsBred(c.ID) {
		t.Fatal("not registered")
	}
	got, ok := ParseCandidateID(c.ID)
	if !ok || got.Args != args || got.SNI != "vk.com" {
		t.Fatalf("lookup: %+v", got)
	}
	found := false
	for _, x := range Candidates(KindTLS) {
		found = found || x.ID == c.ID
	}
	if !found {
		t.Fatal("bred recipe not offered as candidate")
	}
	if _, ok := Register(Bred{Kind: KindTLS, Args: "--hostlist=/etc/passwd"}); ok {
		t.Fatal("unsafe recipe registered")
	}
}

func TestCostCutoff(t *testing.T) {
	if Cutoff("--dpi-desync=fake --dpi-desync-cutoff=n3") != 3 || QueuePackets("--dpi-desync-cutoff=n2", 12) != 3 || QueuePackets("x", 12) != 12 {
		t.Fatal("cutoff")
	}
	if Cost("--dpi-desync=fake --dpi-desync-repeats=6") != 6 || Cost("--dpi-desync=multisplit --dpi-desync-split-pos=1,midsld") != 2 {
		t.Fatal("cost")
	}
}
