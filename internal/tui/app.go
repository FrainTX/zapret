package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"prosvet/internal/core"
	"prosvet/internal/games"
	"prosvet/internal/logbuf"
	"prosvet/internal/probe"
	"prosvet/internal/strategy"
	"prosvet/internal/tgproxy"
	"prosvet/internal/tuner"
)

// Backend is the part of the control API the interface drives. It is the
// daemon client or an in-process controller; nil means neither is
// available (no service, no admin rights) and only the parts that need no
// privileges work: game ping, Siege data centre, Telegram proxy.
type Backend interface {
	Status(ctx context.Context) (core.Status, error)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	SetService(ctx context.Context, id string, on bool) error
	SetDomains(ctx context.Context, domains []string, exclude bool) ([]string, error)
	Settings(ctx context.Context, s core.Settings) error
	Tune(ctx context.Context, o core.TuneOptions) error
	CancelTune(ctx context.Context) error
	Check(ctx context.Context) ([]probe.Result, error)
	FixConflict(ctx context.Context, ref string) error
	Report(ctx context.Context) (*tuner.Report, error)
	Logs(ctx context.Context) ([]logbuf.Entry, error)
	Update(ctx context.Context, install bool) (core.UpdateStatus, error)
}

// Options configure Run.
type Options struct {
	Backend Backend
	// Local is set when Backend is an in-process controller: the bypass
	// lives only as long as this window.
	Local bool
	// Install is offered when no service is installed (runs elevated).
	Install func() error
	// StartTab opens a given screen first.
	StartTab int
}

const (
	tabOverview = iota
	tabTune
	tabGames
	tabTelegram
	tabTargets
	tabLogs
	tabSystem
)

var tabNames = []string{"Обзор", "Подбор", "Игры", "Telegram", "Цели", "Журнал", "Система"}

type (
	tickMsg   time.Time
	pollMsg   time.Time
	statusMsg struct {
		st  core.Status
		err error
	}
	toastMsg struct {
		text string
		err  error
	}
	logsMsg    []logbuf.Entry
	reportMsg  *tuner.Report
	samplesMsg struct {
		siege, udp, udpDirect []games.Sample
	}
	sampleMsg  games.Sample
	checkMsg   []probe.Result
	siegeMsg   []games.SiegeConfig
	tgLocalMsg struct{}
)

type model struct {
	o    Options
	be   Backend
	w, h int
	tab  int
	// inMenu shows the main menu; screens open from it and Esc returns.
	inMenu bool
	mcur   int
	phase  float64

	st    *core.Status
	stErr error
	hist  map[string][]int // latency history per group
	rep   *tuner.Report

	toast   string
	toastOK bool
	toastAt time.Time

	// games
	siege     []games.Sample
	udp       []games.Sample
	udpDirect []games.Sample
	measuring bool
	measured  int
	configs   []games.SiegeConfig

	// telegram without a service
	tg       *tgproxy.Proxy
	tgCancel context.CancelFunc
	tgSecret []byte

	// targets
	cursor int
	input  textinput.Model
	adding string // "", "domains", "exclude"

	// logs
	logs   []logbuf.Entry
	scroll int

	check []probe.Result

	confirm *confirmState

	wantInstall bool
}

type confirmState struct {
	text string
	yes  tea.Cmd
}

func newModel(o Options) model {
	ti := textinput.New()
	ti.Prompt = "  домен › "
	ti.PromptStyle = sBeam
	ti.TextStyle = sInk
	ti.Placeholder = "example.com"
	ti.PlaceholderStyle = sFaint
	ti.CharLimit = 253
	return model{o: o, be: o.Backend, tab: o.StartTab, inMenu: o.StartTab == 0, hist: map[string][]int{}, input: ti, configs: games.FindSiege()}
}

// Result tells the caller what the user asked for on the way out.
type Result struct {
	// Install: run the service installer (needs a normal terminal for the
	// elevation prompt), then come back.
	Install bool
}

// Run shows the interface until the user quits.
func Run(o Options) (Result, error) {
	m := newModel(o)
	p := tea.NewProgram(m, tea.WithAltScreen())
	fm, err := p.Run()
	var res Result
	if mm, ok := fm.(model); ok {
		if mm.tgCancel != nil {
			mm.tgCancel()
		}
		res.Install = mm.wantInstall
	}
	return res, err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tick(), m.poll(), m.fetchReport())
}

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) pollEvery() time.Duration {
	if m.st != nil && m.st.Tuning != nil && m.st.Tuning.Active {
		return 400 * time.Millisecond
	}
	return time.Second
}

func (m model) poll() tea.Cmd {
	if m.be == nil {
		return nil
	}
	be := m.be
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := be.Status(ctx)
		return statusMsg{st, err}
	}
}

func (m model) fetchReport() tea.Cmd {
	if m.be == nil {
		return nil
	}
	be := m.be
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, _ := be.Report(ctx)
		return reportMsg(r)
	}
}

func (m model) fetchLogs() tea.Cmd {
	if m.be == nil {
		return nil
	}
	be := m.be
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		l, _ := be.Logs(ctx)
		return logsMsg(l)
	}
}

// act runs a backend call and reports the outcome as a toast.
func (m model) act(done string, fn func(ctx context.Context, be Backend) error) tea.Cmd {
	if m.be == nil {
		return toast("", errNoBackend)
	}
	be := m.be
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := fn(ctx, be); err != nil {
			return toastMsg{err: err}
		}
		return toastMsg{text: done}
	}
}

var errNoBackend = errors.New("служба не установлена — установите её на вкладке «Система» или запустите от администратора")

// registerBred makes recipes synthesized by the service known here, so
// their ids resolve to arguments and tags.
func registerBred(r *tuner.Report) {
	if r == nil {
		return
	}
	for _, b := range r.Bred {
		strategy.Register(b)
	}
}

func toast(text string, err error) tea.Cmd {
	return func() tea.Msg { return toastMsg{text, err} }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.phase += 0.011
		if m.phase > 2 {
			m.phase -= 2
		}
		return m, tick()
	case pollMsg:
		return m, m.poll()
	case statusMsg:
		next := tea.Tick(m.pollEvery(), func(t time.Time) tea.Msg { return pollMsg(t) })
		if msg.err != nil {
			m.stErr = msg.err
			return m, next
		}
		wasTuning := m.st != nil && m.st.Tuning != nil && m.st.Tuning.Active
		st := msg.st
		if st.Tuning != nil {
			registerBred(st.Tuning.Report)
		}
		m.st, m.stErr = &st, nil
		for _, g := range st.Groups {
			if !g.Checked.IsZero() {
				h := m.hist[g.ID]
				if len(h) == 0 || h[len(h)-1] != g.Latency || len(h) < 2 {
					m.hist[g.ID] = append(h, g.Latency)
					if len(m.hist[g.ID]) > 60 {
						m.hist[g.ID] = m.hist[g.ID][1:]
					}
				}
			}
		}
		cmds := []tea.Cmd{next}
		if wasTuning && (st.Tuning == nil || !st.Tuning.Active) {
			cmds = append(cmds, m.fetchReport())
		}
		if m.tab == tabLogs {
			cmds = append(cmds, m.fetchLogs())
		}
		return m, tea.Batch(cmds...)
	case reportMsg:
		if msg != nil {
			m.rep = msg
			registerBred(msg)
		}
		return m, nil
	case logsMsg:
		m.logs = msg
		return m, nil
	case toastMsg:
		if msg.err != nil {
			m.toast, m.toastOK = msg.err.Error(), false
		} else {
			m.toast, m.toastOK = msg.text, true
		}
		m.toastAt = time.Now()
		return m, m.poll()
	case sampleMsg:
		m.measured++
		return m, nil
	case samplesMsg:
		m.siege, m.udp, m.udpDirect, m.measuring = msg.siege, msg.udp, msg.udpDirect, false
		m.configs = games.FindSiege()
		return m, nil
	case siegeMsg:
		m.configs = msg
		return m, nil
	case checkMsg:
		m.check = msg
		ok := 0
		for _, r := range msg {
			if r.OK {
				ok++
			}
		}
		m.toast, m.toastOK, m.toastAt = fmt.Sprintf("проверка: доступно %d из %d", ok, len(msg)), ok == len(msg), time.Now()
		return m, m.poll()
	case tgLocalMsg:
		return m, nil
	case installMsg:
		m.wantInstall = true
		return m, tea.Quit
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if s == "y" || s == "д" || s == "enter" {
			return m, c.yes
		}
		return m, nil
	}
	if m.adding != "" {
		switch s {
		case "esc":
			m.adding = ""
			m.input.Blur()
			return m, nil
		case "enter":
			v := strings.TrimSpace(m.input.Value())
			list := m.adding
			m.adding = ""
			m.input.Blur()
			m.input.SetValue("")
			if v == "" {
				return m, nil
			}
			return m, m.addDomain(list == "exclude", v)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	}
	switch s {
	case "ctrl+c", "q", "й":
		return m, tea.Quit
	}
	if m.inMenu {
		return m.keyMenu(s)
	}
	if s == "esc" || s == "backspace" {
		m.inMenu = true
		return m, nil
	}
	switch m.tab {
	case tabOverview, tabTune:
		return m.keyMain(s)
	case tabGames:
		return m.keyGames(s)
	case tabTelegram:
		return m.keyTelegram(s)
	case tabTargets:
		return m.keyTargets(s)
	case tabLogs:
		switch s {
		case "up", "k":
			m.scroll++
		case "down", "j":
			m.scroll = max(0, m.scroll-1)
		case "pgup":
			m.scroll += 10
		case "pgdown":
			m.scroll = max(0, m.scroll-10)
		case "end", "G":
			m.scroll = 0
		}
		return m, nil
	case tabSystem:
		return m.keySystem(s)
	}
	return m, nil
}

func (m model) onTab() tea.Cmd {
	switch m.tab {
	case tabLogs:
		return m.fetchLogs()
	case tabGames:
		return func() tea.Msg { return siegeMsg(games.FindSiege()) }
	}
	return nil
}

func (m model) tuning() bool { return m.st != nil && m.st.Tuning != nil && m.st.Tuning.Active }

func (m model) keyMain(s string) (tea.Model, tea.Cmd) {
	switch s {
	case " ", "enter":
		if m.st == nil {
			return m, toast("", errNoBackend)
		}
		if m.st.Running || m.st.Enabled {
			return m, m.act("обход выключен", func(ctx context.Context, be Backend) error { return be.Stop(ctx) })
		}
		return m, m.act("обход включён", func(ctx context.Context, be Backend) error { return be.Start(ctx) })
	case "t", "е":
		m.tab = tabTune
		return m, m.act("подбор начат", func(ctx context.Context, be Backend) error { return be.Tune(ctx, core.TuneOptions{}) })
	case "d", "в":
		m.tab = tabTune
		return m, m.act("глубокий подбор начат", func(ctx context.Context, be Backend) error {
			return be.Tune(ctx, core.TuneOptions{Deep: true})
		})
	case "s", "ы":
		m.tab = tabTune
		return m, m.act("селекция начата: выведу новые рецепты", func(ctx context.Context, be Backend) error {
			return be.Tune(ctx, core.TuneOptions{Deep: true, Synth: 8})
		})
	case "x", "ч":
		if m.tuning() {
			return m, m.act("подбор остановлен", func(ctx context.Context, be Backend) error { return be.CancelTune(ctx) })
		}
	case "c", "с":
		if m.be == nil {
			return m, toast("", errNoBackend)
		}
		be := m.be
		m.toast, m.toastOK, m.toastAt = "проверяю…", true, time.Now()
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			r, err := be.Check(ctx)
			if err != nil {
				return toastMsg{err: err}
			}
			return checkMsg(r)
		}
	}
	return m, nil
}

// ---------------------------------------------------------------- view

func (m model) View() string {
	if m.w == 0 {
		return ""
	}
	w := m.w
	header := m.viewHeader(w)
	footer := m.viewFooter(w)
	bodyH := m.h - lipgloss.Height(header) - lipgloss.Height(footer)
	var body string
	switch {
	case m.inMenu:
		body = m.viewMenu(w, bodyH)
	case m.tab == tabOverview:
		body = m.viewOverview(w, bodyH)
	case m.tab == tabTune:
		body = m.viewTune(w, bodyH)
	case m.tab == tabGames:
		body = m.viewGames(w, bodyH)
	case m.tab == tabTelegram:
		body = m.viewTelegram(w, bodyH)
	case m.tab == tabTargets:
		body = m.viewTargets(w, bodyH)
	case m.tab == tabLogs:
		body = m.viewLogs(w, bodyH)
	case m.tab == tabSystem:
		body = m.viewSystem(w, bodyH)
	}
	if m.confirm != nil {
		box := sBox.Render(sBold.Render(m.confirm.text) + "\n\n" + keyHint("y", "да") + "   " + keyHint("n", "нет"))
		body = lipgloss.Place(w, bodyH, lipgloss.Center, lipgloss.Center, box)
	}
	body = lipgloss.NewStyle().Width(w).Height(max(0, bodyH)).MaxHeight(max(0, bodyH)).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// viewHeader is one quiet line: name, where we are, and the state.
func (m model) viewHeader(w int) string {
	title := sBold.Render("zapret")
	if !m.inMenu {
		title += sFaint.Render("  /  ") + sInk.Render(tabNames[m.tab])
	}
	line := " " + title + sFaint.Render("  ·  ") + m.stateLine()
	return "\n" + line + "\n " + sFaint.Render(strings.Repeat("─", max(10, min(w-2, 72))))
}

func sinPhase(p float64) float64 {
	// triangle-ish smooth sweep for the beam's hot spot
	x := p - float64(int(p))
	if int(p)%2 == 1 {
		x = 1 - x
	}
	return 2*x - 1
}

func (m model) stateLine() string {
	switch {
	case m.st == nil && m.be == nil:
		return sMuted.Render("служба не установлена ")
	case m.st == nil:
		if m.stErr != nil {
			return sBlock.Render("нет связи со службой ")
		}
		return sMuted.Render("подключаюсь… ")
	case m.tuning():
		return sViolet.Render("◆ идёт подбор ")
	case m.st.Running:
		up := time.Since(m.st.Since).Round(time.Minute)
		return sBeamB.Render("● обход работает") + sMuted.Render(" · "+human(up)+" ")
	case m.st.Enabled:
		return sPartial.Render("◐ запускается ")
	}
	return sMuted.Render("○ обход выключен ")
}

func human(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "меньше минуты"
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d ч %d мин", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d дн", int(d.Hours()/24))
}

func (m model) viewFooter(w int) string {
	var keys []string
	switch {
	case m.inMenu:
		keys = []string{keyHint("↑↓", "выбор"), keyHint("Enter", "открыть")}
	case m.tab == tabOverview || m.tab == tabTune:
		if m.tuning() {
			keys = []string{keyHint("x", "остановить подбор")}
		} else {
			on := "включить"
			if m.st != nil && (m.st.Running || m.st.Enabled) {
				on = "выключить"
			}
			keys = []string{keyHint("пробел", on), keyHint("t", "подбор"), keyHint("d", "глубокий"), keyHint("s", "вывести новые"), keyHint("c", "проверить")}
		}
	case m.tab == tabGames:
		keys = []string{keyHint("r", "замерить пинг"), keyHint("p", "закрепить ближайший"), keyHint("u", "вернуть как было"), keyHint("v", "мимо VPN")}
	case m.tab == tabTelegram:
		keys = []string{keyHint("e", "вкл/выкл"), keyHint("o", "открыть в Telegram")}
	case m.tab == tabTargets:
		keys = []string{keyHint("↑↓", "выбор"), keyHint("пробел", "вкл/выкл"), keyHint("a", "свой сайт"), keyHint("x", "исключение"), keyHint("del", "удалить")}
	case m.tab == tabLogs:
		keys = []string{keyHint("↑↓", "листать"), keyHint("G", "в конец")}
	case m.tab == tabSystem:
		keys = []string{keyHint("u", "обновить"), keyHint("a", "автообновление"), keyHint("f", "убрать конфликты"), keyHint("i", "установить службу")}
	}
	if !m.inMenu {
		keys = append(keys, keyHint("esc", "меню"))
	}
	keys = append(keys, keyHint("q", "выход"))
	line := " " + strings.Join(keys, "  ")
	for lipgloss.Width(line) > w && len(keys) > 1 {
		keys = keys[:len(keys)-1]
		line = " " + strings.Join(keys, "  ")
	}
	t := ""
	if m.toast != "" && time.Since(m.toastAt) < 6*time.Second {
		st := sBeam
		mark := "✓ "
		if !m.toastOK {
			st, mark = sBlock, "✕ "
		}
		t = st.Render(truncate(mark+m.toast, max(10, w-2)))
	}
	return " " + sFaint.Render(strings.Repeat("─", max(10, min(w-2, 72)))) + "\n" + line + "\n " + t
}
