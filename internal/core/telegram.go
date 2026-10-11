package core

import (
	"context"
	"encoding/hex"
	"net"
	"strconv"
	"time"

	"prosvet/internal/probe"
	"prosvet/internal/strategy"
	"prosvet/internal/tgproxy"
)

// TelegramStatus is the state of the local MTProto proxy.
type TelegramStatus struct {
	Running bool                       `json:"running"`
	Listen  string                     `json:"listen"`
	Link    string                     `json:"link"`
	Secret  string                     `json:"secret"`
	Stats   tgproxy.Stats              `json:"stats"`
	Routes  map[string][]tgproxy.Route `json:"routes,omitempty"`
}

// telegramSecretLocked returns (creating once) the persistent proxy secret.
func (c *Controller) telegramSecretLocked() []byte {
	if b, err := hex.DecodeString(c.cfg.TelegramSecret); err == nil && len(b) == 16 {
		return b
	}
	s := tgproxy.NewSecret()
	c.cfg.TelegramSecret = hex.EncodeToString(s)
	_ = c.save()
	return s
}

func (c *Controller) telegramListenLocked() string {
	port := c.cfg.TelegramPort
	if port <= 0 || port > 65535 {
		port = 1443
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// syncTelegramLocked runs the proxy exactly when the Telegram target is on
// and the bypass is enabled.
func (c *Controller) syncTelegramLocked() {
	want := c.cfg.Enabled && c.cfg.Services[strategy.SvcTelegram]
	if want && c.tg == nil {
		ctx, cancel := context.WithCancel(context.Background())
		p := tgproxy.New(tgproxy.Config{
			Listen: c.telegramListenLocked(), Secret: c.telegramSecretLocked(),
			Logf:   func(f string, a ...any) { c.Log.Warnf("telegram", f, a...) },
			Direct: tgproxy.AroundVPN,
		})
		c.tg, c.tgCancel = p, cancel
		go func() {
			if err := p.Serve(ctx); err != nil {
				c.Log.Errorf("telegram", "прокси: %v", err)
			}
		}()
		c.Log.Infof("telegram", "прокси для Telegram Desktop: %s", p.Link())
	}
	if !want && c.tg != nil {
		c.tgCancel()
		c.tg, c.tgCancel = nil, nil
		c.Log.Infof("telegram", "прокси остановлен")
	}
}

func (c *Controller) telegramStatusLocked() *TelegramStatus {
	if !c.cfg.Services[strategy.SvcTelegram] {
		return nil
	}
	st := &TelegramStatus{Listen: c.telegramListenLocked(), Secret: c.cfg.TelegramSecret}
	if c.tg != nil {
		st.Running = true
		st.Link = c.tg.Link()
		st.Stats = c.tg.Snapshot()
		st.Routes = c.tg.Routes()
	}
	return st
}

// telegramProbe checks the proxy end to end (req_pq_multi through it).
func (c *Controller) telegramProbe(ctx context.Context) []probe.Result {
	c.mu.Lock()
	running := c.tg != nil
	addr := c.telegramListenLocked()
	secret := c.telegramSecretLocked()
	c.mu.Unlock()
	if !running {
		return nil
	}
	var out []probe.Result
	for _, dc := range []int{2, 4} {
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		rtt, err := tgproxy.SelfTest(tctx, addr, secret, dc)
		cancel()
		r := probe.Result{Target: "tg-dc" + strconv.Itoa(dc), Group: "telegram", Host: "Telegram DC" + strconv.Itoa(dc) + " через прокси",
			OK: err == nil, Class: probe.ClassOK, Latency: rtt, At: time.Now(), Detail: "ответ сервера Telegram"}
		if err != nil {
			r.Class, r.Detail = probe.ClassNoReply, err.Error()
		}
		out = append(out, r)
	}
	return out
}
