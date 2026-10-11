// Package probe checks whether services are reachable and, when they are
// not, how they are being blocked. Every probe opens a fresh connection,
// because DPI state is per flow.
package probe

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"prosvet/internal/netroute"
)

// Block classes, from cheapest to most stubborn.
const (
	ClassOK      = "ok"
	ClassDNS     = "dns"      // name does not resolve or resolves to a stub
	ClassIP      = "ip-block" // TCP connect / UDP times out: address blackholed
	ClassReset   = "reset"    // RST during TLS handshake: classic SNI filter
	ClassDrop    = "drop"     // handshake silently dropped
	ClassFreeze  = "freeze"   // data stops after ~16-20 KB (TSPU "16-20")
	ClassTLS     = "tls"      // certificate/protocol error (MITM or stub page)
	ClassLeak    = "leak"     // a decoy reached the server: the fooling method fails here
	ClassNoReply = "no-reply" // UDP: nothing came back
	ClassError   = "error"
)

// ClassText is a short Russian explanation of a class.
var ClassText = map[string]string{
	ClassOK:      "доступно",
	ClassDNS:     "не резолвится / подмена DNS",
	ClassIP:      "адрес недоступен (блок по IP)",
	ClassReset:   "сброс соединения по SNI",
	ClassDrop:    "рукопожатие TLS теряется",
	ClassFreeze:  "заморозка после 16–20 КБ",
	ClassTLS:     "ошибка TLS — заглушка или подмена",
	ClassLeak:    "фейк дошёл до сервера — этот способ отсева тут не работает",
	ClassNoReply: "UDP без ответа",
	ClassError:   "ошибка",
}

// Kinds of targets.
const (
	KindHTTPS     = "https"
	KindHTTPSPost = "https-post"
	KindQUIC      = "quic"
	KindUDPEcho   = "udp-echo"
	KindSTUN      = "stun"
)

// Target is one endpoint to check.
type Target struct {
	ID       string `json:"id"`
	Group    string `json:"group"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	Path     string `json:"path,omitempty"`
	MinBytes int    `json:"minBytes,omitempty"`
	// Discover replaces Host at run time (e.g. the nearest googlevideo node).
	Discover func(ctx context.Context) (string, error) `json:"-"`
}

// Result is the outcome of one probe.
type Result struct {
	Target  string        `json:"target"`
	Group   string        `json:"group"`
	Host    string        `json:"host"`
	OK      bool          `json:"ok"`
	Class   string        `json:"class"`
	Latency time.Duration `json:"latency"`
	Bytes   int64         `json:"bytes"`
	Detail  string        `json:"detail,omitempty"`
	At      time.Time     `json:"at"`
}

// Options tune timeouts.
type Options struct {
	Timeout time.Duration // whole probe
	Connect time.Duration // TCP connect / first UDP reply
	// Bind sends probes through one interface: with a VPN as the default
	// route, around it — the path where DPI (and the engine) actually act.
	Bind netroute.Bind
}

func (o Options) udp(ctx context.Context, addr string) (net.Conn, error) {
	d := net.Dialer{Control: o.Bind.Control}
	return d.DialContext(ctx, "udp", addr)
}

func (o Options) withDefaults() Options {
	if o.Timeout == 0 {
		o.Timeout = 6 * time.Second
	}
	if o.Connect == 0 {
		o.Connect = 3 * time.Second
	}
	return o
}

// Run executes one probe.
func Run(ctx context.Context, t Target, o Options) Result {
	o = o.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, o.Timeout+time.Second)
	defer cancel()
	host := t.Host
	if t.Discover != nil {
		if h, err := t.Discover(ctx); err == nil && h != "" {
			host = h
		}
	}
	r := Result{Target: t.ID, Group: t.Group, Host: host, At: time.Now()}
	switch t.Kind {
	case KindHTTPS, KindHTTPSPost:
		httpsProbe(ctx, t, host, o, &r)
	case KindQUIC:
		quicProbe(ctx, host, t.Port, o, &r)
	case KindUDPEcho, KindSTUN:
		udpProbe(ctx, t, host, o, &r)
	default:
		r.Class, r.Detail = ClassError, "unknown probe kind"
	}
	r.OK = r.Class == ClassOK
	return r
}

// RunAll probes targets concurrently and returns results in input order.
func RunAll(ctx context.Context, targets []Target, o Options, parallel int) []Result {
	if parallel <= 0 {
		parallel = 12
	}
	out := make([]Result, len(targets))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = Run(ctx, t, o)
		}()
	}
	wg.Wait()
	return out
}

// resolve looks a name up with the system resolver and flags obvious DNS
// poisoning (bogon/stub answers).
func resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var good []net.IP
	for _, a := range addrs {
		if a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() || a.IsLinkLocalUnicast() {
			continue
		}
		good = append(good, a)
	}
	if len(good) == 0 {
		return nil, errors.New("DNS вернул заглушку " + addrs[0].String())
	}
	// Prefer IPv4: IPv6 is often not intercepted and would skew results.
	slices.SortStableFunc(good, func(a, b net.IP) int {
		a4, b4 := a.To4() != nil, b.To4() != nil
		switch {
		case a4 && !b4:
			return -1
		case !a4 && b4:
			return 1
		}
		return 0
	})
	return good, nil
}

func isReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNABORTED) || strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "forcibly closed")
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout())
}

// leakSignatures are TLS errors that appear when the server answered a decoy
// ClientHello (or choked on a non-TLS decoy) instead of ours.
var leakSignatures = []string{
	"did not echo the legacy session ID",
	"first record does not look like a TLS handshake",
	"server selected unadvertised",
	"unexpected ServerHello",
	"bad record MAC",
	"received unexpected handshake message",
}

func isLeak(err error) bool {
	s := err.Error()
	for _, sig := range leakSignatures {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
