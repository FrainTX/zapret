package tgproxy

import (
	"context"
	"errors"
	"net"
	"os"
	"sort"
	"strconv"
	"time"

	"prosvet/internal/netroute"
)

// Telegram Desktop opens dozens of connections at once when a sticker set
// or the emoji panel loads — mostly to the media DCs. Letting every one of
// them race every route (and refill the pool) turns that into hundreds of
// simultaneous TLS+WebSocket handshakes, which the path (and Telegram's
// front ends) answers with failures. So per DC there is one candidate list,
// one background ranking, one pool refill at a time, a cap on concurrent
// handshakes, and each connection walks the routes "happy eyeballs" style.

const (
	maxHandshakes = 8                       // concurrent WS handshakes per DC
	stagger       = 1200 * time.Millisecond // head start of a route before the next one joins
)

type dcState struct {
	sem       chan struct{}
	cands     []Route
	probing   bool
	refilling bool
}

func (p *Proxy) state(k string) *dcState {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.dcs[k]
	if st == nil {
		st = &dcState{sem: make(chan struct{}, maxHandshakes)}
		p.dcs[k] = st
	}
	return st
}

func wsDomains(dc int, media bool) []string {
	if dc == 203 {
		dc = 2
	}
	d := "kws" + strconv.Itoa(dc) + ".web.telegram.org"
	if media {
		return []string{"kws" + strconv.Itoa(dc) + "-1.web.telegram.org", d}
	}
	return []string{d}
}

// candidates lists every route worth trying for a DC: the web front end,
// the addresses the names resolve to, then domain fronting — through the
// default route and, with a VPN up, also around it.
func (p *Proxy) candidates(ctx context.Context, dc int, media bool) []Route {
	base := candidates(ctx, dc, media)
	if p.cfg.Direct == nil {
		return base
	}
	b := p.cfg.Direct()
	if b.IsZero() {
		return base
	}
	out := append([]Route(nil), base...)
	for _, r := range base {
		r.Via = b.Iface
		if r.Via == "" {
			r.Via = "#" + strconv.Itoa(b.Index)
		}
		out = append(out, r)
	}
	return out
}

func candidates(ctx context.Context, dc int, media bool) []Route {
	var out []Route
	seen := map[string]bool{}
	add := func(ip, domain string, front bool) {
		k := ip + domain + strconv.FormatBool(front)
		if !seen[k] {
			seen[k] = true
			out = append(out, Route{IP: ip, Domain: domain, Front: front})
		}
	}
	for _, d := range wsDomains(dc, media) {
		add(WebIP, d, false)
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, _ := net.DefaultResolver.LookupIP(rctx, "ip4", d)
		cancel()
		for _, ip := range ips {
			add(ip.String(), d, false)
		}
	}
	for _, d := range wsDomains(dc, media) {
		add(WebIP, d, true)
	}
	return out
}

func sameRoute(a, b Route) bool {
	return a.IP == b.IP && a.Domain == b.Domain && a.Front == b.Front && a.Via == b.Via
}

// order returns the routes to try, best first: the measured ranking when
// there is one (working routes by setup time, failing ones last), else the
// candidate list — and starts the ranking in the background.
func (p *Proxy) order(ctx context.Context, dc int, media bool) []Route {
	k := key(dc, media)
	st := p.state(k)
	p.mu.Lock()
	var rs []Route
	for _, r := range p.routes[k] {
		rs = append(rs, *r)
	}
	cands := st.cands
	p.mu.Unlock()
	if len(rs) > 0 {
		sort.SliceStable(rs, func(i, j int) bool {
			fi, fj := min(rs[i].Fails, 3), min(rs[j].Fails, 3)
			if fi != fj {
				return fi < fj
			}
			return rs[i].RTT < rs[j].RTT
		})
		return rs
	}
	if cands == nil {
		cands = p.candidates(ctx, dc, media)
		p.mu.Lock()
		if st.cands == nil {
			st.cands = cands
		}
		start := !st.probing
		st.probing = true
		p.mu.Unlock()
		if start {
			go p.probeRoutes(dc, media)
		}
	}
	return cands
}

// handshake opens one WebSocket, within the DC's concurrency cap.
func (p *Proxy) handshake(ctx context.Context, st *dcState, r Route) (*WS, time.Duration, error) {
	select {
	case st.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	defer func() { <-st.sem }()
	start := time.Now()
	w, err := DialWS(ctx, r.IP, r.Domain, r.Front, p.bindFor(r))
	return w, time.Since(start), err
}

// note records the outcome of a handshake in the ranking.
func (p *Proxy) note(k string, r Route, rtt time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.routes[k] {
		if sameRoute(*x, r) {
			if err != nil {
				x.Fails++
			} else {
				x.Fails = 0
				// smooth, so one slow handshake under load does not reorder
				if x.RTT > 0 {
					x.RTT = (3*x.RTT + rtt) / 4
				} else {
					x.RTT = rtt
				}
			}
			return
		}
	}
}

// eyeballs starts the best route, gives it a head start, then adds the next
// one, and so on; the first WebSocket that opens wins.
func (p *Proxy) eyeballs(ctx context.Context, dc int, media bool, routes []Route) (*WS, Route, error) {
	if len(routes) == 0 {
		return nil, Route{}, errors.New("tgproxy: no websocket route")
	}
	k := key(dc, media)
	st := p.state(k)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type res struct {
		w   *WS
		r   Route
		rtt time.Duration
		err error
	}
	ch := make(chan res, len(routes))
	next, running := 0, 0
	launch := func() {
		r := routes[next]
		next++
		running++
		go func() {
			w, rtt, err := p.handshake(ctx, st, r)
			ch <- res{w, r, rtt, err}
		}()
	}
	launch()
	timer := time.NewTimer(stagger)
	defer timer.Stop()
	var lastErr error
	for running > 0 {
		select {
		case x := <-ch:
			running--
			if x.err == nil {
				cancel()
				p.note(k, x.r, x.rtt, nil)
				go func(n int) { // close the losers that still open
					for ; n > 0; n-- {
						if y := <-ch; y.w != nil {
							y.w.Close()
						}
					}
				}(running)
				return x.w, x.r, nil
			}
			if ctx.Err() == nil {
				p.note(k, x.r, 0, x.err)
				lastErr = x.err
				if os.Getenv("PROSVET_TGDEBUG") != "" {
					p.cfg.Logf("ws DC%d %s via %s front=%v if=%q: %v", dc, x.r.Domain, x.r.IP, x.r.Front, x.r.Via, x.err)
				}
			}
			if next < len(routes) {
				launch()
				timer.Reset(stagger)
			}
		case <-timer.C:
			if next < len(routes) {
				launch()
				timer.Reset(stagger)
			}
		}
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return nil, Route{}, lastErr
}

// probeRoutes measures every candidate once and installs the ranking.
func (p *Proxy) probeRoutes(dc int, media bool) {
	k := key(dc, media)
	st := p.state(k)
	defer func() {
		p.mu.Lock()
		st.probing = false
		p.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cands := p.candidates(ctx, dc, media)
	out := make([]*Route, len(cands))
	done := make(chan struct{}, len(cands))
	for i, r := range cands {
		out[i] = &Route{IP: r.IP, Domain: r.Domain, Front: r.Front, Via: r.Via}
		go func() {
			defer func() { done <- struct{}{} }()
			w, rtt, err := p.handshake(ctx, st, r)
			if err != nil {
				out[i].Fails = 1
				return
			}
			out[i].RTT = rtt
			w.Close()
		}()
	}
	for range cands {
		<-done
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Fails == 0) != (out[j].Fails == 0) {
			return out[i].Fails == 0
		}
		return out[i].RTT < out[j].RTT
	})
	p.mu.Lock()
	p.routes[k] = out
	st.cands = cands
	p.mu.Unlock()
}

// dial gets a WebSocket for a DC: from the warm pool if possible, otherwise
// along the routes; one retry absorbs a momentary hiccup of the path.
func (p *Proxy) dial(ctx context.Context, dc int, media bool) (*WS, *Route, error) {
	k := key(dc, media)
	p.mu.Lock()
	for len(p.pools[k]) > 0 {
		w := p.pools[k][0]
		p.pools[k] = p.pools[k][1:]
		if time.Since(w.born) < 50*time.Second {
			p.mu.Unlock()
			go p.refill(dc, media)
			return w, nil, nil
		}
		w.Close()
	}
	p.mu.Unlock()
	go p.refill(dc, media)
	w, r, err := p.eyeballs(ctx, dc, media, p.order(ctx, dc, media))
	if err != nil && ctx.Err() == nil {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		w, r, err = p.eyeballs(ctx, dc, media, p.order(ctx, dc, media))
	}
	if err != nil {
		return nil, nil, err
	}
	return w, &r, nil
}

// refill tops the warm pool up; at most one refill runs per DC.
func (p *Proxy) refill(dc int, media bool) {
	k := key(dc, media)
	st := p.state(k)
	p.mu.Lock()
	if st.refilling {
		p.mu.Unlock()
		return
	}
	st.refilling = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		st.refilling = false
		p.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		p.mu.Lock()
		need := p.cfg.PoolSize - len(p.pools[k])
		p.mu.Unlock()
		if need <= 0 {
			return
		}
		w, _, err := p.eyeballs(ctx, dc, media, p.order(ctx, dc, media))
		if err != nil {
			return
		}
		p.mu.Lock()
		p.pools[k] = append(p.pools[k], w)
		p.mu.Unlock()
	}
}

// bindFor turns a route's interface into a socket binding.
func (p *Proxy) bindFor(r Route) netroute.Bind {
	if r.Via == "" || p.cfg.Direct == nil {
		return netroute.Bind{}
	}
	return p.cfg.Direct()
}

// AroundVPN is a Config.Direct that follows the machine's routing: with a
// VPN as the default route it offers the physical interface next to it.
func AroundVPN() netroute.Bind { return netroute.Detect().Direct() }
