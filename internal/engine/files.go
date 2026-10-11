package engine

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"prosvet/internal/fakes"
	"prosvet/internal/lists"
	"prosvet/internal/strategy"
)

// Files produces everything the engine reads from disk.
type Files struct {
	FakesDir string
	ListsDir string

	mu   sync.Mutex
	sets map[string][]netip.Prefix
}

// NewFiles creates the directories if needed.
func NewFiles(fakesDir, listsDir string) (*Files, error) {
	for _, d := range []string{fakesDir, listsDir, filepath.Join(listsDir, "ip")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return &Files{FakesDir: fakesDir, ListsDir: listsDir, sets: map[string][]netip.Prefix{}}, nil
}

func refFile(ref string) (string, error) {
	switch {
	case ref == "@stun":
		return "gen_stun.bin", nil
	case strings.HasPrefix(ref, "@bin:"):
		name := ref[5:]
		if strings.ContainsAny(name, `/\:`) || strings.HasPrefix(name, ".") {
			return "", fmt.Errorf("bad bundled decoy name")
		}
		return "stock_" + name, nil
	case strings.HasPrefix(ref, "@tls:"), strings.HasPrefix(ref, "@quic:"), strings.HasPrefix(ref, "@http:"):
		kind, host, _ := strings.Cut(ref[1:], ":")
		if !fakes.ValidHostname(host) {
			return "", fakes.ErrBadHostname
		}
		return "gen_" + kind + "_" + host + ".bin", nil
	}
	return "", fmt.Errorf("unknown decoy reference %q", ref)
}

func generate(ref string) ([]byte, error) {
	switch {
	case ref == "@stun":
		return fakes.STUNBindingRequest(), nil
	case strings.HasPrefix(ref, "@tls:"):
		// 517 bytes is what Chrome sends without post-quantum key shares;
		// a familiar size makes the decoy indistinguishable from a browser.
		return fakes.TLSClientHello(ref[5:], fakes.TLSOptions{PadTo: 517})
	case strings.HasPrefix(ref, "@quic:"):
		return fakes.QUICInitial(ref[6:], fakes.QUICOptions{Size: 1250})
	case strings.HasPrefix(ref, "@http:"):
		return fakes.HTTPRequest(ref[6:])
	}
	return nil, fmt.Errorf("cannot generate %q", ref)
}

// Resolve implements strategy.Env.Resolve: it returns the path and size of
// a decoy, generating it on first use.
func (f *Files) Resolve(ref string) (string, int, error) {
	name, err := refFile(ref)
	if err != nil {
		return "", 0, err
	}
	p := filepath.Join(f.FakesDir, name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		return p, int(st.Size()), nil
	}
	if strings.HasPrefix(ref, "@bin:") {
		return "", 0, fmt.Errorf("bundled decoy %s is missing", ref[5:])
	}
	b, err := generate(ref)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", 0, err
	}
	return p, len(b), nil
}

// Hostlist implements strategy.Env.Hostlist.
func (f *Files) Hostlist(name string) string { return filepath.Join(f.ListsDir, name+".txt") }

// IPSet implements strategy.Env.IPSet.
func (f *Files) IPSet(name string) string { return filepath.Join(f.ListsDir, "ipset-"+name+".txt") }

// Env returns a composer environment backed by these files.
func (f *Files) Env(os string, qnum int) strategy.Env {
	return strategy.Env{OS: os, QNum: qnum, Resolve: f.Resolve, Hostlist: f.Hostlist, IPSet: f.IPSet}
}

// ProviderPrefixes returns the freshest prefixes for a provider: a
// downloaded update if present, the embedded snapshot otherwise.
func (f *Files) ProviderPrefixes(id string) ([]netip.Prefix, error) {
	p := filepath.Join(f.ListsDir, "ip", id+".txt")
	if fh, err := os.Open(p); err == nil {
		defer fh.Close()
		if ps, err := lists.ParsePrefixes(fh); err == nil && len(ps) > 0 {
			return ps, nil
		}
	}
	return lists.SnapshotPrefixes(id)
}

// SaveProvider stores freshly downloaded prefixes for a provider.
func (f *Files) SaveProvider(id string, ps []netip.Prefix) error {
	p := filepath.Join(f.ListsDir, "ip", id+".txt")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, lists.FormatPrefixes(ps), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ListOptions are the user-controlled inputs of WriteLists.
type ListOptions struct {
	Custom    []string // user domains to unblock
	Exclude   []string // user domains never to touch
	Providers []string // restrict the cloud set (empty = all)
}

// WriteLists (re)writes every hostlist and IP set file.
func (f *Files) WriteLists(o ListOptions) error {
	write := func(name string, domains []string) error {
		return lists.WriteHostlist(f.Hostlist(name), domains)
	}
	for _, n := range []string{lists.YouTube, lists.Discord, lists.Ubisoft, lists.Telegram, lists.General, lists.Exclude} {
		if err := write(n, lists.Builtin(n)); err != nil {
			return err
		}
	}
	if err := write("custom", lists.Merge(o.Custom)); err != nil {
		return err
	}
	if err := write("exclude-user", lists.Merge(o.Exclude)); err != nil {
		return err
	}
	if err := os.WriteFile(f.IPSet("ipset-exclude"), lists.BuiltinRaw("ipset-exclude"), 0o644); err != nil {
		return err
	}

	allow := map[string]bool{}
	for _, id := range o.Providers {
		allow[id] = true
	}
	sets := map[string][]netip.Prefix{}
	for _, set := range []string{"cloud", "game"} {
		var all []netip.Prefix
		for _, id := range lists.SetProviders(set) {
			if set == "cloud" && len(allow) > 0 && !allow[id] {
				continue
			}
			ps, err := f.ProviderPrefixes(id)
			if err != nil {
				continue
			}
			all = append(all, ps...)
		}
		all = lists.Aggregate(all)
		if len(all) == 0 {
			// An empty ipset would match everything; use a documentation
			// address that never appears on the wire instead.
			all = []netip.Prefix{netip.MustParsePrefix("203.0.113.113/32")}
		}
		if err := os.WriteFile(f.IPSet(set), lists.FormatPrefixes(all), 0o644); err != nil {
			return err
		}
		sets[set] = all
	}
	f.mu.Lock()
	f.sets = sets
	f.mu.Unlock()
	return nil
}

// SetPrefixes returns the prefixes of a written IP set (for nft sets).
func (f *Files) SetPrefixes(name string) []netip.Prefix {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets[name]
}
