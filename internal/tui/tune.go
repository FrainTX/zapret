package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"prosvet/internal/strategy"
	"prosvet/internal/tuner"
)

var stageNames = map[int]string{
	1: "перебор рецептов",
	2: "подбор приманки",
	3: "селекция: новые рецепты",
	4: "поиск универсального",
	5: "проверка связки",
}

type groupRow struct {
	id     string
	cells  []tuner.Event
	best   tuner.Event
	hasBst bool
}

func (m model) viewTune(w, h int) string {
	var tp = m.st
	if tp == nil {
		return m.viewNoService(w, h)
	}
	t := tp.Tuning
	if (t == nil || (!t.Active && t.Report == nil)) && m.rep == nil {
		return panel("Автоподбор", w, lipgloss.NewStyle().Width(max(20, w-4)).Render(
			sMuted.Render("Проверит каждую группу трафика без обхода, затем рецепты из каталога и имена-приманки, а лучшие скрестит в новые.")+"\n\n"+
				keyHint("t", "быстрый · 1–3 мин")+"\n"+keyHint("d", "глубокий · 5–10 мин")+"\n"+keyHint("s", "селекция · 10–20 мин")))
	}
	var events []tuner.Event
	if t != nil {
		events = t.Events
	}
	rows := map[string]*groupRow{}
	var order []string
	universal := ""
	var universalTags []string
	gen, gens := 0, 0
	for _, e := range events {
		switch e.Type {
		case "trial":
			r := rows[e.Group]
			if r == nil {
				r = &groupRow{id: e.Group}
				rows[e.Group] = r
				order = append(order, e.Group)
			}
			r.cells = append(r.cells, e)
			if e.Best == e.Candidate {
				r.best, r.hasBst = e, true
			}
		case "generation":
			gen, gens = e.Round, e.Rounds
		case "universal":
			universal, universalTags = e.Candidate, e.Tags
		}
	}

	// header: stage track
	var track []string
	cur := 0
	if t != nil {
		cur = t.Stage
	}
	for i := 1; i <= 5; i++ {
		n := stageNames[i]
		switch {
		case i < cur || (t != nil && !t.Active):
			track = append(track, sBeam.Render("● ")+sMuted.Render(n))
		case i == cur:
			track = append(track, gradient("◆ "+n, m.phase*2, true))
		default:
			track = append(track, sFaint.Render("○ "+n))
		}
	}
	head := " " + strings.Join(track, sFaint.Render("  ─  "))
	if lipgloss.Width(head) > w {
		head = " " + gradient(fmt.Sprintf("этап %d · %s", cur, stageNames[cur]), m.phase*2, true)
	}
	info := ""
	if t != nil && t.Active {
		el := time.Since(t.Started).Round(time.Second)
		info = sMuted.Render(fmt.Sprintf(" прогон %d · %s", t.Round, el))
		if gen > 0 {
			info += sViolet.Render(fmt.Sprintf(" · поколение %d/%d", gen, gens))
		}
		if t.Message != "" {
			info += sMuted.Render(" · " + t.Message)
		}
	} else if t != nil && t.Error != "" {
		info = sBlock.Render(" " + t.Error)
	}

	// matrix
	// Columns: name | recent cells | result | best recipe. On a narrow
	// terminal the recipe column goes first, then cells shrink; no line
	// ever wraps.
	titleW := min(22, max(10, w*28/100))
	const resultW = 13
	room := w - 1 - titleW - 1 - 2 - resultW
	maxCells := 0
	for _, id := range order {
		maxCells = max(maxCells, len(rows[id].cells))
	}
	need := maxCells + max(0, maxCells-1)/5 // a gap after every 5 cells
	cellsW, bestW := min(room, need), 0
	if room >= 44 {
		cellsW = min(need, room/2)
		bestW = room - cellsW - 1
	}
	cellsW = max(4, cellsW)
	perCell := cellsW
	for perCell > 1 && perCell+(perCell-1)/5 > cellsW {
		perCell--
	}
	var lines []string
	for _, id := range order {
		r := rows[id]
		title := id
		if g, ok := strategy.GroupByID(id); ok {
			title = g.Title
		}
		cs := r.cells
		if len(cs) > perCell {
			cs = cs[len(cs)-perCell:]
		}
		var cb strings.Builder
		for i, c := range cs {
			if i > 0 && i%5 == 0 {
				cb.WriteString(" ")
			}
			cb.WriteString(cell(c.OK, c.Total, strategy.IsBred(c.Candidate)))
		}
		result := sFaint.Render("…")
		label := ""
		if r.hasBst {
			b := r.best
			result = ratioStyle(b.OK, b.Total).Render(fmt.Sprintf("%d/%d", b.OK, b.Total)) + " " + sMuted.Render(ms(b.Latency))
			l := strings.Join(b.Tags, " · ")
			st := sMuted
			if strategy.IsBred(b.Candidate) {
				l, st = "✦ "+l, sViolet
			}
			if b.Candidate == strategy.None {
				l = "без обхода"
			}
			if bestW > 6 {
				label = " " + st.Render(truncate(l, bestW))
			}
		}
		lines = append(lines, " "+pad(sInk.Render(truncate(title, titleW)), titleW)+" "+pad(cb.String(), cellsW)+"  "+pad(result, resultW)+label)
	}
	if len(lines) == 0 {
		lines = append(lines, sMuted.Render(" готовлю движок…"))
	}
	legend := " " + sBeam.Render("■") + sMuted.Render(" всё доступно  ") + sPartial.Render("■") + sMuted.Render(" частично  ") +
		sBlock.Render("■") + sMuted.Render(" не помогло  ") + sViolet.Render("◆") + sMuted.Render(" выведенный рецепт")

	out := []string{head, info, "", strings.Join(lines, "\n"), "", legend}
	if universal != "" {
		out = append(out, "", " "+sViolet.Render("✦ Найден универсальный рецепт")+sMuted.Render(" — один на все TLS-группы: ")+sViolet.Render(strings.Join(universalTags, " · ")))
		if c, ok := strategy.ParseCandidateID(universal); ok {
			out = append(out, "   "+sFaint.Render(truncate(c.Args, w-4)))
		}
	}
	rep := m.rep
	if t != nil && t.Report != nil {
		rep = t.Report
	}
	if rep != nil && (t == nil || !t.Active) {
		out = append(out, "", m.reportSummary(rep, w))
	}
	return strings.Join(out, "\n")
}

func (m model) reportSummary(rep *tuner.Report, w int) string {
	var b []string
	b = append(b, " "+sTitle.Render("ИТОГ")+sMuted.Render(fmt.Sprintf("  %s · прогонов %d", rep.Finished.Sub(rep.Started).Round(time.Second), rep.Rounds)))
	for _, g := range rep.Groups {
		line := " " + dot(map[bool]string{true: "ok", false: "partial"}[g.Best.Total > 0 && g.Best.OK == g.Best.Total])
		if g.Best.OK == 0 {
			line = " " + dot("down")
		}
		label := strings.Join(g.PickTags, " · ")
		st := sMuted
		if strategy.IsBred(g.Pick) {
			label, st = "✦ "+label, sViolet
		}
		if g.Pick == strategy.None {
			label = "без обхода"
		}
		line += " " + pad(sInk.Render(truncate(g.Title, 22)), 22) + " " + st.Render(truncate(label, max(10, w-60)))
		if g.Note != "" {
			line += sFaint.Render("  " + truncate(g.Note, 40))
		}
		b = append(b, line)
	}
	if n := len(rep.Bred); n > 0 {
		b = append(b, " "+sViolet.Render(fmt.Sprintf("✦ сохранено выведенных рецептов: %d", n)))
	}
	return strings.Join(b, "\n")
}
