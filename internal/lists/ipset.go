package lists

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Prefix is re-exported for callers that only deal with lists.
type Prefix = netip.Prefix

// Provider is a hosting/CDN company whose address space the Russian TSPU
// treats with the "TCP 16-20" freeze, or a game publisher we care about.
type Provider struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	ASNs  []int  `json:"asns"`
	Cloud bool   `json:"cloud"` // part of the "cloud" set (16-20 bypass)
	Game  bool   `json:"game"`  // part of the "game" set (UDP/TCP high ports)
}

// Providers is ordered roughly by how often their ranges are throttled.
// Ubisoft's own ASNs (8361, 22634, 28759, 55743) announce nothing: its
// services live on AWS, Akamai and its subsidiary i3D.net.
var Providers = []Provider{
	{ID: "aws", Name: "Amazon AWS", ASNs: []int{16509, 14618, 8987}, Cloud: true, Game: true},
	{ID: "azure", Name: "Microsoft Azure", ASNs: []int{8075, 8068}, Cloud: true, Game: true},
	{ID: "gcp", Name: "Google Cloud", ASNs: []int{396982}, Cloud: true, Game: true},
	{ID: "cloudflare", Name: "Cloudflare", ASNs: []int{13335, 209242}, Cloud: true},
	{ID: "akamai", Name: "Akamai / Linode", ASNs: []int{16625, 20940, 21342, 63949}, Cloud: true, Game: true},
	{ID: "fastly", Name: "Fastly", ASNs: []int{54113}, Cloud: true},
	{ID: "hetzner", Name: "Hetzner", ASNs: []int{24940, 213230}, Cloud: true},
	{ID: "ovh", Name: "OVH", ASNs: []int{16276}, Cloud: true},
	{ID: "digitalocean", Name: "DigitalOcean", ASNs: []int{14061}, Cloud: true},
	{ID: "oracle", Name: "Oracle Cloud", ASNs: []int{31898}, Cloud: true},
	{ID: "vultr", Name: "Vultr", ASNs: []int{20473}, Cloud: true},
	{ID: "contabo", Name: "Contabo", ASNs: []int{51167}, Cloud: true},
	{ID: "scaleway", Name: "Scaleway", ASNs: []int{12876}, Cloud: true},
	{ID: "gcore", Name: "Gcore", ASNs: []int{199524, 202422}, Cloud: true, Game: true},
	{ID: "cdn77", Name: "CDN77 / DataCamp", ASNs: []int{60068}, Cloud: true},
	{ID: "leaseweb", Name: "Leaseweb", ASNs: []int{60781, 28753, 16265}, Cloud: true},
	{ID: "melbicom", Name: "Melbicom", ASNs: []int{8849, 56630}, Cloud: true},
	{ID: "buyvm", Name: "FranTech / BuyVM", ASNs: []int{53667}, Cloud: true},
	{ID: "i3d", Name: "i3D.net (серверы Ubisoft)", ASNs: []int{49544, 41031, 206570}, Game: true},
}

// ProviderByID looks a provider up.
func ProviderByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// SetProviders returns the provider ids that make up a named set.
func SetProviders(set string) []string {
	var out []string
	for _, p := range Providers {
		if set == "cloud" && p.Cloud || set == "game" && p.Game {
			out = append(out, p.ID)
		}
	}
	return out
}

// Aggregate normalises, de-duplicates and merges prefixes into the smallest
// equivalent list. IPv4 and IPv6 are handled together.
func Aggregate(in []netip.Prefix) []netip.Prefix {
	ps := make([]netip.Prefix, 0, len(in))
	for _, p := range in {
		if p.IsValid() {
			ps = append(ps, p.Masked())
		}
	}
	slices.SortFunc(ps, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	stack := make([]netip.Prefix, 0, len(ps))
	for _, p := range ps {
		if n := len(stack); n > 0 && stack[n-1].Bits() <= p.Bits() && stack[n-1].Contains(p.Addr()) {
			continue
		}
		stack = append(stack, p)
		for len(stack) >= 2 {
			a, b := stack[len(stack)-2], stack[len(stack)-1]
			if a.Bits() != b.Bits() || a.Bits() == 0 || a.Addr().Is4() != b.Addr().Is4() {
				break
			}
			pa := netip.PrefixFrom(a.Addr(), a.Bits()-1).Masked()
			pb := netip.PrefixFrom(b.Addr(), b.Bits()-1).Masked()
			if pa != pb {
				break
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, pa)
		}
	}
	return stack
}

// ParsePrefixes reads one CIDR or address per line; '#' starts a comment.
func ParsePrefixes(r io.Reader) ([]netip.Prefix, error) {
	var out []netip.Prefix
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if p, err := netip.ParsePrefix(line); err == nil {
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(line)
		if err != nil {
			return nil, fmt.Errorf("lists: bad prefix %q", line)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, sc.Err()
}

// FormatPrefixes renders prefixes one per line.
func FormatPrefixes(ps []netip.Prefix) []byte {
	var b bytes.Buffer
	for _, p := range ps {
		b.WriteString(p.String())
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// SnapshotPrefixes returns the embedded, build-time snapshot for a provider.
func SnapshotPrefixes(id string) ([]netip.Prefix, error) {
	raw, err := data.ReadFile("data/ip/" + id + ".txt.gz")
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return ParsePrefixes(zr)
}

// SnapshotDate is when the embedded IP snapshot was generated.
func SnapshotDate() string {
	b, _ := data.ReadFile("data/ip/DATE")
	return strings.TrimSpace(string(b))
}

// Fetcher downloads fresh prefixes. RIPEstat is used because it is reachable
// from Russian networks where some vendor endpoints (gstatic.com) are not.
type Fetcher struct {
	Client *http.Client
	// MinPeers filters out prefixes seen by few BGP peers (noise).
	MinPeers int
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func (f *Fetcher) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "prosvet-lists/1")
	resp, err := f.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// ASN returns the prefixes currently announced by one autonomous system.
func (f *Fetcher) ASN(ctx context.Context, asn int) ([]netip.Prefix, error) {
	var r struct {
		Data struct {
			Prefixes []struct {
				Prefix string `json:"prefix"`
			} `json:"prefixes"`
		} `json:"data"`
	}
	peers := f.MinPeers
	if peers == 0 {
		peers = 10
	}
	url := fmt.Sprintf("https://stat.ripe.net/data/announced-prefixes/data.json?resource=AS%d&min_peers_seeing=%d&sourceapp=prosvet", asn, peers)
	if err := f.getJSON(ctx, url, &r); err != nil {
		return nil, err
	}
	out := make([]netip.Prefix, 0, len(r.Data.Prefixes))
	for _, p := range r.Data.Prefixes {
		if pp, err := netip.ParsePrefix(p.Prefix); err == nil {
			out = append(out, pp)
		}
	}
	return out, nil
}

// AWS reads Amazon's official ip-ranges.json, which is more complete than BGP.
func (f *Fetcher) AWS(ctx context.Context) ([]netip.Prefix, error) {
	var r struct {
		Prefixes []struct {
			IPPrefix string `json:"ip_prefix"`
		} `json:"prefixes"`
		IPv6Prefixes []struct {
			IPv6Prefix string `json:"ipv6_prefix"`
		} `json:"ipv6_prefixes"`
	}
	if err := f.getJSON(ctx, "https://ip-ranges.amazonaws.com/ip-ranges.json", &r); err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, p := range r.Prefixes {
		if pp, err := netip.ParsePrefix(p.IPPrefix); err == nil {
			out = append(out, pp)
		}
	}
	for _, p := range r.IPv6Prefixes {
		if pp, err := netip.ParsePrefix(p.IPv6Prefix); err == nil {
			out = append(out, pp)
		}
	}
	return out, nil
}

// Provider fetches and aggregates everything for one provider.
func (f *Fetcher) Provider(ctx context.Context, p Provider) ([]netip.Prefix, error) {
	var all []netip.Prefix
	if p.ID == "aws" {
		if ps, err := f.AWS(ctx); err == nil {
			all = append(all, ps...)
		}
	}
	var firstErr error
	for _, asn := range p.ASNs {
		ps, err := f.ASN(ctx, asn)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		all = append(all, ps...)
	}
	if len(all) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("lists: no prefixes for %s", p.ID)
		}
		return nil, firstErr
	}
	return Aggregate(all), nil
}

// FetchAll downloads all providers in parallel. Failed providers are
// reported in the error map; callers fall back to the snapshot for them.
func (f *Fetcher) FetchAll(ctx context.Context, ids []string, progress func(id string, n int, err error)) (map[string][]netip.Prefix, map[string]error) {
	type res struct {
		id  string
		ps  []netip.Prefix
		err error
	}
	out := map[string][]netip.Prefix{}
	errs := map[string]error{}
	sem := make(chan struct{}, 4)
	ch := make(chan res)
	var wg sync.WaitGroup
	for _, id := range ids {
		p, ok := ProviderByID(id)
		if !ok {
			errs[id] = fmt.Errorf("unknown provider")
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ps, err := f.Provider(ctx, p)
			ch <- res{p.ID, ps, err}
		}()
	}
	go func() { wg.Wait(); close(ch) }()
	for r := range ch {
		if progress != nil {
			progress(r.id, len(r.ps), r.err)
		}
		if r.err != nil {
			errs[r.id] = r.err
			continue
		}
		out[r.id] = r.ps
	}
	return out, errs
}

// WriteGzipFile writes data gzip-compressed (nfqws/winws read .gz natively).
func WriteGzipFile(path string, b []byte) error {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
