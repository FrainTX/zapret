package lists

import (
	"net/netip"
	"slices"
	"testing"
)

func pfx(ss ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestAggregate(t *testing.T) {
	cases := []struct {
		in, want []netip.Prefix
	}{
		{pfx("10.0.0.0/24", "10.0.1.0/24"), pfx("10.0.0.0/23")},
		{pfx("10.0.0.0/24", "10.0.1.0/24", "10.0.2.0/23"), pfx("10.0.0.0/22")},
		{pfx("10.0.0.0/8", "10.1.2.0/24", "10.200.0.0/16"), pfx("10.0.0.0/8")},
		{pfx("10.0.1.0/24", "10.0.2.0/24"), pfx("10.0.1.0/24", "10.0.2.0/24")},
		{pfx("1.2.3.4/32", "1.2.3.5/32", "1.2.3.4/31"), pfx("1.2.3.4/31")},
		{pfx("2001:db8::/33", "2001:db8:8000::/33", "10.0.0.0/24"), pfx("10.0.0.0/24", "2001:db8::/32")},
		{pfx("10.0.0.7/24"), pfx("10.0.0.0/24")},
	}
	for _, c := range cases {
		got := Aggregate(c.in)
		if !slices.Equal(got, c.want) {
			t.Errorf("Aggregate(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"https://Www.YouTube.com/watch?v=1": "www.youtube.com",
		"discord.gg":                        "discord.gg",
		"*.ubisoft.com":                     "ubisoft.com",
		"^dns.google":                       "^dns.google",
		"example.com:443":                   "example.com",
		"localhost":                         "",
		"1.2.3.4":                           "",
		"--debug=@/etc/shadow":              "",
		"a b.com":                           "",
	}
	for in, want := range cases {
		if got := NormalizeDomain(in); got != want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContains(t *testing.T) {
	l := []string{"youtube.com", "^dns.google"}
	if !Contains(l, "www.youtube.com") || !Contains(l, "youtube.com") || Contains(l, "notyoutube.com") {
		t.Error("subdomain matching broken")
	}
	if !Contains(l, "dns.google") || Contains(l, "x.dns.google") {
		t.Error("exact matching broken")
	}
}

func TestBuiltinLists(t *testing.T) {
	for _, n := range []string{YouTube, Discord, Ubisoft, General, Exclude} {
		l := Builtin(n)
		if len(l) == 0 {
			t.Fatalf("builtin %s is empty", n)
		}
		for _, d := range l {
			if NormalizeDomain(d) != d {
				t.Errorf("%s: entry %q is not normalised", n, d)
			}
		}
	}
}
