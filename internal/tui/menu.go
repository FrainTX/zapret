package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"prosvet/internal/strategy"
)

// The main menu: a numbered list in the spirit of a service.bat menu —
// arrows and Enter, or the digit, and a short hint of the current state
// next to each line.

type menuItem struct {
	label string
	hint  func(m model) string
	run   func(m model) (model, tea.Cmd)
}

func open(tab int) func(m model) (model, tea.Cmd) {
	return func(m model) (model, tea.Cmd) {
		m.inMenu, m.tab = false, tab
		return m, m.onTab()
	}
}

var menuItems = []menuItem{
	{"Включить / выключить", func(m model) string {
		switch {
		case m.st == nil:
			return ""
		case m.st.Running:
			return "сейчас работает"
		case m.st.Enabled:
			return "запускается"
		}
		return "сейчас выключен"
	}, func(m model) (model, tea.Cmd) {
		_, cmd := m.keyMain(" ")
		return m, cmd
	}},
	{"Подобрать стратегию", func(m model) string {
		if m.st == nil || m.st.LastTune.IsZero() {
			return "ещё не было"
		}
		return "последний " + m.st.LastTune.Format("02.01 15:04")
	}, open(tabTune)},
	{"Проверить сайты", func(m model) string {
		if m.st == nil {
			return ""
		}
		ok, total := 0, 0
		for _, g := range m.st.Groups {
			if g.Total > 0 {
				total++
				if g.OK == g.Total {
					ok++
				}
			}
		}
		if total == 0 {
			return ""
		}
		return fmt.Sprintf("работает %d из %d", ok, total)
	}, func(m model) (model, tea.Cmd) {
		m.inMenu, m.tab = false, tabOverview
		_, cmd := m.keyMain("c")
		m.toast, m.toastOK, m.toastAt = "проверяю…", true, time.Now()
		return m, cmd
	}},
	{"Игры: пинг и дата-центр", func(m model) string {
		if m.st == nil {
			return ""
		}
		for _, g := range m.st.Groups {
			if g.ID == "game-udp" && g.OK > 0 {
				s := fmt.Sprintf("%d мс", g.Latency)
				if m.st.Direct {
					s += " · мимо VPN"
				}
				return s
			}
		}
		return ""
	}, open(tabGames)},
	{"Telegram", func(m model) string {
		if m.st == nil {
			return ""
		}
		if t := m.st.Telegram; t != nil && t.Running {
			return fmt.Sprintf("работает · %d соединений", t.Stats.Active)
		}
		if m.st.Services[strategy.SvcTelegram] {
			return "включится с обходом"
		}
		return "выключен"
	}, open(tabTelegram)},
	{"Цели и свои сайты", func(m model) string {
		if m.st == nil {
			return ""
		}
		n := 0
		for _, on := range m.st.Services {
			if on {
				n++
			}
		}
		return fmt.Sprintf("включено целей: %d", n)
	}, open(tabTargets)},
	{"Журнал", func(model) string { return "" }, open(tabLogs)},
	{"Система и обновления", func(m model) string {
		if m.st == nil || m.st.Update == nil {
			return ""
		}
		u := m.st.Update
		if u.Available {
			return "доступна версия " + u.Latest
		}
		return "версия " + u.Current
	}, open(tabSystem)},
}

func (m model) keyMenu(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "up", "k", "л":
		m.mcur = (m.mcur + len(menuItems) - 1) % len(menuItems)
	case "down", "j", "о", "tab":
		m.mcur = (m.mcur + 1) % len(menuItems)
	case "enter", " ", "right", "l":
		return menuItems[m.mcur].run(m)
	default:
		if len(s) == 1 && s[0] >= '1' && int(s[0]-'0') <= len(menuItems) {
			m.mcur = int(s[0] - '1')
			return menuItems[m.mcur].run(m)
		}
	}
	return m, nil
}

func (m model) viewMenu(w, h int) string {
	labelW := 0
	for _, it := range menuItems {
		labelW = max(labelW, len([]rune(it.label)))
	}
	var b strings.Builder
	b.WriteString("\n")
	for i, it := range menuItems {
		num := fmt.Sprintf("%d", i+1)
		hint := it.hint(m)
		if i == m.mcur {
			b.WriteString(" " + sBeamB.Render(">") + " " + sBold.Render(num) + "  " + pad(sBold.Render(it.label), labelW))
		} else {
			b.WriteString("   " + sMuted.Render(num) + "  " + pad(sInk.Render(it.label), labelW))
		}
		if hint != "" {
			b.WriteString("   " + sFaint.Render(hint))
		}
		b.WriteString("\n")
	}
	if m.st == nil && m.be == nil {
		b.WriteString("\n " + sMuted.Render("Служба не установлена: пункт 8 → i. Игры и Telegram работают и без неё."))
	}
	return b.String()
}
