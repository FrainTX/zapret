package ui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"prosvet/internal/core"
	"prosvet/internal/probe"
	"prosvet/internal/strategy"
	"prosvet/internal/tuner"
)

const boardWidth = 78

// Duration formats an uptime compactly in Russian.
func Duration(d time.Duration) string {
	d = d.Round(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h >= 24:
		return fmt.Sprintf("%dд %dч", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dч %02dм", h, m)
	}
	return fmt.Sprintf("%dм", m)
}

// Header prints the product line.
func Header(w io.Writer, s core.Status) {
	left := Bold("ПРОСВЕТ") + Dim(fmt.Sprintf("  %s · zapret %s · %s", s.Version, s.EngineVersion, s.Firewall))
	var right string
	switch {
	case s.Tuning != nil && s.Tuning.Active:
		right = Amber("◐ ПОДБОР")
	case s.Running:
		right = AccentBG(" РАБОТАЕТ ") + Dim(" "+Duration(time.Since(s.Since)))
	default:
		right = Dim("○ ВЫКЛЮЧЕН")
	}
	fmt.Fprintln(w, Pad(left, boardWidth-Width(right))+right)
	fmt.Fprintln(w, Dim(strings.Repeat("━", boardWidth)))
}

func healthGlyph(h string) string {
	switch h {
	case "ok":
		return Accent("●")
	case "partial":
		return Amber("◑")
	case "fail":
		return Red("●")
	}
	return Dim("○")
}

// StatusBoard renders services and groups.
func StatusBoard(w io.Writer, s core.Status) {
	Header(w, s)
	byService := map[string][]core.GroupStatus{}
	for _, g := range s.Groups {
		for _, svc := range g.Services {
			byService[svc] = append(byService[svc], g)
		}
	}
	for _, svc := range s.ServiceList {
		on := s.Services[svc.ID]
		box := Dim("□")
		title := Dim(svc.Title)
		if on {
			box = Accent("■")
			title = Bold(svc.Title)
		}
		fmt.Fprintf(w, " %s %s %s\n", box, Pad(title, 24), Dim(Trunc(svc.Note, 48)))
		if !on {
			continue
		}
		for _, g := range byService[svc.ID] {
			tags := strings.Join(g.PickTags, " · ")
			if g.Pick == strategy.None {
				tags = "без обхода"
			}
			health := ""
			if g.Total > 0 {
				health = fmt.Sprintf("%d/%d", g.OK, g.Total)
				if g.Latency > 0 {
					health += fmt.Sprintf(" %dмс", g.Latency)
				}
			} else if !g.Testable {
				health = "не проверяется"
			}
			fmt.Fprintf(w, "     %s %s %s %s\n", healthGlyph(g.Health), Pad(Dim(g.Title), 22), Pad(Trunc(tags, 34), 35), Dim(health))
		}
	}
	fmt.Fprintln(w, Dim(strings.Repeat("━", boardWidth)))
	if s.Error != "" {
		fmt.Fprintln(w, Red(" ошибка: ")+s.Error)
	}
	if len(s.Platform.Conflicts) > 0 {
		for _, c := range s.Platform.Conflicts {
			fmt.Fprintln(w, Amber(" ⚠ ")+c.Name+Dim(" — "+c.Detail))
		}
		fmt.Fprintln(w, Dim("   исправить: prosvet fix"))
	}
	if !s.LastTune.IsZero() {
		fmt.Fprintln(w, Dim(" подбор: "+s.LastTune.Format("02.01.2006 15:04")+"  ·  списки IP: "+s.ListsDate))
	} else {
		fmt.Fprintln(w, Amber(" стратегии ещё не подбирались — ")+Bold("prosvet tune"))
	}
}

// Results prints probe results grouped.
func Results(w io.Writer, rs []probe.Result) {
	last := ""
	for _, r := range rs {
		if r.Group != last {
			if g, ok := strategy.GroupByID(r.Group); ok {
				Rule(w, g.Title, boardWidth)
			}
			last = r.Group
		}
		mark := Accent("●")
		cls := Accent("доступно")
		if !r.OK {
			mark = Red("●")
			cls = Red(probe.ClassText[r.Class])
		}
		lat := ""
		if r.Latency > 0 {
			lat = fmt.Sprintf("%d мс", r.Latency.Milliseconds())
		}
		fmt.Fprintf(w, " %s %s %s %s\n", mark, Pad(Trunc(r.Host, 38), 39), Pad(cls, 30), Dim(lat))
	}
}

// Matrix renders live tuning progress: one row per group, one cell per
// tried recipe.
type Matrix struct {
	W      io.Writer
	groups []string
	cells  map[string][]string
	best   map[string]string
	stage  int
	round  int
	msg    string
	lines  int
	start  time.Time
}

// NewMatrix creates a matrix for the given groups.
func NewMatrix(w io.Writer, groups []string) *Matrix {
	return &Matrix{W: w, groups: groups, cells: map[string][]string{}, best: map[string]string{}, start: time.Now()}
}

func cell(e tuner.Event) string {
	switch {
	case e.Total == 0:
		return Dim("·")
	case e.OK == e.Total:
		return Accent("█")
	case e.Classes[probe.ClassLeak]*2 >= e.Total:
		return Red("×")
	case e.OK > 0:
		return Amber("▓")
	}
	return Dim("·")
}

// Feed applies an event and redraws.
func (m *Matrix) Feed(e tuner.Event) {
	switch e.Type {
	case "stage":
		m.stage, m.msg = e.Stage, e.Message
	case "round":
		m.round = e.Round
	case "trial":
		m.cells[e.Group] = append(m.cells[e.Group], cell(e))
		if e.Best == e.Candidate {
			m.best[e.Group] = strings.Join(e.Tags, " · ")
			if strategy.IsBred(e.Candidate) {
				m.best[e.Group] = "✦ " + m.best[e.Group]
			}
		} else if c, ok := strategy.ParseCandidateID(e.Best); ok && !strategy.IsBred(e.Best) {
			m.best[e.Group] = strings.Join(strategy.Tags(c.Args), " · ")
		}
	case "done":
		m.msg = e.Message
	}
	m.draw()
}

func (m *Matrix) draw() {
	if !Color && m.lines > 0 {
		return // plain output: only the final state is printed
	}
	var b strings.Builder
	b.WriteString(Up(m.lines))
	head := fmt.Sprintf(" этап %d · прогон %d · %s", m.stage, m.round, time.Since(m.start).Round(time.Second))
	b.WriteString(ClearLine() + Bold(head) + Dim("  "+m.msg) + "\n")
	for _, g := range m.groups {
		title := g
		if gg, ok := strategy.GroupByID(g); ok {
			title = gg.Title
		}
		cells := m.cells[g]
		if len(cells) > 40 {
			cells = cells[len(cells)-40:]
		}
		row := strings.Join(cells, "")
		best := m.best[g]
		if best == "" {
			best = Dim("—")
		}
		b.WriteString(ClearLine() + " " + Pad(Dim(title), 22) + Pad(row, 41) + Trunc(best, 30) + "\n")
	}
	fmt.Fprint(m.W, b.String())
	m.lines = len(m.groups) + 1
}

// Legend explains the matrix glyphs.
func Legend(w io.Writer) {
	fmt.Fprintln(w, Dim(" ")+Accent("█")+Dim(" работает  ")+Amber("▓")+Dim(" частично  ")+Red("×")+Dim(" фейк дошёл до сервера  ")+Dim("· не помогло"))
}

// Report prints the tuning summary.
func Report(w io.Writer, r *tuner.Report) {
	if r == nil {
		return
	}
	Rule(w, "итог подбора", boardWidth)
	for _, g := range r.Groups {
		mark := Accent("●")
		switch {
		case g.Best.OK == 0:
			mark = Red("●")
		case g.Best.OK < g.Best.Total:
			mark = Amber("◑")
		}
		pick := strings.Join(g.PickTags, " · ")
		if g.Pick == strategy.None {
			pick = "без обхода"
		} else if strategy.IsBred(g.Pick) {
			pick = "✦ " + pick
		}
		block := ""
		if g.Blocking != "" && g.Blocking != probe.ClassOK {
			block = Dim("было: " + probe.ClassText[g.Blocking])
		}
		fmt.Fprintf(w, " %s %s %s %s\n", mark, Pad(Bold(g.Title), 22), Pad(Trunc(pick, 34), 35), Dim(fmt.Sprintf("%d/%d", g.Best.OK, g.Best.Total)))
		if block != "" || g.Note != "" {
			fmt.Fprintf(w, "   %s %s %s\n", Pad("", 22), block, Dim(g.Note))
		}
	}
	if b, ok := r.Bred[r.Universal]; ok && r.Universal != "" {
		fmt.Fprintln(w, Violet(" ✦ универсальный рецепт для "+strings.Join(r.UniversalGroups, ", ")+": ")+strings.Join(strategy.Tags(b.Args), " · "))
		fmt.Fprintln(w, Dim("   "+b.Args))
	}
	if n := len(r.Bred); n > 0 {
		fmt.Fprintln(w, Violet(fmt.Sprintf(" ✦ выведено и сохранено новых рецептов: %d", n)))
	}
	fmt.Fprintln(w, Dim(fmt.Sprintf(" %d прогонов за %s", r.Rounds, r.Finished.Sub(r.Started).Round(time.Second))))
}
