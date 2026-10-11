package tui

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"prosvet/internal/strategy"
	"prosvet/internal/tgproxy"
)

// LocalTelegramSecret returns the per-user secret for a proxy run without
// the service, so the link added to Telegram keeps working between runs.
func LocalTelegramSecret() []byte {
	dir, err := os.UserConfigDir()
	if err != nil {
		return tgproxy.NewSecret()
	}
	p := filepath.Join(dir, "prosvet", "telegram.secret")
	if b, err := os.ReadFile(p); err == nil {
		if s, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(s) == 16 {
			return s
		}
	}
	s := tgproxy.NewSecret()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(hex.EncodeToString(s)+"\n"), 0o600)
	return s
}

// OpenURL hands a link to the desktop (tg:// opens Telegram's "add proxy").
func OpenURL(u string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		return exec.Command("open", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}

func (m model) telegramLink() string {
	if m.st != nil && m.st.Telegram != nil && m.st.Telegram.Running {
		return m.st.Telegram.Link
	}
	if m.tg != nil {
		return m.tg.Link()
	}
	return ""
}

func (m model) keyTelegram(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "e", "у", " ":
		if m.be != nil && m.st != nil {
			on := !m.st.Services[strategy.SvcTelegram]
			text := map[bool]string{true: "Telegram-прокси включён", false: "Telegram-прокси выключен"}[on]
			return m, m.act(text, func(ctx context.Context, be Backend) error {
				if err := be.SetService(ctx, strategy.SvcTelegram, on); err != nil {
					return err
				}
				if on && !m.st.Enabled {
					return be.Start(ctx)
				}
				return nil
			})
		}
		// No service: run the proxy inside this window, no rights needed.
		if m.tg != nil {
			m.tgCancel()
			m.tg, m.tgCancel = nil, nil
			return m, toast("Telegram-прокси остановлен", nil)
		}
		if m.tgSecret == nil {
			m.tgSecret = LocalTelegramSecret()
		}
		ctx, cancel := context.WithCancel(context.Background())
		p := tgproxy.New(tgproxy.Config{Listen: "127.0.0.1:1443", Secret: m.tgSecret, Direct: tgproxy.AroundVPN})
		m.tg, m.tgCancel = p, cancel
		go func() { _ = p.Serve(ctx) }()
		return m, toast("Telegram-прокси работает, пока открыто это окно", nil)
	case "o", "щ", "enter":
		link := m.telegramLink()
		if link == "" {
			return m, toast("", fmt.Errorf("сначала включите прокси: e"))
		}
		if err := OpenURL(link); err != nil {
			return m, toast("", err)
		}
		return m, toast("ссылка отправлена в Telegram — подтвердите «Подключить прокси»", nil)
	}
	return m, nil
}

func (m model) viewTelegram(w, h int) string {
	var (
		running bool
		listen  string
		stats   tgproxy.Stats
		routes  map[string][]tgproxy.Route
	)
	switch {
	case m.st != nil && m.st.Telegram != nil && m.st.Telegram.Running:
		t := m.st.Telegram
		running, listen, stats, routes = true, t.Listen, t.Stats, t.Routes
	case m.tg != nil:
		running, listen, stats, routes = true, "127.0.0.1:1443", m.tg.Snapshot(), m.tg.Routes()
	}
	how := sMuted.Render("Telegram Desktop подключается к локальному прокси, а тот ведёт\nтрафик к серверам Telegram через WebSocket (как web.telegram.org) —\nэто не блокируется. Если WebSocket недоступен — запасной путь по TCP.")
	if !running {
		body := how + "\n\n"
		if m.be != nil {
			body += keyHint("e", "включить") + sMuted.Render("  прокси будет запускаться вместе с обходом")
		} else {
			body += keyHint("e", "запустить здесь") + sMuted.Render("  права администратора не нужны")
		}
		return panel("Telegram", w, body)
	}
	link := m.telegramLink()
	card := dot("ok") + sBold.Render(" прокси работает") + sMuted.Render("  "+listen) + "\n\n" +
		sMuted.Render("Добавить в Telegram: ") + keyHint("o", "открыть ссылку") + sMuted.Render("  или вручную:") + "\n" +
		sBeam.Render(truncate(link, w-6)) + "\n\n" +
		sMuted.Render("Настройки → Продвинутые → Тип соединения → MTProto, сервер 127.0.0.1, порт "+portOf(listen))
	st := fmt.Sprintf("%s %d   %s %d   %s %d   %s %d   %s %d",
		sMuted.Render("активных"), stats.Active, sMuted.Render("всего"), stats.Total,
		sMuted.Render("WebSocket"), stats.WS, sMuted.Render("через фронт"), stats.Fronted, sMuted.Render("TCP"), stats.TCP)
	st += "\n" + sMuted.Render("принято ") + sInk.Render(bytesH(stats.Down)) + sMuted.Render("   отправлено ") + sInk.Render(bytesH(stats.Up))
	if stats.Failed > 0 {
		st += sBlock.Render(fmt.Sprintf("   ошибок %d", stats.Failed))
	}
	var keys []string
	for k := range routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rr []string
	for _, k := range keys {
		for i, r := range routes[k] {
			if i >= 2 {
				break
			}
			path := sFaint.Render("по умолчанию")
			if m.st != nil && m.st.Route.VPN {
				path = sPartial.Render("через " + m.st.Route.App)
			}
			if r.Via != "" {
				path = sBeam.Render("напрямую " + r.Via)
			}
			via := sMuted.Render(truncate(r.Domain, 30))
			if r.Front {
				via += sViolet.Render(" ⇢ фронт")
			}
			rtt := sFaint.Render("—")
			if r.RTT > 0 {
				rtt = sBeam.Render(fmt.Sprintf("%d мс", r.RTT.Milliseconds()))
			}
			line := pad(sInk.Render("DC"+k), 8) + pad(rtt, 9) + pad(path, 20) + pad(via, 40) + sFaint.Render(r.IP)
			if r.Fails > 0 {
				line += sBlock.Render(fmt.Sprintf("  сбоев %d", r.Fails))
			}
			rr = append(rr, line)
		}
	}
	if len(rr) == 0 {
		rr = append(rr, sMuted.Render("маршруты ещё измеряются…"))
	}
	return panel("Telegram", w, card) + "\n" + panel("Трафик", w, st) + "\n" + panel("Пути к дата-центрам", w, strings.Join(rr, "\n"))
}

func portOf(listen string) string {
	if i := strings.LastIndexByte(listen, ':'); i >= 0 {
		return listen[i+1:]
	}
	return listen
}
