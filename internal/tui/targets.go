package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"prosvet/internal/core"
	"prosvet/internal/platform"
	"prosvet/internal/strategy"
)

// targetItems is the cursor-addressable list: services, then own domains,
// then exclusions.
type targetItem struct {
	kind string // service, domain, exclude
	id   string
}

func (m model) targetItems() []targetItem {
	var out []targetItem
	if m.st == nil {
		return out
	}
	for _, s := range m.st.ServiceList {
		out = append(out, targetItem{"service", s.ID})
	}
	for _, d := range m.st.Domains {
		out = append(out, targetItem{"domain", d})
	}
	for _, d := range m.st.Exclude {
		out = append(out, targetItem{"exclude", d})
	}
	return out
}

func (m model) keyTargets(s string) (tea.Model, tea.Cmd) {
	items := m.targetItems()
	switch s {
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(len(items)-1, m.cursor+1)
	case "a", "ф":
		if m.be == nil {
			return m, toast("", errNoBackend)
		}
		m.adding = "domains"
		m.input.Prompt = "  свой сайт › "
		return m, m.input.Focus()
	case "x", "ч":
		if m.be == nil {
			return m, toast("", errNoBackend)
		}
		m.adding = "exclude"
		m.input.Prompt = "  не трогать › "
		return m, m.input.Focus()
	case " ", "enter", "delete", "backspace":
		if m.cursor >= len(items) || m.st == nil {
			return m, nil
		}
		it := items[m.cursor]
		switch it.kind {
		case "service":
			on := !m.st.Services[it.id]
			return m, m.act(map[bool]string{true: "цель включена", false: "цель выключена"}[on], func(ctx context.Context, be Backend) error {
				return be.SetService(ctx, it.id, on)
			})
		case "domain", "exclude":
			if s == " " || s == "enter" {
				return m, nil
			}
			ex := it.kind == "exclude"
			list := m.st.Domains
			if ex {
				list = m.st.Exclude
			}
			rest := slices.DeleteFunc(slices.Clone(list), func(d string) bool { return d == it.id })
			return m, m.act(it.id+" удалён", func(ctx context.Context, be Backend) error {
				_, err := be.SetDomains(ctx, rest, ex)
				return err
			})
		}
	}
	return m, nil
}

func (m model) addDomain(exclude bool, v string) tea.Cmd {
	if m.st == nil {
		return toast("", errNoBackend)
	}
	list := m.st.Domains
	if exclude {
		list = m.st.Exclude
	}
	var add []string
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' || r == ';' }) {
		add = append(add, f)
	}
	all := append(slices.Clone(list), add...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		got, err := m.be.SetDomains(ctx, all, exclude)
		if err != nil {
			return toastMsg{err: err}
		}
		return toastMsg{text: fmt.Sprintf("в списке %d доменов", len(got))}
	}
}

func (m model) viewTargets(w, h int) string {
	if m.st == nil {
		return m.viewNoService(w, h)
	}
	items := m.targetItems()
	var svc, own []string
	for i, it := range items {
		sel := i == m.cursor
		cur := "  "
		if sel {
			cur = sBeamB.Render("▸ ")
		}
		switch it.kind {
		case "service":
			var s strategy.Service
			for _, x := range m.st.ServiceList {
				if x.ID == it.id {
					s = x
				}
			}
			box := sFaint.Render("[ ]")
			name := sMuted.Render(s.Title)
			if m.st.Services[it.id] {
				box, name = sBeam.Render("[●]"), sInk.Render(s.Title)
			}
			svc = append(svc, cur+box+" "+pad(name, 26)+sFaint.Render(truncate(s.Note, max(10, w-40))))
		case "domain":
			own = append(own, cur+sBeam.Render("+ ")+sInk.Render(it.id))
		case "exclude":
			own = append(own, cur+sBlock.Render("− ")+sMuted.Render(it.id))
		}
	}
	if len(own) == 0 {
		own = append(own, sMuted.Render("  пока пусто: a — добавить сайт в обход, x — сайт, который не трогать"))
	}
	out := panel("Что обходить", w, strings.Join(svc, "\n")) + "\n" + panel("Свои сайты и исключения", w, strings.Join(own, "\n"))
	if m.adding != "" {
		out += "\n" + m.input.View()
	}
	return out
}

// ---------------------------------------------------------------- logs

func (m model) viewLogs(w, h int) string {
	if m.be == nil {
		return m.viewNoService(w, h)
	}
	n := max(1, h-1)
	end := max(0, len(m.logs)-m.scroll)
	start := max(0, end-n)
	var rows []string
	for _, e := range m.logs[start:end] {
		lv := sFaint
		switch e.Level {
		case "warn":
			lv = sPartial
		case "error":
			lv = sBlock
		case "engine":
			lv = sFaint
		case "info":
			lv = sMuted
		}
		text := e.Text
		st := sInk
		if e.Level == "engine" {
			st = sMuted
		}
		rows = append(rows, sFaint.Render(e.Time.Format("15:04:05"))+" "+lv.Render(pad(truncate(e.Source, 8), 8))+" "+st.Render(truncate(text, w-20)))
	}
	if len(rows) == 0 {
		rows = append(rows, sMuted.Render(" журнал пуст"))
	}
	return strings.Join(rows, "\n")
}

// ---------------------------------------------------------------- system

func (m model) keySystem(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "u", "г":
		m.toast, m.toastOK, m.toastAt = "проверяю обновления…", true, time.Now()
		return m, m.act("готово", func(ctx context.Context, be Backend) error {
			st, err := be.Update(ctx, false)
			if err != nil {
				return err
			}
			if !st.Available {
				return nil
			}
			_, err = be.Update(ctx, true)
			return err
		})
	case "a", "ф":
		on := m.st == nil || m.st.Update == nil || !m.st.Update.Auto
		return m, m.act(map[bool]string{true: "автообновление включено", false: "автообновление выключено"}[on], func(ctx context.Context, be Backend) error {
			return be.Settings(ctx, core.Settings{AutoUpdate: &on})
		})
	case "f", "а":
		if m.st == nil || len(m.st.Platform.Conflicts) == 0 {
			return m, toast("конфликтов нет", nil)
		}
		cs := m.st.Platform.Conflicts
		return m, m.act("конфликтующие обходы остановлены", func(ctx context.Context, be Backend) error {
			for _, c := range cs {
				if c.Fixable {
					if err := be.FixConflict(ctx, c.Ref); err != nil {
						return err
					}
				}
			}
			return nil
		})
	case "i", "ш":
		if m.o.Install == nil {
			return m, toast("служба уже установлена", nil)
		}
		m.confirm = &confirmState{text: "Установить службу Просвета с автозапуском?", yes: func() tea.Msg { return installMsg{} }}
		return m, nil
	}
	return m, nil
}

type installMsg struct{}

func (m model) viewSystem(w, h int) string {
	info := platform.Detect()
	var st *core.Status = m.st
	if st != nil {
		info = st.Platform
	}
	kv := func(k, v string) string { return pad(sMuted.Render(k), 18) + sInk.Render(v) }
	var b []string
	distro := info.Distro.Name
	if distro == "" {
		distro = info.OS
	}
	b = append(b, kv("система", distro+" · "+info.Arch), kv("ядро", info.Kernel), kv("init", info.Init), kv("перехват", info.Firewall), kv("движок", info.Engine))
	if st != nil {
		b = append(b, kv("zapret", st.EngineVersion), kv("списки IP", st.ListsDate))
	}
	if info.Immutable {
		b = append(b, sPartial.Render("неизменяемая система: служба ставится в /var или /opt"))
	}
	for _, n := range info.Notes {
		b = append(b, sMuted.Render("· "+n))
	}
	for _, x := range info.Missing {
		b = append(b, sBlock.Render("✕ нет: "+x))
	}
	var c []string
	if len(info.Conflicts) == 0 {
		c = append(c, dot("ok")+sMuted.Render(" других обходов не найдено"))
	}
	for _, x := range info.Conflicts {
		c = append(c, sPartial.Render("◐ ")+sInk.Render(x.Name)+sMuted.Render(" — "+x.Detail))
	}
	svc := sMuted.Render("служба: ")
	switch {
	case m.be == nil:
		svc += sPartial.Render("не установлена") + "  " + keyHint("i", "установить")
	case m.o.Local:
		svc += sPartial.Render("не установлена — обход живёт, пока открыто окно") + "  " + keyHint("i", "установить")
	default:
		svc += sBeam.Render("работает")
	}
	return lipgloss.JoinVertical(lipgloss.Left, panel("Платформа", w, strings.Join(b, "\n")), panel("Служба", w, svc), panel("Обновления", w, m.updatePanel()), panel("Другие обходы", w, strings.Join(c, "\n")))
}

func (m model) updatePanel() string {
	if m.st == nil || m.st.Update == nil {
		return sMuted.Render("версия " + core.Version + " · обновления управляются службой")
	}
	u := m.st.Update
	line := sMuted.Render("версия ") + sInk.Render(u.Current)
	switch {
	case u.Installing:
		line += "  " + gradient("устанавливаю "+u.Latest+"…", m.phase*2, true)
	case u.Available:
		line += "  " + sBeamB.Render("доступна "+u.Latest) + sMuted.Render(" — ") + keyHint("u", "обновить")
	case !u.Checked.IsZero() && u.Error == "":
		line += "  " + dot("ok") + sMuted.Render(" последняя")
	}
	auto := sBeam.Render("автообновление включено")
	if !u.Auto {
		auto = sPartial.Render("автообновление выключено")
	}
	out := line + "\n" + auto + sMuted.Render(" · "+u.Source)
	if !u.Checked.IsZero() {
		out += sFaint.Render(" · проверено " + u.Checked.Format("02.01 15:04"))
	}
	if u.Error != "" {
		out += "\n" + sBlock.Render(truncate(u.Error, 100))
	}
	return out
}
