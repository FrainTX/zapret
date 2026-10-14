package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"prosvet/internal/core"
	"prosvet/internal/strategy"
)

func panel(title string, w int, body string) string {
	t := sTitle.Render(strings.ToUpper(title))
	return sPanel.Width(max(10, w-2)).Render(t + "\n" + sFaint.Render(strings.Repeat("─", max(4, min(w-4, 72)))) + "\n" + body)
}

// tile is a small dashboard card: a caption, one big value, one remark.
func tile(title, value, sub string, w int) string {
	return sPanel.Width(max(12, w-2)).Render(sTitle.Render(strings.ToUpper(title)) + "\n" + value + "\n" + sub)
}

func (m model) viewOverview(w, h int) string {
	if m.st == nil {
		return m.viewNoService(w, h)
	}
	st := m.st
	n := 4
	if w < 100 {
		n = 2
	}
	tw := w / n
	var tiles []string
	tiles = append(tiles, m.tileBypass(st, tw), m.tileHealth(st, tw), m.tileGames(st, tw), m.tileTelegram(st, w-tw*(n-1)))
	var top string
	if n == 4 {
		top = lipgloss.JoinHorizontal(lipgloss.Top, tiles...)
	} else {
		top = lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinHorizontal(lipgloss.Top, tiles[0], tiles[1]), lipgloss.JoinHorizontal(lipgloss.Top, tiles[2], tiles[3]))
	}
	traffic := panel("Трафик", w, m.trafficTable(st, w-4))
	foot := " " + sMuted.Render(st.Engine+" · zapret "+st.EngineVersion+" · "+st.Firewall)
	if st.LastTune.IsZero() {
		foot += sMuted.Render(" · подбора ещё не было — ") + sBeam.Render("t")
	} else {
		foot += sMuted.Render(" · подбор " + st.LastTune.Format("02.01 15:04"))
	}
	if st.Error != "" {
		foot += "\n " + sBlock.Render(truncate(st.Error, w-2))
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, traffic, foot)
}

func (m model) tileBypass(st *core.Status, w int) string {
	switch {
	case m.tuning():
		return tile("Обход", gradient("◆ идёт подбор", m.phase*2, true), sMuted.Render(fmt.Sprintf("прогон %d", st.Tuning.Round)), w)
	case st.Running:
		return tile("Обход", sBeamB.Render("● работает"), sMuted.Render(human(time.Since(st.Since))), w)
	case st.Enabled:
		return tile("Обход", sPartial.Render("◐ запускается"), sMuted.Render("…"), w)
	}
	return tile("Обход", sMuted.Render("○ выключен"), sFaint.Render("пробел — включить"), w)
}

func (m model) tileHealth(st *core.Status, w int) string {
	ok, total := 0, 0
	for _, g := range st.Groups {
		if g.Total == 0 {
			continue
		}
		total++
		if g.OK == g.Total {
			ok++
		}
	}
	if total == 0 {
		return tile("Доступность", sMuted.Render("не проверено"), sFaint.Render("c — проверить"), w)
	}
	val := ratioStyle(ok, total).Bold(true).Render(fmt.Sprintf("%d из %d", ok, total)) + sMuted.Render(" групп")
	meter := ""
	for i := 0; i < total; i++ {
		if i < ok {
			meter += sBeam.Render("▰")
		} else {
			meter += sFaint.Render("▱")
		}
	}
	return tile("Доступность", val, meter, w)
}

func (m model) tileGames(st *core.Status, w int) string {
	lat := 0
	for _, g := range st.Groups {
		if g.ID == "game-udp" && g.OK > 0 {
			lat = g.Latency
		}
	}
	val := sMuted.Render("—")
	if lat > 0 {
		val = sBeamB.Render(fmt.Sprintf("%d мс", lat))
	}
	r := st.Route
	sub := sMuted.Render("напрямую")
	switch {
	case !st.Services[strategy.SvcR6] && !st.Services[strategy.SvcForHonor]:
		val, sub = sMuted.Render("выключены"), sFaint.Render("«Цели» → R6 / For Honor")
	case r.VPN && st.Direct:
		sub = sViolet.Render("мимо VPN") + sMuted.Render(" · "+r.Phys)
	case r.VPN:
		sub = sPartial.Render("через VPN " + r.App)
	}
	return tile("Пинг игр", val, sub, w)
}

func (m model) tileTelegram(st *core.Status, w int) string {
	t := st.Telegram
	if t == nil || !t.Running {
		return tile("Telegram", sMuted.Render("○ выключен"), sFaint.Render("вкладка 4 → e"), w)
	}
	return tile("Telegram", sBeamB.Render(fmt.Sprintf("● %d", t.Stats.Active))+sMuted.Render(" соединений"),
		sMuted.Render("↓"+bytesH(t.Stats.Down)+"  ↑"+bytesH(t.Stats.Up)), w)
}

func (m model) trafficTable(st *core.Status, w int) string {
	if len(st.Groups) == 0 {
		return sMuted.Render("Ни одна цель не включена — отметьте нужные на вкладке «Цели».")
	}
	titleW, resW, latW, sparkW := 24, 7, 8, 14
	recW := max(10, w-titleW-resW-latW-sparkW-8)
	var rows []string
	for _, g := range st.Groups {
		health := g.Health
		if health == "fail" {
			health = "down"
		}
		if !st.Running && g.Pick != strategy.None {
			health = "off"
		}
		rec := g.PickLabel
		recSt := sMuted
		switch {
		case g.Pick == strategy.None:
			rec, recSt = "напрямую, без обхода", sFaint
		case strategy.IsBred(g.Pick):
			rec, recSt = "✦ "+strings.Join(g.PickTags, " · "), sViolet
		case len(g.PickTags) > 0:
			rec = strings.Join(g.PickTags, " · ")
		}
		res, lat := sFaint.Render("—"), sFaint.Render("")
		if g.Total > 0 {
			res = ratioStyle(g.OK, g.Total).Render(fmt.Sprintf("%d/%d", g.OK, g.Total))
			lat = sMuted.Render(ms(g.Latency))
		} else if !g.Testable {
			res = sFaint.Render("·")
		}
		rows = append(rows, dot(health)+" "+
			pad(sInk.Render(truncate(g.Title, titleW)), titleW)+" "+
			pad(recSt.Render(truncate(rec, recW)), recW)+" "+
			pad(res, resW)+pad(lat, latW)+" "+
			spark(m.hist[g.ID], sparkW))
	}
	if m.rep != nil && m.rep.Universal != "" {
		if c, ok := strategy.ParseCandidateID(m.rep.Universal); ok && c.Args != "" {
			rows = append(rows, "", sViolet.Render("✦ ")+sMuted.Render(fmt.Sprintf("один выведенный рецепт работает для %d групп: ", len(m.rep.UniversalGroups)))+sViolet.Render(truncate(strings.Join(strategy.Tags(c.Args), " · "), w-50)))
		}
	}
	if len(m.check) > 0 {
		var bad []string
		for _, r := range m.check {
			if !r.OK {
				bad = append(bad, r.Host)
			}
		}
		if len(bad) > 0 {
			rows = append(rows, "", sBlock.Render("недоступно: ")+sMuted.Render(truncate(strings.Join(bad, ", "), w-14)))
		}
	}
	return strings.Join(rows, "\n")
}

func (m model) routeCard(st *core.Status) string {
	r := st.Route
	switch {
	case r.Iface == "":
		return sMuted.Render("маршрут не определён")
	case !r.VPN:
		return dot("ok") + sInk.Render(" напрямую через "+r.Iface) + "\n" + sMuted.Render("VPN не мешает — игры идут коротким путём")
	case st.Direct:
		return sViolet.Render("◆ ") + sInk.Render("VPN "+r.App+" ("+r.Iface+")") + "\n" +
			dot("ok") + sBeam.Render(" игры идут мимо VPN") + sMuted.Render(" через "+r.Phys)
	case r.CanBypass() && st.GameDirect == "off":
		return sPartial.Render("◐ ") + sInk.Render("VPN "+r.App+" ("+r.Iface+")") + "\n" + sMuted.Render("игры идут через VPN — пинг выше. «Игры» → v")
	case r.VPN:
		return sPartial.Render("◐ ") + sInk.Render("VPN "+r.App+" ("+r.Iface+")") + "\n" + sMuted.Render("исключите игры из туннеля — см. «Игры»")
	}
	return ""
}

func (m model) telegramCard(st *core.Status) string {
	t := st.Telegram
	if t == nil || !t.Running {
		if st.Services[strategy.SvcTelegram] {
			return sMuted.Render("включится вместе с обходом")
		}
		return sMuted.Render("выключен · вкладка «Telegram» → e")
	}
	return dot("ok") + sInk.Render(" прокси "+t.Listen) + "\n" +
		sMuted.Render(fmt.Sprintf("соединений %d · всего %d · ↓%s ↑%s", t.Stats.Active, t.Stats.Total, bytesH(t.Stats.Down), bytesH(t.Stats.Up)))
}

func (m model) engineCard(st *core.Status) string {
	l1 := sInk.Render(st.Engine) + sMuted.Render(" zapret "+st.EngineVersion+" · "+st.Firewall)
	l2 := sMuted.Render("подбор: ")
	if st.LastTune.IsZero() {
		l2 += sPartial.Render("ещё не было — нажмите t")
	} else {
		l2 += sInk.Render(st.LastTune.Format("02.01 15:04"))
	}
	if st.Error != "" {
		l2 += "\n" + sBlock.Render(truncate(st.Error, 60))
	}
	return l1 + "\n" + l2
}

func bytesH(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d Б", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f КБ", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f МБ", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f ГБ", float64(n)/(1<<30))
}

func (m model) viewNoService(w, h int) string {
	msg := sBold.Render("Служба Просвета не установлена") + "\n\n" +
		sMuted.Render("Обходу нужны права администратора: он перехватывает пакеты.\n") +
		sMuted.Render("Установка займёт минуту, автоподбор — ещё пару.\n\n") +
		keyHint("7", "Система → i — установить") + "\n\n" +
		sMuted.Render("Без службы уже работают:\n") +
		sBeam.Render("  3 ") + sInk.Render("Игры") + sMuted.Render(" — пинг до дата-центров Siege и выбор ближайшего\n") +
		sBeam.Render("  4 ") + sInk.Render("Telegram") + sMuted.Render(" — локальный прокси через WebSocket, права не нужны")
	if m.stErr != nil {
		msg += "\n\n" + sBlock.Render(truncate(m.stErr.Error(), w-10))
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sBox.Render(msg))
}
