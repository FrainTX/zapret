// Package core is the brain shared by the daemon and the standalone CLI: it
// owns the configuration, the engine process, the firewall rules, tuning and
// the self-healing health monitor.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"prosvet/internal/config"
	"prosvet/internal/engine"
	"prosvet/internal/firewall"
	"prosvet/internal/lists"
	"prosvet/internal/logbuf"
	"prosvet/internal/netroute"
	"prosvet/internal/platform"
	"prosvet/internal/probe"
	"prosvet/internal/strategy"
	"prosvet/internal/tgproxy"
	"prosvet/internal/tuner"
)

// Version of prosvet itself (set by the linker).
var Version = "1.1.2"

// GroupStatus is the live state of one traffic group.
type GroupStatus struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Kind      string         `json:"kind"`
	Services  []string       `json:"services"`
	Pick      string         `json:"pick"`
	PickLabel string         `json:"pickLabel"`
	PickTags  []string       `json:"pickTags"`
	Testable  bool           `json:"testable"`
	Health    string         `json:"health"` // ok, partial, fail, unknown
	OK        int            `json:"ok"`
	Total     int            `json:"total"`
	Latency   int            `json:"latency"`
	Checked   time.Time      `json:"checked"`
	Results   []probe.Result `json:"results,omitempty"`
}

// Status is everything the UI shows on its main screen.
type Status struct {
	Version       string             `json:"version"`
	Running       bool               `json:"running"`
	Enabled       bool               `json:"enabled"`
	Since         time.Time          `json:"since"`
	Engine        string             `json:"engine"`
	EngineVersion string             `json:"engineVersion"`
	Firewall      string             `json:"firewall"`
	Services      map[string]bool    `json:"services"`
	ServiceList   []strategy.Service `json:"serviceList"`
	Groups        []GroupStatus      `json:"groups"`
	Tuning        *TuneProgress      `json:"tuning,omitempty"`
	LastTune      time.Time          `json:"lastTune"`
	AutoHeal      bool               `json:"autoHeal"`
	HealMinutes   int                `json:"healMinutes"`
	IPv6          bool               `json:"ipv6"`
	Domains       []string           `json:"domains"`
	Exclude       []string           `json:"exclude"`
	ListsDate     string             `json:"listsDate"`
	Platform      platform.Info      `json:"platform"`
	Error         string             `json:"error,omitempty"`
	Telegram      *TelegramStatus    `json:"telegram,omitempty"`
	Route         netroute.Info      `json:"route"`
	// Direct is set while game UDP is routed around the VPN.
	Direct     bool          `json:"direct"`
	GameDirect string        `json:"gameDirect"`
	Update     *UpdateStatus `json:"update,omitempty"`
}

// TuneProgress is the state of a running or finished tuning session.
type TuneProgress struct {
	Active  bool          `json:"active"`
	Deep    bool          `json:"deep"`
	Stage   int           `json:"stage"`
	Round   int           `json:"round"`
	Started time.Time     `json:"started"`
	Message string        `json:"message"`
	Events  []tuner.Event `json:"events"`
	Report  *tuner.Report `json:"report,omitempty"`
	Error   string        `json:"error,omitempty"`
}

// Event is pushed to live subscribers (UI).
type Event struct {
	Type string `json:"type"` // status, tune, log, health
	Data any    `json:"data"`
}

// Controller coordinates everything. All methods are safe for concurrent use.
type Controller struct {
	Paths config.Paths
	Log   *logbuf.Buffer

	store *config.Store
	files *engine.Files
	proc  *engine.Process
	fw    firewall.Firewall
	info  platform.Info

	mu       sync.Mutex
	cfg      *config.Config
	health   map[string]*GroupStatus
	tune     *TuneProgress
	cancel   context.CancelFunc
	lastErr  string
	tg       *tgproxy.Proxy
	tgCancel context.CancelFunc
	fwRules  []strategy.PortRule
	fwActive bool
	fwDirect *firewall.Direct
	fwSkip   string
	upd      UpdateStatus
	restart  chan struct{}
	route    netroute.Info
	netOnce  sync.Once

	subMu sync.Mutex
	subs  map[chan Event]struct{}

	healStop chan struct{}
}

// New prepares a controller: extracts the engine and writes list files.
func New(paths config.Paths, log *logbuf.Buffer) (*Controller, error) {
	if log == nil {
		log = logbuf.New(2000)
	}
	c := &Controller{
		Paths:   paths,
		Log:     log,
		store:   config.NewStore(paths.ConfigFile),
		health:  map[string]*GroupStatus{},
		subs:    map[chan Event]struct{}{},
		restart: make(chan struct{}, 1),
	}
	cfg, err := c.store.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.cfg = cfg
	for _, b := range cfg.Bred {
		strategy.Register(b)
	}
	c.info = platform.Detect()
	for _, d := range []string{paths.StateDir, paths.RunDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := engine.Install(paths.Engine(), paths.Fakes()); err != nil {
		return nil, err
	}
	files, err := engine.NewFiles(paths.Fakes(), paths.Lists())
	if err != nil {
		return nil, err
	}
	c.files = files
	if err := c.writeLists(); err != nil {
		return nil, err
	}
	bin, err := engine.Locate(paths.Engine())
	if err != nil {
		return nil, err
	}
	c.proc = &engine.Process{Bin: bin, Log: log}
	fw, err := firewall.New()
	if err != nil {
		return nil, err
	}
	c.fw = fw
	log.Infof("core", "Просвет %s · движок zapret %s · %s", Version, engine.Version(), fw.Name())
	return c, nil
}

func (c *Controller) writeLists() error {
	c.mu.Lock()
	o := engine.ListOptions{Custom: c.cfg.Domains, Exclude: c.cfg.Exclude, Providers: c.cfg.Providers}
	c.mu.Unlock()
	return c.files.WriteLists(o)
}

// Config returns a copy of the configuration.
func (c *Controller) Config() *config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.Clone()
}

func (c *Controller) save() error {
	return c.store.Save(c.cfg)
}

// ---------------------------------------------------------------- events

// Subscribe returns a channel of live events.
func (c *Controller) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 128)
	c.subMu.Lock()
	c.subs[ch] = struct{}{}
	c.subMu.Unlock()
	logs, cancelLogs := c.Log.Subscribe()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case e, ok := <-logs:
				if !ok {
					return
				}
				c.publishTo(ch, Event{Type: "log", Data: e})
			case <-done:
				return
			}
		}
	}()
	return ch, func() {
		close(done)
		cancelLogs()
		c.subMu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.subMu.Unlock()
	}
}

func (c *Controller) publishTo(ch chan Event, e Event) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	if _, ok := c.subs[ch]; !ok {
		return
	}
	select {
	case ch <- e:
	default:
	}
}

func (c *Controller) publish(e Event) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for ch := range c.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (c *Controller) statusChanged() { c.publish(Event{Type: "status", Data: c.Status()}) }

// ---------------------------------------------------------------- status

// Status returns the current state.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg := c.cfg
	s := Status{
		Version:       Version,
		Running:       c.proc.Running(),
		Enabled:       cfg.Enabled,
		Engine:        engine.BinaryName(),
		EngineVersion: engine.Version(),
		Firewall:      c.fw.Name(),
		Services:      cfg.Services,
		ServiceList:   strategy.Services,
		LastTune:      cfg.Tuned,
		AutoHeal:      cfg.AutoHeal,
		HealMinutes:   cfg.HealMinutes,
		IPv6:          cfg.IPv6,
		Domains:       cfg.Domains,
		Exclude:       cfg.Exclude,
		ListsDate:     lists.SnapshotDate(),
		Platform:      c.info,
		Error:         c.lastErr,
		Telegram:      c.telegramStatusLocked(),
		Route:         c.route,
		Direct:        c.fwActive && c.fwDirect != nil,
		GameDirect:    cfg.GameDirect,
		Update:        c.updateStatusLocked(),
	}
	if s.Running {
		s.Since = c.proc.Since()
	}
	for _, g := range strategy.ActiveGroups(cfg.Services) {
		cand := cfg.Pick(g)
		gs := GroupStatus{ID: g.ID, Title: g.Title, Kind: string(g.Kind), Services: g.Services, Pick: cand.ID,
			PickLabel: cand.Label, PickTags: strategy.Tags(cand.Args), Testable: g.Testable, Health: "unknown"}
		if h := c.health[g.ID]; h != nil {
			gs.Health, gs.OK, gs.Total, gs.Latency, gs.Checked, gs.Results = h.Health, h.OK, h.Total, h.Latency, h.Checked, h.Results
		}
		s.Groups = append(s.Groups, gs)
	}
	if c.tune != nil {
		cp := *c.tune
		cp.Events = append([]tuner.Event(nil), c.tune.Events...)
		s.Tuning = &cp
	}
	return s
}

// Platform returns the detected platform (re-detected on each call).
func (c *Controller) Platform() platform.Info {
	info := platform.Detect()
	c.mu.Lock()
	c.info = info
	c.mu.Unlock()
	return info
}

// ---------------------------------------------------------------- engine

func (c *Controller) composeLocked(sel func(strategy.Group) strategy.Candidate) (*strategy.Plan, []strategy.Group, error) {
	groups := strategy.ActiveGroups(c.cfg.Services)
	env := c.files.Env(runtime.GOOS, c.cfg.QNum)
	plan, err := strategy.Compose(env, groups, sel)
	if err == nil && len(plan.Args) > 0 && runtime.GOOS == "linux" && !dropPrivilegesOK(c.Paths.StateDir) {
		// nfqws drops to uid 0x7FFFFFFF after start. That uid does not exist
		// in a user namespace (rootless containers) and cannot read a state
		// directory that is not world-traversable (portable mode in $HOME).
		plan.Args = append([]string{"--uid=0:0"}, plan.Args...)
	}
	return plan, groups, err
}

func (c *Controller) setsLocked() map[string][]lists.Prefix {
	return map[string][]lists.Prefix{"cloud": c.files.SetPrefixes("cloud"), "game": c.files.SetPrefixes("game")}
}

// applyLocked (re)starts the engine with plan and installs firewall rules.
// It reports whether a new engine process was started.
func (c *Controller) applyLocked(plan *strategy.Plan, rules []strategy.PortRule) (bool, error) {
	direct := c.directLocked()
	if len(plan.Groups) == 0 {
		_ = c.proc.Stop()
		if direct != nil {
			// Nothing needs a bypass, but games still deserve the short path.
			if !c.fwActive || len(c.fwRules) > 0 || !directEqual(c.fwDirect, direct) || c.fwSkip != c.route.Tunnels {
				if err := c.fw.Apply(nil, c.setsLocked(), firewall.Options{QNum: c.cfg.QNum, IPv6: c.cfg.IPv6, Direct: direct, SkipIfaces: c.route.TunnelList()}); err != nil {
					c.Log.Warnf("core", "маршрут игр мимо VPN: %v", err)
					return false, nil
				}
				c.fwRules, c.fwActive, c.fwDirect, c.fwSkip = nil, true, direct, c.route.Tunnels
			}
			return false, nil
		}
		if c.fwActive {
			_ = c.fw.Clear()
			c.fwActive, c.fwDirect = false, nil
		}
		return false, nil
	}
	fwSame := c.fwActive && rulesEqual(c.fwRules, rules) && directEqual(c.fwDirect, direct) && c.fwSkip == c.route.Tunnels
	if c.proc.Running() && slices.Equal(c.proc.Args(), plan.Args) && fwSame {
		return false, nil
	}
	if !fwSame {
		err := c.fw.Apply(rules, c.setsLocked(), firewall.Options{QNum: c.cfg.QNum, IPv6: c.cfg.IPv6, Direct: direct, SkipIfaces: c.route.TunnelList()})
		if err != nil && direct != nil {
			// Never lose the bypass over the routing extra.
			c.Log.Warnf("core", "маршрут игр мимо VPN: %v", err)
			direct = nil
			err = c.fw.Apply(rules, c.setsLocked(), firewall.Options{QNum: c.cfg.QNum, IPv6: c.cfg.IPv6, SkipIfaces: c.route.TunnelList()})
		}
		if err != nil {
			return false, err
		}
		if direct != nil && !directEqual(c.fwDirect, direct) {
			c.Log.Infof("core", "игры идут мимо VPN %s: напрямую через %s (шлюз %s)", c.route.App, direct.Iface, direct.Gateway)
		}
		c.fwRules, c.fwActive, c.fwDirect, c.fwSkip = rules, true, direct, c.route.Tunnels
	}
	_ = c.proc.Stop()
	if err := c.proc.Start(plan.Args); err != nil {
		return false, err
	}
	return true, nil
}

func rulesEqual(a, b []strategy.PortRule) bool { return slices.Equal(a, b) }

// dropPrivilegesOK reports whether nfqws can safely switch to its
// unprivileged uid: we are in the initial user namespace and every
// directory up to dir is world-traversable.
func dropPrivilegesOK(dir string) bool {
	if platform.InUserNamespace() {
		return false
	}
	for d := dir; ; d = filepath.Dir(d) {
		st, err := os.Stat(d)
		if err != nil || st.Mode().Perm()&0o001 == 0 {
			return false
		}
		if parent := filepath.Dir(d); parent == d {
			return true
		}
	}
}

// Start launches the bypass with the stored picks.
func (c *Controller) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tune != nil && c.tune.Active {
		return errors.New("идёт подбор — дождитесь окончания")
	}
	plan, _, err := c.composeLocked(c.cfg.Pick)
	if err != nil {
		c.lastErr = err.Error()
		return err
	}
	started, err := c.applyLocked(plan, plan.Rules)
	if err != nil {
		c.lastErr = err.Error()
		c.Log.Errorf("core", "запуск: %v", err)
		go c.statusChanged()
		return err
	}
	c.lastErr = ""
	c.cfg.Enabled = true
	_ = c.save()
	c.syncTelegramLocked()
	if started {
		c.Log.Infof("core", "обход запущен: %s", strings.Join(plan.Groups, ", "))
		go c.watch()
	}
	c.netOnce.Do(func() { go c.netWatch() })
	go c.statusChanged()
	return nil
}

// watch restarts the engine if it dies unexpectedly.
func (c *Controller) watch() {
	done := c.proc.Done()
	<-done
	c.mu.Lock()
	enabled := c.cfg.Enabled
	tuning := c.tune != nil && c.tune.Active
	c.mu.Unlock()
	if !enabled || tuning {
		return
	}
	time.Sleep(2 * time.Second)
	c.mu.Lock()
	still := c.cfg.Enabled && !c.proc.Running() && !(c.tune != nil && c.tune.Active)
	c.mu.Unlock()
	if still {
		c.Log.Warnf("core", "движок завершился — перезапускаю")
		_ = c.Start()
	}
}

// Stop turns the bypass off and removes firewall rules.
func (c *Controller) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.Enabled = false
	_ = c.save()
	c.syncTelegramLocked()
	_ = c.proc.Stop()
	if c.fwActive || c.fw.Active() {
		if err := c.fw.Clear(); err != nil {
			c.Log.Warnf("core", "очистка правил: %v", err)
		}
		c.fwActive, c.fwDirect = false, nil
	}
	c.Log.Infof("core", "обход остановлен")
	go c.statusChanged()
	return nil
}

// Shutdown stops the engine without changing the "enabled" setting (used
// when the daemon exits, so the bypass comes back after reboot).
func (c *Controller) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	if c.tgCancel != nil {
		c.tgCancel()
		c.tg, c.tgCancel = nil, nil
	}
	_ = c.proc.Stop()
	if c.fwActive {
		_ = c.fw.Clear()
		c.fwActive, c.fwDirect = false, nil
	}
}

// restartIfRunning re-applies configuration after a settings change.
func (c *Controller) restartIfRunning() error {
	c.mu.Lock()
	enabled := c.cfg.Enabled
	c.mu.Unlock()
	if !enabled {
		return nil
	}
	return c.Start()
}

// ---------------------------------------------------------------- settings

// SetService toggles a user-facing target.
func (c *Controller) SetService(id string, on bool) error {
	known := false
	for _, s := range strategy.Services {
		known = known || s.ID == id
	}
	if !known {
		return fmt.Errorf("неизвестная цель %q", id)
	}
	c.mu.Lock()
	c.cfg.Services[id] = on
	err := c.save()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.Log.Infof("core", "цель %s: %v", id, on)
	return c.restartIfRunning()
}

// SetDomains replaces the user's list (validated).
func (c *Controller) SetDomains(domains []string, exclude bool) ([]string, error) {
	clean := lists.Merge(domains)
	if len(clean) > 5000 {
		return nil, errors.New("слишком длинный список")
	}
	c.mu.Lock()
	if exclude {
		c.cfg.Exclude = clean
	} else {
		c.cfg.Domains = clean
	}
	err := c.save()
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := c.writeLists(); err != nil {
		return nil, err
	}
	// nfqws re-reads list files on change, no restart needed.
	go c.statusChanged()
	return clean, nil
}

// SetPick stores a recipe for a group manually.
func (c *Controller) SetPick(group, candidate string) error {
	if _, ok := strategy.GroupByID(group); !ok {
		return fmt.Errorf("неизвестная группа %q", group)
	}
	cand, ok := strategy.ParseCandidateID(candidate)
	if !ok {
		return fmt.Errorf("неизвестный рецепт %q", candidate)
	}
	c.mu.Lock()
	c.cfg.Picks[group] = cand.ID
	err := c.save()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.restartIfRunning()
}

// Settings are the toggles of the "system" screen.
type Settings struct {
	AutoHeal    *bool `json:"autoHeal,omitempty"`
	HealMinutes *int  `json:"healMinutes,omitempty"`
	IPv6        *bool `json:"ipv6,omitempty"`
	// GameDirect: "auto" (or "") or "off".
	GameDirect *string `json:"gameDirect,omitempty"`
	// AutoUpdate and UpdateMirror control updates.
	AutoUpdate   *bool   `json:"autoUpdate,omitempty"`
	UpdateMirror *string `json:"updateMirror,omitempty"`
}

// ApplySettings updates simple options.
func (c *Controller) ApplySettings(s Settings) error {
	c.mu.Lock()
	restart := false
	if s.AutoHeal != nil {
		c.cfg.AutoHeal = *s.AutoHeal
	}
	if s.HealMinutes != nil && *s.HealMinutes >= 5 && *s.HealMinutes <= 240 {
		c.cfg.HealMinutes = *s.HealMinutes
	}
	if s.IPv6 != nil && *s.IPv6 != c.cfg.IPv6 {
		c.cfg.IPv6 = *s.IPv6
		restart = true
		c.fwRules = nil // force re-apply
	}
	if s.AutoUpdate != nil {
		c.cfg.NoAutoUpdate = !*s.AutoUpdate
	}
	if s.UpdateMirror != nil {
		m := strings.TrimSpace(*s.UpdateMirror)
		if err := validMirror(m); err != nil {
			c.mu.Unlock()
			return err
		}
		c.cfg.UpdateMirror = m
	}
	if s.GameDirect != nil {
		v := *s.GameDirect
		if v == "auto" {
			v = ""
		}
		if v != "" && v != "off" {
			c.mu.Unlock()
			return fmt.Errorf("gameDirect: auto или off")
		}
		if v != c.cfg.GameDirect {
			c.cfg.GameDirect = v
			restart = true
		}
	}
	err := c.save()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if restart {
		return c.restartIfRunning()
	}
	go c.statusChanged()
	return nil
}

// ---------------------------------------------------------------- tuning

type tuneRunner struct {
	c     *Controller
	rules []strategy.PortRule
}

func (r *tuneRunner) Apply(ctx context.Context, sel map[string]strategy.Candidate) error {
	c := r.c
	c.mu.Lock()
	defer c.mu.Unlock()
	plan, _, err := c.composeLocked(func(g strategy.Group) strategy.Candidate {
		if cand, ok := sel[g.ID]; ok {
			return cand
		}
		return c.cfg.Pick(g)
	})
	if err != nil {
		return err
	}
	_, err = c.applyLocked(plan, r.rules)
	return err
}

// TuneOptions configure Tune.
type TuneOptions struct {
	Deep   bool     `json:"deep"`
	Groups []string `json:"groups,omitempty"` // empty: all active testable groups
	// Synth is the number of breeding generations: 0 = default (2, or 4
	// when deep), -1 = no synthesis.
	Synth int `json:"synth,omitempty"`
}

// Tune starts a tuning session in the background.
func (c *Controller) Tune(o TuneOptions) error {
	c.mu.Lock()
	if c.tune != nil && c.tune.Active {
		c.mu.Unlock()
		return errors.New("подбор уже идёт")
	}
	active := strategy.ActiveGroups(c.cfg.Services)
	var groups []strategy.Group
	fixed := map[string]strategy.Candidate{}
	for _, g := range active {
		if g.Testable && (len(o.Groups) == 0 || slices.Contains(o.Groups, g.ID)) {
			groups = append(groups, g)
		} else {
			fixed[g.ID] = c.cfg.Pick(g)
		}
	}
	if len(groups) == 0 {
		c.mu.Unlock()
		return errors.New("нечего подбирать: включите хотя бы одну цель")
	}
	// During tuning every active group is queued, whatever its candidate.
	rules := strategy.RulesFor(active)
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.tune = &TuneProgress{Active: true, Deep: o.Deep, Started: time.Now(), Message: "подготовка"}
	custom := append([]string(nil), c.cfg.Domains...)
	wasEnabled := c.cfg.Enabled
	c.mu.Unlock()

	c.Log.Infof("tuner", "подбор начат (%s): %d групп", map[bool]string{true: "глубокий", false: "быстрый"}[o.Deep], len(groups))
	go c.statusChanged()

	go func() {
		defer cancel()
		synth := o.Synth
		switch {
		case synth == 0 && o.Deep:
			synth = 4
		case synth == 0:
			synth = 2
		case synth < 0:
			synth = 0
		}
		rep, err := tuner.Run(ctx, &tuneRunner{c: c, rules: rules}, tuner.Options{Groups: groups, Fixed: fixed, Deep: o.Deep, Custom: custom, Synth: min(synth, 12),
			Probe: probe.Options{Timeout: 5 * time.Second, Connect: 2500 * time.Millisecond, Bind: netroute.Detect().Direct()}},
			func(e tuner.Event) {
				c.mu.Lock()
				if c.tune != nil {
					if e.Stage > 0 {
						c.tune.Stage = e.Stage
					}
					if e.Round > 0 {
						c.tune.Round = e.Round
					}
					if e.Message != "" {
						c.tune.Message = e.Message
					}
					if e.Type == "trial" || e.Type == "verify" || e.Type == "stage" || e.Type == "done" || e.Type == "generation" || e.Type == "universal" {
						c.tune.Events = append(c.tune.Events, e)
						if len(c.tune.Events) > 600 {
							c.tune.Events = c.tune.Events[len(c.tune.Events)-600:]
						}
					}
				}
				c.mu.Unlock()
				c.publish(Event{Type: "tune", Data: e})
				if e.Type == "trial" {
					c.Log.Infof("tuner", "%-13s %-34s %d/%d %s", e.Group, e.Candidate, e.OK, e.Total, latencyText(e.Latency))
				}
			})
		c.mu.Lock()
		c.tune.Active = false
		c.cancel = nil
		if err != nil {
			c.tune.Error = err.Error()
			c.Log.Errorf("tuner", "подбор прерван: %v", err)
		} else {
			c.tune.Report = rep
			c.keepBredLocked(rep.Bred)
			for g, p := range rep.Picks {
				c.cfg.Picks[g] = p
			}
			if rep.Universal != "" {
				c.Log.Infof("tuner", "выведен универсальный рецепт %s для %s", rep.Universal, strings.Join(rep.UniversalGroups, ", "))
			}
			c.cfg.Tuned = time.Now()
			_ = c.save()
			c.saveReport(rep)
			for _, gr := range rep.Groups {
				c.health[gr.Group] = &GroupStatus{Health: healthOf(gr.Best.OK, gr.Best.Total), OK: gr.Best.OK, Total: gr.Best.Total, Latency: gr.Best.Latency, Checked: time.Now()}
			}
		}
		c.mu.Unlock()
		// Leave the engine in the tuned state if it was on, otherwise
		// restore "off" (rules removed).
		if wasEnabled || err == nil {
			_ = c.Start()
		} else {
			_ = c.Stop()
		}
		c.statusChanged()
	}()
	return nil
}

func latencyText(ms int) string {
	if ms <= 0 {
		return ""
	}
	return fmt.Sprintf("%d мс", ms)
}

func healthOf(ok, total int) string {
	switch {
	case total == 0:
		return "unknown"
	case ok == total:
		return "ok"
	case ok == 0:
		return "fail"
	}
	return "partial"
}

// CancelTune stops a running session.
func (c *Controller) CancelTune() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *Controller) saveReport(rep *tuner.Report) {
	b, err := json.MarshalIndent(rep, "", " ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(c.Paths.StateDir, "tune-report.json"), b, 0o644)
	}
}

// LastReport loads the last tuning report from disk.
func (c *Controller) LastReport() *tuner.Report {
	c.mu.Lock()
	if c.tune != nil && c.tune.Report != nil {
		r := c.tune.Report
		c.mu.Unlock()
		return r
	}
	c.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(c.Paths.StateDir, "tune-report.json"))
	if err != nil {
		return nil
	}
	var r tuner.Report
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// ---------------------------------------------------------------- health

// Check probes every active testable group with the engine as it is now.
func (c *Controller) Check(ctx context.Context) []probe.Result {
	c.mu.Lock()
	groups := strategy.ActiveGroups(c.cfg.Services)
	custom := append([]string(nil), c.cfg.Domains...)
	c.mu.Unlock()
	var targets []probe.Target
	for _, g := range groups {
		if g.Testable {
			targets = append(targets, probe.Targets(g.ID, custom)...)
		}
	}
	results := probe.RunAll(ctx, targets, probe.Options{Bind: netroute.Detect().Direct()}, 16)
	results = append(results, c.telegramProbe(ctx)...)
	by := map[string][]probe.Result{}
	for _, r := range results {
		by[r.Group] = append(by[r.Group], r)
	}
	c.mu.Lock()
	for g, rs := range by {
		gs := &GroupStatus{Checked: time.Now(), Results: rs, Total: len(rs)}
		var lat []int
		for _, r := range rs {
			if r.OK {
				gs.OK++
				lat = append(lat, int(r.Latency.Milliseconds()))
			}
		}
		slices.Sort(lat)
		if len(lat) > 0 {
			gs.Latency = lat[len(lat)/2]
		}
		gs.Health = healthOf(gs.OK, gs.Total)
		c.health[g] = gs
	}
	c.mu.Unlock()
	c.publish(Event{Type: "health", Data: results})
	go c.statusChanged()
	return results
}

// StartHealer runs periodic checks and re-tunes groups that broke.
func (c *Controller) StartHealer() {
	c.healStop = make(chan struct{})
	go func() {
		failures := map[string]int{}
		for {
			c.mu.Lock()
			period := time.Duration(c.cfg.HealMinutes) * time.Minute
			c.mu.Unlock()
			select {
			case <-c.healStop:
				return
			case <-time.After(period):
			}
			c.mu.Lock()
			skip := !c.cfg.Enabled || !c.proc.Running() || (c.tune != nil && c.tune.Active)
			auto := c.cfg.AutoHeal
			c.mu.Unlock()
			if skip {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			c.Check(ctx)
			cancel()
			var broken []string
			c.mu.Lock()
			for g, h := range c.health {
				if h.Health == "fail" {
					failures[g]++
				} else {
					failures[g] = 0
				}
				if failures[g] >= 2 {
					broken = append(broken, g)
				}
			}
			c.mu.Unlock()
			if len(broken) > 0 && auto {
				c.Log.Warnf("healer", "перестало работать: %s — подбираю заново", strings.Join(broken, ", "))
				if err := c.Tune(TuneOptions{Groups: broken}); err == nil {
					for _, g := range broken {
						failures[g] = 0
					}
				}
			}
		}
	}()
}

// ---------------------------------------------------------------- lists

// UpdateLists downloads fresh provider prefixes.
func (c *Controller) UpdateLists(ctx context.Context) (map[string]int, error) {
	var ids []string
	for _, p := range lists.Providers {
		ids = append(ids, p.ID)
	}
	f := &lists.Fetcher{}
	got, errs := f.FetchAll(ctx, ids, nil)
	counts := map[string]int{}
	for id, ps := range got {
		if err := c.files.SaveProvider(id, ps); err != nil {
			return nil, err
		}
		counts[id] = len(ps)
	}
	for id, err := range errs {
		c.Log.Warnf("lists", "%s: %v (оставлен встроенный снимок)", id, err)
	}
	if err := c.writeLists(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.fwRules = nil // sets changed: force firewall reload
	c.mu.Unlock()
	if err := c.restartIfRunning(); err != nil {
		return counts, err
	}
	c.Log.Infof("lists", "обновлено провайдеров: %d", len(counts))
	return counts, nil
}

// ---------------------------------------------------------------- export

// Plan renders the current configuration (for export / debugging).
func (c *Controller) Plan() (*strategy.Plan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	plan, _, err := c.composeLocked(c.cfg.Pick)
	return plan, err
}

// EngineBinary returns the engine path.
func (c *Controller) EngineBinary() string { return c.proc.Bin }

// FirewallScript returns the firewall rules as text (Linux nft only).
func (c *Controller) FirewallScript(plan *strategy.Plan) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.fw.(interface {
		Render([]strategy.PortRule, map[string][]lists.Prefix, firewall.Options) string
	}); ok {
		return r.Render(plan.Rules, c.setsLocked(), firewall.Options{QNum: c.cfg.QNum, IPv6: c.cfg.IPv6, SkipIfaces: c.route.TunnelList()})
	}
	return ""
}

// ---------------------------------------------------------------- conflicts

// FixConflict stops a conflicting program detected by the platform scan.
// Only conflicts that are actually present can be fixed, so the API cannot
// be abused to kill arbitrary processes.
func (c *Controller) FixConflict(ref string) error {
	for _, cf := range platform.Detect().Conflicts {
		if cf.Ref == ref {
			if !cf.Fixable {
				return fmt.Errorf("%s нужно отключить вручную", cf.Name)
			}
			if err := platform.FixConflict(cf); err != nil {
				return err
			}
			c.Log.Infof("core", "остановлено: %s", cf.Name)
			go c.statusChanged()
			return nil
		}
	}
	return fmt.Errorf("конфликт %q не найден", ref)
}
