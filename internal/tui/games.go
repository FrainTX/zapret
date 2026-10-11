package tui

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"prosvet/internal/core"
	"prosvet/internal/games"
	"prosvet/internal/netroute"
)

func (m model) keyGames(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "r", "к":
		if m.measuring {
			return m, nil
		}
		m.measuring, m.measured = true, 0
		return m, measureCmd()
	case "p", "з":
		if len(m.siege) == 0 {
			return m, toast("", fmt.Errorf("сначала замерьте пинг: r"))
		}
		if len(m.configs) == 0 {
			return m, toast("", fmt.Errorf("настройки Siege не найдены — запустите игру хотя бы раз"))
		}
		cur := m.configs[0].Hint
		best, ok := games.Pick(m.siege, cur)
		if !ok {
			return m, toast("", fmt.Errorf("ни один дата-центр не ответил"))
		}
		cfgs := m.configs
		return m, func() tea.Msg {
			for _, c := range cfgs {
				if err := games.SetHint(c.Path, games.HintValue(best.Region.ID)); err != nil {
					return toastMsg{err: err}
				}
			}
			return toastMsg{text: fmt.Sprintf("Siege закреплён за %s (%s) — перезапустите игру", best.Region.Title, games.Ms(best.RTT))}
		}
	case "u", "г":
		cfgs := m.configs
		return m, func() tea.Msg {
			n := 0
			for _, c := range cfgs {
				if c.Backup {
					if err := games.Restore(c.Path); err != nil {
						return toastMsg{err: err}
					}
					n++
				}
			}
			if n == 0 {
				return toastMsg{err: fmt.Errorf("Просвет ещё ничего не менял в настройках Siege")}
			}
			return toastMsg{text: "настройки Siege возвращены как были"}
		}
	case "v", "м":
		if runtime.GOOS != "linux" {
			return m, toast("", fmt.Errorf("на Windows исключите игры в самом VPN-клиенте — список ниже"))
		}
		v := "off"
		text := "игры снова идут через VPN"
		if m.st != nil && m.st.GameDirect == "off" {
			v, text = "auto", "игры пойдут мимо VPN"
		}
		return m, m.act(text, func(ctx context.Context, be Backend) error { return be.Settings(ctx, core.Settings{GameDirect: &v}) })
	}
	return m, nil
}

func measureCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		via := games.DirectVia(netroute.Detect())
		ss := games.Measure(ctx, games.SiegeRegions, 7, via, nil)
		us := games.Measure(ctx, games.UDPBeacons, 5, games.Via{}, nil)
		var ud []games.Sample
		if via != (games.Via{}) {
			ud = games.Measure(ctx, games.UDPBeacons, 5, via, nil)
		}
		return samplesMsg{siege: ss, udp: us, udpDirect: ud}
	}
}

func (m model) viewGames(w, h int) string {
	var route netroute.Info
	direct, gameDirect := false, ""
	if m.st != nil {
		route, direct, gameDirect = m.st.Route, m.st.Direct, m.st.GameDirect
	} else {
		route = netroute.Detect()
	}
	wide := w >= 110
	leftW := w
	if wide {
		leftW = w * 58 / 100
	}
	dc := panel("Дата-центры Rainbow Six Siege", leftW, m.regionTable(m.siege, leftW-4, true))
	side := lipgloss.JoinVertical(lipgloss.Left,
		panel("Путь пакетов", ifW(wide, w-leftW, w), m.routePanel(route, direct, gameDirect)),
		panel("Игровой UDP · AWS", ifW(wide, w-leftW, w), m.udpPanel(route, ifW(wide, w-leftW, w)-4)),
		panel("Настройки игры", ifW(wide, w-leftW, w), m.siegePanel(ifW(wide, w-leftW, w)-4)),
	)
	if wide {
		return lipgloss.JoinHorizontal(lipgloss.Top, dc, side)
	}
	return lipgloss.JoinVertical(lipgloss.Left, dc, side)
}

func ifW(c bool, a, b int) int {
	if c {
		return a
	}
	return b
}

func (m model) regionTable(ss []games.Sample, w int, siege bool) string {
	if m.measuring {
		return gradient("замеряю задержку…", m.phase*3, false) + "\n" + sMuted.Render("каждый регион — 7 замеров, чтобы отсечь случайные всплески")
	}
	if len(ss) == 0 {
		if siege {
			return sMuted.Render("Нажмите ") + sBeam.Render("r") + sMuted.Render(" — Просвет измерит путь до каждого региона PlayFab,\nгде живут серверы Siege, и покажет самый быстрый.\n\nFor Honor выбирает сервер сам по пингу — ему важны\nчистый путь (мимо VPN) и лёгкий рецепт обхода.")
		}
		return sMuted.Render("r — замерить")
	}
	hi := 0.0
	for _, s := range ss {
		if s.OK() {
			hi = max(hi, float64(s.RTT.Milliseconds()))
		}
	}
	hi = max(hi, 60)
	nameW := 30
	if !siege {
		nameW = 16
	}
	barW := max(6, w-nameW-26)
	var rows []string
	cur := ""
	if len(m.configs) > 0 {
		cur = games.Short(m.configs[0].Hint)
	}
	best, _ := games.Pick(ss, "")
	for i, s := range ss {
		if siege && i >= 13 {
			break
		}
		name := s.Region.Title
		mark := "  "
		if s.Region.ID == best.Region.ID && s.OK() {
			mark = sBeamB.Render("★ ")
		}
		if siege && s.Region.ID == cur {
			name += " ◂"
		}
		if !s.OK() {
			reason := s.Err
			if reason == "" {
				reason = fmt.Sprintf("потери %.0f%%", s.Loss()*100)
			}
			rows = append(rows, mark+pad(sFaint.Render(truncate(name, nameW)), nameW)+" "+sBlock.Render(truncate(reason, barW+20)))
			continue
		}
		v := float64(s.RTT.Milliseconds())
		st := sBeam
		switch {
		case v > 150:
			st = sBlock
		case v > 80:
			st = sPartial
		}
		extra := sFaint.Render(fmt.Sprintf(" ±%d", s.Jitter.Milliseconds()))
		if s.Loss() > 0 {
			extra += sPartial.Render(fmt.Sprintf(" %.0f%%↓", s.Loss()*100))
		}
		rows = append(rows, mark+pad(sInk.Render(truncate(name, nameW)), nameW)+" "+bar(v, hi, barW, st)+" "+pad(st.Render(games.Ms(s.RTT)), 7)+extra)
	}
	if siege && best.OK() && best.RTT < 2*time.Millisecond {
		rows = append(rows, "", sPartial.Render("Задержка подозрительно мала: соединения перехватывает локальный прокси/VPN —\nцифры не отражают путь до серверов."))
	}
	return strings.Join(rows, "\n")
}

// udpPanel compares game-like UDP through the VPN and around it.
func (m model) udpPanel(r netroute.Info, w int) string {
	if len(m.udpDirect) == 0 {
		return m.regionTable(m.udp, w, false)
	}
	if m.measuring {
		return gradient("замеряю…", m.phase*3, false)
	}
	byID := map[string]games.Sample{}
	for _, s := range m.udpDirect {
		byID[s.Region.ID] = s
	}
	txt := func(s games.Sample) string {
		if !s.OK() {
			return sBlock.Render("нет")
		}
		return games.Ms(s.RTT)
	}
	rows := []string{pad(sFaint.Render(""), 16) + pad(sMuted.Render("через "+r.App), 12) + sMuted.Render("напрямую")}
	for _, s := range m.udp {
		d := byID[s.Region.ID]
		line := pad(sInk.Render(truncate(s.Region.Title, 15)), 16) + pad(sPartial.Render(txt(s)), 12) + pad(sBeam.Render(txt(d)), 9)
		if s.OK() && d.OK() && s.RTT > d.RTT {
			line += sBeamB.Render(fmt.Sprintf(" −%d мс", (s.RTT - d.RTT).Milliseconds()))
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}

func (m model) routePanel(r netroute.Info, direct bool, gameDirect string) string {
	switch {
	case r.Iface == "":
		return sMuted.Render("не удалось определить маршрут")
	case !r.VPN:
		return dot("ok") + sInk.Render(" напрямую через "+r.Iface) + "\n" + sMuted.Render("Игры идут кратчайшим путём; обход DPI\nтрогает только первые пакеты каждого потока.")
	case direct:
		return sInk.Render("VPN "+r.App+" · "+r.Iface) + "\n" + dot("ok") + sBeam.Render(" игры идут мимо VPN") + sMuted.Render(" → "+r.Phys+" (шлюз "+r.Gateway.String()+")") +
			"\n" + sMuted.Render("Без лишнего круга через сервер VPN.")
	case runtime.GOOS == "linux" && r.CanBypass():
		s := sInk.Render("VPN "+r.App+" · "+r.Iface) + "\n" + sPartial.Render("◐ игры идут через VPN") + sMuted.Render(" — каждый пакет\nделает крюк через сервер VPN.")
		if gameDirect == "off" {
			return s + "\n" + keyHint("v", "пустить игры мимо VPN")
		}
		return s + "\n" + sMuted.Render("Включится вместе с обходом (цели R6/For Honor).")
	case r.VPN:
		return sInk.Render("VPN "+r.App+" · "+r.Iface) + "\n" + sPartial.Render("◐ игры идут через VPN") + "\n" +
			sMuted.Render("Добавьте в исключения (прямое подключение)\nв настройках "+r.App+":\n") + sBeam.Render("  "+strings.Join(netroute.GameExes, "\n  "))
	}
	return ""
}

func (m model) siegePanel(w int) string {
	if len(m.configs) == 0 {
		return sMuted.Render("GameSettings.ini не найден — запустите Siege\nхотя бы раз (Windows, Steam/Proton, Heroic, Lutris).")
	}
	var rows []string
	for _, c := range m.configs {
		hint := games.Short(c.Hint)
		if r, ok := games.RegionByID(hint); ok {
			hint = r.Title
		}
		if c.Hint == "" || c.Hint == "default" {
			hint = "авто (игра выбирает сама)"
		}
		mark := ""
		if c.Backup {
			mark = sFaint.Render(" · есть копия")
		}
		rows = append(rows, sMuted.Render("дата-центр: ")+sInk.Render(hint)+mark, sFaint.Render(truncate(shortPath(c.Path), w)))
	}
	return strings.Join(rows, "\n")
}

func shortPath(p string) string {
	if i := strings.Index(p, "My Games"); i > 0 {
		return "…" + p[i-1:]
	}
	return p
}
