package strategy

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestCatalogValid(t *testing.T) {
	seen := map[string]bool{}
	for _, tpl := range Templates {
		if seen[tpl.ID] {
			t.Fatalf("duplicate template %s", tpl.ID)
		}
		seen[tpl.ID] = true
		if tpl.UsesSNI() && tpl.SNI == "" {
			t.Errorf("%s uses $SNI but has no default", tpl.ID)
		}
		c := tpl.Instantiate("")
		if err := Validate(c.Args); err != nil {
			t.Errorf("%s: %v", tpl.ID, err)
		}
		for _, v := range Variants(tpl.ID) {
			if err := Validate(v.Args); err != nil {
				t.Errorf("%s: %v", v.ID, err)
			}
			if back, ok := ParseCandidateID(v.ID); !ok || back.Args != v.Args {
				t.Errorf("round trip of %s failed", v.ID)
			}
		}
	}
	for _, g := range Groups {
		if _, ok := ParseCandidateID(g.Default); !ok {
			t.Errorf("group %s: bad default %q", g.ID, g.Default)
		}
		if g.Default != None {
			c, _ := ParseCandidateID(g.Default)
			tpl, _ := TemplateByID(c.Base)
			if tpl.Kind != g.Kind {
				t.Errorf("group %s: default %s has kind %s", g.ID, g.Default, tpl.Kind)
			}
		}
	}
}

func TestValidateRejectsDangerous(t *testing.T) {
	bad := []string{
		"--dpi-desync=fake --debug=@/root/x",
		"--dpi-desync=fake --dpi-desync-fake-tls=/etc/shadow",
		"--dpi-desync=fake --dpi-desync-fake-tls=@/etc/shadow",
		"--dpi-desync=fake --dpi-desync-fake-tls=@bin:../../etc/shadow.bin",
		"--dpi-desync=fake --hostlist=/etc/passwd",
		"--dpi-desync=fake --pidfile=/etc/passwd",
		"--dpi-desync=fake --user=root",
		"--dpi-desync=fake --wf-raw=true",
		"--dpi-desync=fake --dpi-desync-fake-tls=@tls:bad;host",
		"--dpi-desync-repeats=6",
		"--dpi-desync=fake --dpi-desync-hostfakesplit-mod=host=$(reboot)",
		"@/etc/zapret.conf",
	}
	for _, b := range bad {
		if err := Validate(b); err == nil {
			t.Errorf("accepted dangerous recipe: %s", b)
		}
	}
}

func fakeEnv(os string) Env {
	return Env{
		OS: os, QNum: 200,
		Resolve: func(ref string) (string, int, error) {
			return "/data/fakes/" + strings.NewReplacer(":", "_", "@", "").Replace(ref) + ".bin", 681, nil
		},
		Hostlist: func(n string) string { return "/data/lists/" + n + ".txt" },
		IPSet:    func(n string) string { return "/data/lists/ipset-" + n + ".txt" },
	}
}

func TestComposeLinux(t *testing.T) {
	groups := ActiveGroups(map[string]bool{SvcYouTube: true, SvcDiscord: true, SvcR6: true})
	plan, err := Compose(fakeEnv("linux"), groups, func(g Group) Candidate {
		c, _ := ParseCandidateID(g.Default)
		return c
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(plan.Args, " ")
	if !strings.HasPrefix(args, "--qnum=200 ") {
		t.Errorf("missing qnum: %s", args)
	}
	if strings.Contains(args, "@") {
		t.Errorf("unresolved reference left: %s", args)
	}
	if !strings.Contains(args, "--dpi-desync-split-seqovl=681") {
		t.Errorf("seqovl must be the decoy length: %s", args)
	}
	// Ubisoft defaults to "none" and must not produce a profile.
	if slices.Contains(plan.Groups, "ubisoft") {
		t.Errorf("ubisoft group with None candidate rendered")
	}
	if plan.Selection["ubisoft"] != None {
		t.Errorf("selection not recorded")
	}
	var gameRule *PortRule
	for i := range plan.Rules {
		if plan.Rules[i].Set == "game" && plan.Rules[i].Proto == "udp" {
			gameRule = &plan.Rules[i]
		}
	}
	if gameRule == nil || gameRule.Ports != "1024-65535" {
		t.Errorf("game UDP rule missing: %+v", plan.Rules)
	}
	if n := strings.Count(args, "--new"); n != len(plan.Groups)-1 {
		t.Errorf("%d --new for %d groups", n, len(plan.Groups))
	}
}

func TestComposeWindowsFilters(t *testing.T) {
	groups := ActiveGroups(map[string]bool{SvcYouTube: true, SvcDiscord: true})
	plan, err := Compose(fakeEnv("windows"), groups, func(g Group) Candidate {
		c, _ := ParseCandidateID(g.Default)
		return c
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--wf-tcp=80,443,2053,2083,2087,2096,8443", "--wf-udp=443,19294-19344,50000-50100"}
	if !slices.Equal(plan.Args[:2], want) {
		t.Errorf("windivert filter = %v, want %v", plan.Args[:2], want)
	}
}

func TestPorts(t *testing.T) {
	r, err := ParsePorts("443,80,1024-2000,1500-3000,3001")
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatPorts(MergePorts(r)); got != "80,443,1024-3001" {
		t.Errorf("merge = %s", got)
	}
	for _, bad := range []string{"0", "70000", "5-3", "a"} {
		if _, err := ParsePorts(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTags(t *testing.T) {
	c, _ := ParseCandidateID("fake-ts@max.ru")
	got := fmt.Sprint(Tags(c.Args))
	if got != "[фейк ×6 TS max.ru]" {
		t.Errorf("tags = %s", got)
	}
}
