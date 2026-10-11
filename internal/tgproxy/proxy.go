package tgproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"prosvet/internal/netroute"
)

// DefaultDCIPs are the classic data-center addresses (direct TCP fallback).
var DefaultDCIPs = map[int]string{
	1: "149.154.175.50", 2: "149.154.167.51", 3: "149.154.175.100",
	4: "149.154.167.91", 5: "149.154.171.5", 203: "91.105.192.100",
}

// WebIP is the web.telegram.org front-end that accepts WebSocket for every
// DC and is not throttled like the DC addresses themselves.
const WebIP = "149.154.167.220"

// Config configures the proxy.
type Config struct {
	Listen   string // 127.0.0.1:1443
	Secret   []byte // 16 bytes
	PoolSize int    // idle WebSockets kept per DC
	Logf     func(format string, a ...any)
	// Direct, if set, names an interface around a VPN: every route is then
	// also tried through it, and the ranking keeps whichever path works —
	// a flaky VPN no longer takes stickers and media down with it.
	Direct func() netroute.Bind
}

// Stats are live counters.
type Stats struct {
	Active, Total, WS, Fronted, TCP, Failed int64
	Up, Down                                int64
}

// Route is one way to reach a DC, ranked by measured setup time.
type Route struct {
	IP     string        `json:"ip"`
	Domain string        `json:"domain"`
	Front  bool          `json:"front"`
	Via    string        `json:"via,omitempty"` // interface, "" = default route
	RTT    time.Duration `json:"rtt"`
	Fails  int           `json:"fails"`
}

// Proxy is a running local MTProto proxy.
type Proxy struct {
	cfg   Config
	ln    net.Listener
	stats Stats

	mu     sync.Mutex
	routes map[string][]*Route // key "dc/m", ranked
	pools  map[string][]*WS
	dcs    map[string]*dcState
}

// NewSecret returns a random 16-byte secret.
func NewSecret() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

// New creates a proxy.
func New(cfg Config) *Proxy {
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 3
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Proxy{cfg: cfg, routes: map[string][]*Route{}, pools: map[string][]*WS{}, dcs: map[string]*dcState{}}
}

// Link is the tg:// link that configures Telegram Desktop in one click.
func (p *Proxy) Link() string {
	host, port, _ := net.SplitHostPort(p.cfg.Listen)
	return "tg://proxy?server=" + host + "&port=" + port + "&secret=dd" + hex.EncodeToString(p.cfg.Secret)
}

// Snapshot returns the counters.
func (p *Proxy) Snapshot() Stats {
	return Stats{
		Active: atomic.LoadInt64(&p.stats.Active), Total: atomic.LoadInt64(&p.stats.Total),
		WS: atomic.LoadInt64(&p.stats.WS), Fronted: atomic.LoadInt64(&p.stats.Fronted),
		TCP: atomic.LoadInt64(&p.stats.TCP), Failed: atomic.LoadInt64(&p.stats.Failed),
		Up: atomic.LoadInt64(&p.stats.Up), Down: atomic.LoadInt64(&p.stats.Down),
	}
}

// Routes returns the measured routes per DC.
func (p *Proxy) Routes() map[string][]Route {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string][]Route{}
	for k, rs := range p.routes {
		for _, r := range rs {
			out[k] = append(out[k], *r)
		}
	}
	return out
}

func key(dc int, media bool) string {
	if media {
		return strconv.Itoa(dc) + "/m"
	}
	return strconv.Itoa(dc)
}

// Serve accepts clients until ctx ends.
func (p *Proxy) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.cfg.Listen)
	if err != nil {
		return err
	}
	p.ln = ln
	go func() { <-ctx.Done(); ln.Close() }()
	// Warm up the DCs Telegram Desktop uses first.
	for _, dc := range []int{2, 4} {
		go p.refill(dc, false)
	}
	go p.reprobeLoop(ctx)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		go p.handle(ctx, c)
	}
}

// reprobeLoop re-ranks routes every few minutes so the fastest path wins as
// network conditions change.
func (p *Proxy) reprobeLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.mu.Lock()
			var keys []string
			for k := range p.routes {
				keys = append(keys, k)
			}
			p.mu.Unlock()
			for _, k := range keys {
				var dc int
				media := len(k) > 2 && k[len(k)-2:] == "/m"
				fmt.Sscanf(k, "%d", &dc)
				st := p.state(k)
				p.mu.Lock()
				busy := st.probing
				st.probing = true
				p.mu.Unlock()
				if !busy {
					p.probeRoutes(dc, media)
				}
			}
		}
	}
}

func (p *Proxy) handle(ctx context.Context, c net.Conn) {
	atomic.AddInt64(&p.stats.Active, 1)
	atomic.AddInt64(&p.stats.Total, 1)
	defer atomic.AddInt64(&p.stats.Active, -1)
	defer c.Close()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	init := make([]byte, initLen)
	if _, err := io.ReadFull(c, init); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	cs, err := AcceptInit(init, p.cfg.Secret)
	if err != nil {
		atomic.AddInt64(&p.stats.Failed, 1)
		p.cfg.Logf("telegram: неверный секрет или не MTProto")
		return
	}
	dcIdx := cs.DC
	if cs.Media {
		dcIdx = -dcIdx
	}
	dc := cs.DC
	if dc >= 10000 {
		dc -= 10000
	}
	ts := NewInit(cs.Tag, dcIdx)

	dctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	ws, route, err := p.dial(dctx, dc, cs.Media)
	cancel()
	if err != nil {
		p.tcpFallback(c, cs, ts, dc)
		return
	}
	atomic.AddInt64(&p.stats.WS, 1)
	if route != nil && route.Front {
		atomic.AddInt64(&p.stats.Fronted, 1)
	}
	defer ws.Close()
	if err := ws.Send(ts.Init); err != nil {
		return
	}
	sp := NewSplitter(cs.Tag)
	done := make(chan struct{}, 2)
	go func() { // client -> Telegram
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 64*1024)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				atomic.AddInt64(&p.stats.Up, int64(n))
				plain := make([]byte, n)
				cs.Dec.XORKeyStream(plain, buf[:n])
				enc := make([]byte, n)
				ts.Enc.XORKeyStream(enc, plain)
				if parts := sp.Push(enc, plain); len(parts) > 0 {
					if ws.Send(parts...) != nil {
						return
					}
				}
			}
			if err != nil {
				if t := sp.Flush(); t != nil {
					_ = ws.Send(t)
				}
				return
			}
		}
	}()
	go func() { // Telegram -> client
		defer func() { done <- struct{}{} }()
		for {
			m, err := ws.Recv()
			if err != nil {
				return
			}
			atomic.AddInt64(&p.stats.Down, int64(len(m)))
			ts.Dec.XORKeyStream(m, m)
			cs.Enc.XORKeyStream(m, m)
			if _, err := c.Write(m); err != nil {
				return
			}
		}
	}()
	<-done
}

// tcpFallback connects to the DC directly (works where only WS is blocked).
func (p *Proxy) tcpFallback(c net.Conn, cs *ClientSide, ts *TelegramSide, dc int) {
	ip := DefaultDCIPs[dc]
	if ip == "" {
		atomic.AddInt64(&p.stats.Failed, 1)
		return
	}
	r, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "443"), 6*time.Second)
	if err != nil {
		atomic.AddInt64(&p.stats.Failed, 1)
		p.cfg.Logf("telegram: DC%d недоступен ни через WebSocket, ни напрямую", dc)
		return
	}
	defer r.Close()
	atomic.AddInt64(&p.stats.TCP, 1)
	if _, err := r.Write(ts.Init); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	pipe := func(src, dst net.Conn, a, b interface{ XORKeyStream(dst, src []byte) }, ctr *int64) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 64*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				atomic.AddInt64(ctr, int64(n))
				a.XORKeyStream(buf[:n], buf[:n])
				b.XORKeyStream(buf[:n], buf[:n])
				if _, err := dst.Write(buf[:n]); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pipe(c, r, cs.Dec, ts.Enc, &p.stats.Up)
	go pipe(r, c, ts.Dec, cs.Enc, &p.stats.Down)
	<-done
}
