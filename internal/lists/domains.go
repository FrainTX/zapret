// Package lists holds the domain lists and IP sets that decide which traffic
// the engine touches, plus the code that refreshes them from public sources.
package lists

import (
	"bufio"
	"embed"
	"net/netip"
	"os"
	"slices"
	"strings"

	"prosvet/internal/fakes"
)

//go:embed data
var data embed.FS

// Built-in hostlist names.
const (
	YouTube  = "youtube"
	Discord  = "discord"
	Ubisoft  = "ubisoft"
	Telegram = "telegram"
	General  = "general"
	Exclude  = "exclude"
)

// Builtin returns an embedded domain list.
func Builtin(name string) []string {
	b, err := data.ReadFile("data/" + name + ".txt")
	if err != nil {
		return nil
	}
	return ParseDomains(string(b))
}

// BuiltinRaw returns the embedded file as is (comments included).
func BuiltinRaw(name string) []byte {
	b, _ := data.ReadFile("data/" + name + ".txt")
	return b
}

// ParseDomains extracts domains from list text. Lines may contain comments
// and the zapret "^" prefix (exact match, no subdomains).
func ParseDomains(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// NormalizeDomain turns user input ("https://Www.Example.com/path") into a
// hostlist entry ("www.example.com"). It returns "" for garbage, which is
// how the daemon refuses anything that is not a plain host name.
func NormalizeDomain(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s[:i], ":") {
		s = s[:i]
	}
	exact := strings.HasPrefix(s, "^")
	s = strings.TrimPrefix(s, "^")
	s = strings.TrimPrefix(s, "*.")
	s = strings.TrimSuffix(s, ".")
	if _, err := netip.ParseAddr(s); err == nil {
		return ""
	}
	if !fakes.ValidHostname(s) || !strings.Contains(s, ".") {
		return ""
	}
	if exact {
		return "^" + s
	}
	return s
}

// Merge combines lists, normalising and de-duplicating entries.
func Merge(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, d := range l {
			if n := NormalizeDomain(d); n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return out
}

// WriteHostlist writes a hostlist file readable by nfqws/winws. An empty list
// is written as a single impossible domain because zapret treats an empty
// include-list as "match everything".
func WriteHostlist(path string, domains []string) error {
	if len(domains) == 0 {
		domains = []string{"^prosvet.invalid"}
	}
	return os.WriteFile(path, []byte(strings.Join(domains, "\n")+"\n"), 0o644)
}

// Contains reports whether host is covered by a hostlist (subdomain aware).
func Contains(list []string, host string) bool {
	host = strings.ToLower(host)
	for _, d := range list {
		if strings.HasPrefix(d, "^") {
			if host == d[1:] {
				return true
			}
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
