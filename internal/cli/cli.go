// Package cli implements the command line and the daemon entry point of
// Просвет; a bare "prosvet" opens the full-screen interface (package tui).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"prosvet/internal/config"
	"prosvet/internal/core"
	"prosvet/internal/daemon"
	"prosvet/internal/engine"
	"prosvet/internal/lists"
	"prosvet/internal/logbuf"
	"prosvet/internal/platform"
	"prosvet/internal/service"
	"prosvet/internal/strategy"
	"prosvet/internal/tuner"
	"prosvet/internal/ui"
)

const usage = `Просвет — обход DPI: YouTube, Discord, Telegram, Rainbow Six Siege, For Honor

  zapret  (или prosvet)       панель — полноэкранный интерфейс
                              (на Windows — двойной щелчок по zapret.bat)
  prosvet status              состояние и здоровье целей
  prosvet tune [--deep] [--synth N] [группы…]
                              автоподбор; --synth — сколько поколений выводить
                              новые рецепты скрещиванием лучших
  prosvet start | stop        включить / выключить обход
  prosvet check               проверить доступность прямо сейчас
  prosvet games               пинг до дата-центров Siege, путь пакетов, VPN
  prosvet games pin [регион]  закрепить ближайший (или указанный) дата-центр Siege
  prosvet games restore       вернуть настройки Siege как были
  prosvet games direct on|off игровой UDP мимо VPN (Linux)
  prosvet telegram            прокси для Telegram Desktop (без службы — прямо здесь)
  prosvet enable  <цель>      youtube discord telegram r6 forhonor cloud sites
  prosvet disable <цель>
  prosvet domains [add|rm] <домен…>   свои сайты для обхода
  prosvet exclude [add|rm] <домен…>   сайты, которые не трогать никогда
  prosvet recipes [группа]    рецепты (включая выведенные) и выбранный
  prosvet pick <группа> <рецепт>      выбрать рецепт вручную
  prosvet update [--check]    обновиться до последней версии (сама служба
                              проверяет обновления каждые 6 часов)
  prosvet update --auto off   выключить автообновление (on — включить)
  prosvet update --mirror https://…   своё зеркало на случай недоступности GitHub
  prosvet install             установить службу с автозапуском (+ подбор)
  prosvet uninstall [--purge] удалить службу
  prosvet system              платформа, движок, конфликты
  prosvet fix                 остановить конфликтующие обходы (Flowseal, GoodbyeDPI…)
  prosvet lists update        обновить IP-диапазоны облаков из RIPEstat/AWS
  prosvet logs [-f]           журнал
  prosvet export [sh|bat]     выгрузить текущую стратегию отдельным скриптом
  prosvet run                 работать в этом окне без службы (Ctrl+C — выход)
  prosvet version
`

// Main runs the command line and returns the exit code.
func Main(args []string) int {
	defer func() {
		if localCtl != nil {
			localCtl.Shutdown()
		}
	}()
	if len(args) == 0 {
		if runtime.GOOS == "windows" && daemon.IsService() {
			return runDaemon(false)
		}
		return menu()
	}
	cmd, rest := args[0], args[1:]
	var code int
	switch cmd {
	case "daemon":
		code = runDaemon(false)
	case "run":
		code = runDaemon(true)
	case "status", "st":
		code = cmdStatus()
	case "tune":
		code = cmdTune(rest)
	case "start", "on":
		code = simple(func(ctx context.Context, a api) error { return a.Start(ctx) }, "обход включён")
	case "stop", "off":
		code = simple(func(ctx context.Context, a api) error { return a.Stop(ctx) }, "обход выключен")
	case "check":
		code = cmdCheck()
	case "games", "game", "ping":
		code = cmdGames(rest)
	case "telegram", "tg":
		code = cmdTelegram(rest)
	case "update", "upgrade":
		code = cmdUpdate(rest)
	case "enable", "disable":
		code = cmdTarget(cmd == "enable", rest)
	case "domains", "exclude":
		code = cmdDomains(cmd == "exclude", rest)
	case "recipes":
		code = cmdRecipes(rest)
	case "pick":
		code = cmdPick(rest)
	case "install":
		code = cmdInstall(rest)
	case "uninstall":
		code = cmdUninstall(rest)
	case "system", "doctor":
		code = cmdSystem()
	case "fix":
		code = cmdFix()
	case "lists":
		code = cmdLists(rest)
	case "logs":
		code = cmdLogs(rest)
	case "export":
		code = cmdExport(rest)
	case "version", "--version", "-v":
		fmt.Printf("prosvet %s · zapret %s · %s/%s\n", core.Version, engine.Version(), runtime.GOOS, runtime.GOARCH)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q\n\n%s", cmd, usage)
		code = 2
	}
	return code
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, ui.Red("✕ ")+err.Error())
	if errors.Is(err, errNeedRoot) {
		if runtime.GOOS == "windows" {
			fmt.Fprintln(os.Stderr, ui.Dim("  запустите от имени администратора или установите службу: prosvet install"))
		} else {
			fmt.Fprintln(os.Stderr, ui.Dim("  установите службу: sudo prosvet install   (или разово: sudo prosvet run)"))
		}
	}
	return 1
}

func ok(msg string) { fmt.Println(ui.Accent("✓ ") + msg) }

func ctxT(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func simple(fn func(context.Context, api) error, msg string) int {
	a, isLocal, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctxT(2 * time.Minute)
	defer cancel()
	if err := fn(ctx, a); err != nil {
		return fail(err)
	}
	ok(msg)
	if isLocal && msg == "обход включён" {
		fmt.Println(ui.Dim("  служба не установлена — обход работает, пока открыто это окно (Ctrl+C — выход)"))
		waitInterrupt()
		_ = a.Stop(context.Background())
	}
	return 0
}

func waitInterrupt() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	<-ch
	fmt.Println()
}

// ---------------------------------------------------------------- daemon

func runDaemon(foreground bool) int {
	paths := config.DefaultPaths()
	if !platform.Detect().Elevated {
		return fail(errors.New("демону нужны права root/администратора"))
	}
	log := logbuf.New(3000)
	var file *os.File
	if err := os.MkdirAll(filepath.Dir(paths.LogFile), 0o755); err == nil {
		file, _ = os.OpenFile(paths.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	if file != nil {
		defer file.Close()
	}
	log.SetSink(func(e logbuf.Entry) {
		if file != nil {
			fmt.Fprintf(file, "%s %-6s %-7s %s\n", e.Time.Format("2006-01-02 15:04:05"), e.Level, e.Source, e.Text)
		}
		if foreground && (e.Level != "engine" || strings.Contains(e.Text, "error") || strings.Contains(e.Text, "could not")) {
			fmt.Printf("%s %s %s\n", ui.Dim(e.Time.Format("15:04:05")), ui.Dim(ui.Pad(e.Source, 7)), e.Text)
		}
	})
	err := daemon.RunService(func(ctx context.Context) error {
		c, err := core.New(paths, log)
		if err != nil {
			return err
		}
		defer c.Shutdown()
		if foreground || c.Config().Enabled {
			if err := c.Start(); err != nil {
				log.Errorf("daemon", "%v", err)
			}
		}
		c.StartHealer()
		if !foreground {
			c.StartUpdater(ctx)
		}
		service.AfterStart()
		srv := &daemon.Server{C: c}
		log.Infof("daemon", "API: %s", paths.Socket)
		sctx, cancel := context.WithCancel(ctx)
		defer cancel()
		errc := make(chan error, 1)
		go func() { errc <- srv.Serve(sctx, paths.Socket) }()
		select {
		case err := <-errc:
			return err
		case <-c.RestartRequested():
			cancel()
			<-errc
			return core.ErrRestart
		}
	})
	if errors.Is(err, core.ErrRestart) {
		if file != nil {
			file.Close()
		}
		return restartSelf()
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

// ---------------------------------------------------------------- status

func cmdStatus() int {
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctxT(10 * time.Second)
	defer cancel()
	s, err := a.Status(ctx)
	if err != nil {
		return fail(err)
	}
	ui.StatusBoard(os.Stdout, s)
	return 0
}

// ---------------------------------------------------------------- tune

func cmdTune(rest []string) int {
	fs := flag.NewFlagSet("tune", flag.ContinueOnError)
	deep := fs.Bool("deep", false, "перебрать все имена-приманки")
	synth := fs.Int("synth", 0, "поколений селекции новых рецептов (0 — по умолчанию, -1 — без селекции)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	a, isLocal, err := connect()
	if err != nil {
		return fail(err)
	}
	code := runTune(a, core.TuneOptions{Deep: *deep, Groups: fs.Args(), Synth: *synth})
	if isLocal && code == 0 {
		fmt.Println(ui.Dim("\n  Подбор сохранён. Чтобы обход работал постоянно: ") + ui.Bold(sudoHint()+"prosvet install"))
	}
	return code
}

func sudoHint() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return "sudo "
}

// runTune starts tuning and renders the live matrix until it finishes.
func runTune(a api, o core.TuneOptions) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := a.Status(ctx)
	if err != nil {
		return fail(err)
	}
	if len(st.Platform.Conflicts) > 0 {
		for _, c := range st.Platform.Conflicts {
			fmt.Println(ui.Amber("⚠ ") + c.Name + ui.Dim(" — "+c.Detail))
		}
		fmt.Println(ui.Amber("  Другой обход исказит результаты подбора. Остановите его: ") + ui.Bold("prosvet fix"))
		if !confirm("Продолжить всё равно?", false) {
			return 1
		}
	}
	var groups []string
	for _, g := range st.Groups {
		if g.Testable && (len(o.Groups) == 0 || slices.Contains(o.Groups, g.ID)) {
			groups = append(groups, g.ID)
		}
	}
	ui.Header(os.Stdout, st)
	fmt.Println(ui.Dim(" Подбираю рецепты для каждой группы трафика. Интернет может мигать — это нормально."))
	ui.Legend(os.Stdout)
	fmt.Println()
	m := ui.NewMatrix(os.Stdout, groups)

	done := make(chan *tuner.Report, 1)
	evErr := make(chan error, 1)
	go func() {
		evErr <- a.Events(ctx, func(e daemon.RawEvent) {
			switch e.Type {
			case "tune":
				var te tuner.Event
				if json.Unmarshal(e.Data, &te) == nil {
					m.Feed(te)
				}
			case "status":
				var s core.Status
				if json.Unmarshal(e.Data, &s) == nil && s.Tuning != nil && !s.Tuning.Active {
					if s.Tuning.Error != "" {
						fmt.Println(ui.Red("✕ ") + s.Tuning.Error)
						done <- nil
						return
					}
					if s.Tuning.Report != nil {
						done <- s.Tuning.Report
					}
				}
			}
		})
	}()
	// Give the event stream a moment to attach before starting.
	time.Sleep(150 * time.Millisecond)
	if err := a.Tune(ctx, o); err != nil {
		return fail(err)
	}
	intr := make(chan os.Signal, 1)
	signal.Notify(intr, os.Interrupt)
	defer signal.Stop(intr)
	select {
	case rep := <-done:
		fmt.Println()
		if rep == nil {
			return 1
		}
		ui.Report(os.Stdout, rep)
		return 0
	case <-intr:
		_ = a.CancelTune(context.Background())
		fmt.Println(ui.Amber("\n  подбор отменён"))
		return 130
	case err := <-evErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fail(err)
		}
		return 1
	}
}

// ---------------------------------------------------------------- check

func cmdCheck() int {
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctxT(2 * time.Minute)
	defer cancel()
	fmt.Println(ui.Dim(" проверяю…"))
	rs, err := a.Check(ctx)
	if err != nil {
		return fail(err)
	}
	ui.Results(os.Stdout, rs)
	return 0
}

// ---------------------------------------------------------------- targets

func cmdTarget(on bool, rest []string) int {
	if len(rest) == 0 {
		fmt.Println("цели:")
		for _, s := range strategy.Services {
			fmt.Printf("  %-9s %s — %s\n", s.ID, s.Title, ui.Dim(s.Note))
		}
		return 0
	}
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctxT(time.Minute)
	defer cancel()
	for _, id := range rest {
		if err := a.SetService(ctx, strings.ToLower(id), on); err != nil {
			return fail(err)
		}
		state := "включена"
		if !on {
			state = "выключена"
		}
		ok("цель " + id + " " + state)
	}
	if on {
		fmt.Println(ui.Dim("  новые цели работают на рецептах по умолчанию — подберите их: prosvet tune"))
	}
	return 0
}

func cmdDomains(exclude bool, rest []string) int {
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctxT(time.Minute)
	defer cancel()
	st, err := a.Status(ctx)
	if err != nil {
		return fail(err)
	}
	cur := st.Domains
	if exclude {
		cur = st.Exclude
	}
	if len(rest) == 0 || rest[0] == "list" {
		if len(cur) == 0 {
			fmt.Println(ui.Dim("  список пуст"))
		}
		for _, d := range cur {
			fmt.Println("  " + d)
		}
		return 0
	}
	op, items := rest[0], rest[1:]
	switch op {
	case "add":
		cur = append(cur, items...)
	case "rm", "remove", "del":
		var keep []string
		for _, d := range cur {
			if !slices.Contains(items, d) {
				keep = append(keep, d)
			}
		}
		cur = keep
	default:
		cur = append(cur, rest...) // "prosvet domains example.com"
	}
	clean, err := a.SetDomains(ctx, cur, exclude)
	if err != nil {
		return fail(err)
	}
	ok(fmt.Sprintf("в списке %d доменов", len(clean)))
	for _, it := range items {
		if lists.NormalizeDomain(it) == "" {
			fmt.Println(ui.Amber("  пропущено (не домен): ") + it)
		}
	}
	return 0
}

// ---------------------------------------------------------------- recipes

func cmdRecipes(rest []string) int {
	var st *core.Status
	if a, _, err := connect(); err == nil {
		ctx, cancel := ctxT(10 * time.Second)
		if s, err := a.Status(ctx); err == nil {
			st = &s
		}
		// Recipes bred by the service live in its process; learn them.
		if rep, err := a.Report(ctx); err == nil && rep != nil {
			for _, b := range rep.Bred {
				strategy.Register(b)
			}
		}
		cancel()
	}
	for _, g := range strategy.Groups {
		if len(rest) > 0 && !slices.Contains(rest, g.ID) {
			continue
		}
		cur := g.Default
		if st != nil {
			for _, gs := range st.Groups {
				if gs.ID == g.ID {
					cur = gs.Pick
				}
			}
		}
		ui.Rule(os.Stdout, g.Title+" ("+g.ID+")", 78)
		for _, c := range strategy.Candidates(g.Kind) {
			mark := "  "
			base, _, _ := strings.Cut(cur, "@")
			if c.Base == base || c.ID == cur {
				mark = ui.Accent("▸ ")
			}
			fmt.Printf(" %s%s %s\n", mark, ui.Pad(c.Base, 26), ui.Dim(c.Label))
		}
		if strings.Contains(cur, "@") {
			fmt.Println(ui.Dim("   приманка: " + cur[strings.Index(cur, "@")+1:]))
		}
	}
	fmt.Println(ui.Dim("\n выбрать вручную: prosvet pick <группа> <рецепт>[@имя-приманки]"))
	return 0
}

func cmdPick(rest []string) int {
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "использование: prosvet pick <группа> <рецепт>[@имя]")
		return 2
	}
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctxT(time.Minute)
	defer cancel()
	if err := a.SetPick(ctx, rest[0], rest[1]); err != nil {
		return fail(err)
	}
	ok("группа " + rest[0] + " → " + rest[1])
	return 0
}

// ---------------------------------------------------------------- system

func cmdSystem() int {
	info := platform.Detect()
	st := service.Query(info)
	kv := func(k, v string) { fmt.Printf("  %s %s\n", ui.Pad(ui.Dim(k), 22), v) }
	ui.Rule(os.Stdout, "система", 78)
	kv("ОС", fmt.Sprintf("%s %s", info.Distro.Name, info.Distro.Version))
	kv("ядро / сборка", info.Kernel)
	kv("архитектура", info.MachineArch+ui.Dim(" (бинарник "+info.Arch+")"))
	if info.OS == "linux" {
		kv("init", info.Init)
		kv("файрвол", info.Firewall)
		if info.PackageManager != "" {
			kv("пакеты", info.PackageManager)
		}
	}
	kv("права", map[bool]string{true: ui.Accent("администратор"), false: "пользователь"}[info.Elevated])
	ui.Rule(os.Stdout, "движок", 78)
	kv("zapret", engine.Version()+" · "+info.Engine+map[bool]string{true: " (встроен)", false: ui.Amber(" (системный)")}[info.EngineBundled])
	kv("служба", serviceText(st))
	kv("IP-снимок", lists.SnapshotDate())
	if len(info.Missing) > 0 {
		ui.Rule(os.Stdout, "не хватает", 78)
		for _, m := range info.Missing {
			fmt.Printf("  %s %s  %s\n", ui.Red("✕"), m, ui.Dim(info.InstallHint(m)))
		}
	}
	if len(info.Conflicts) > 0 {
		ui.Rule(os.Stdout, "конфликты", 78)
		for _, c := range info.Conflicts {
			fix := ui.Dim("отключите вручную")
			if c.Fixable {
				fix = ui.Dim("prosvet fix")
			}
			fmt.Printf("  %s %s — %s  %s\n", ui.Amber("⚠"), c.Name, c.Detail, fix)
		}
	}
	for _, n := range info.Notes {
		fmt.Println("  " + ui.Dim("· "+n))
	}
	return 0
}

func serviceText(st service.State) string {
	switch {
	case st.Running:
		return ui.Accent("работает") + ui.Dim(" ("+st.Manager+")")
	case st.Installed:
		return ui.Amber("установлена, не запущена")
	}
	return ui.Dim("не установлена")
}

func cmdFix() int {
	info := platform.Detect()
	if len(info.Conflicts) == 0 {
		ok("конфликтов нет")
		return 0
	}
	a, _, err := connect()
	for _, c := range info.Conflicts {
		if !c.Fixable {
			fmt.Println(ui.Amber("⚠ ") + c.Name + ui.Dim(" — отключите вручную: "+c.Detail))
			continue
		}
		if err == nil {
			ctx, cancel := ctxT(30 * time.Second)
			e := a.FixConflict(ctx, c.Ref)
			cancel()
			if e != nil {
				fmt.Println(ui.Red("✕ ") + c.Name + ": " + e.Error())
				continue
			}
		} else if e := platform.FixConflict(c); e != nil {
			fmt.Println(ui.Red("✕ ") + c.Name + ": " + e.Error())
			continue
		}
		ok("остановлено: " + c.Name)
	}
	return 0
}

// ---------------------------------------------------------------- lists

func cmdLists(rest []string) int {
	if len(rest) == 0 || rest[0] != "update" {
		fmt.Println("использование: prosvet lists update")
		for _, p := range lists.Providers {
			fmt.Printf("  %-13s %s %s\n", p.ID, p.Name, ui.Dim(fmt.Sprint(p.ASNs)))
		}
		return 0
	}
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctxT(10 * time.Minute)
	defer cancel()
	fmt.Println(ui.Dim(" скачиваю диапазоны из RIPEstat и AWS…"))
	counts, err := a.UpdateLists(ctx)
	if err != nil {
		return fail(err)
	}
	ids := make([]string, 0, len(counts))
	total := 0
	for id, n := range counts {
		ids = append(ids, id)
		total += n
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  %-13s %6d\n", id, counts[id])
	}
	ok(fmt.Sprintf("обновлено: %d префиксов", total))
	return 0
}

// ---------------------------------------------------------------- logs

func cmdLogs(rest []string) int {
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	print := func(e logbuf.Entry) {
		lvl := ui.Dim
		switch e.Level {
		case "warn":
			lvl = ui.Amber
		case "error":
			lvl = ui.Red
		}
		fmt.Printf("%s %s %s\n", ui.Dim(e.Time.Format("15:04:05")), ui.Dim(ui.Pad(e.Source, 7)), lvl(e.Text))
	}
	ctx, cancel := ctxT(10 * time.Second)
	entries, err := a.Logs(ctx)
	cancel()
	if err != nil {
		return fail(err)
	}
	for _, e := range entries {
		print(e)
	}
	if len(rest) > 0 && (rest[0] == "-f" || rest[0] == "--follow") {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { waitInterrupt(); cancel() }()
		_ = a.Events(ctx, func(e daemon.RawEvent) {
			if e.Type == "log" {
				var le logbuf.Entry
				if json.Unmarshal(e.Data, &le) == nil {
					print(le)
				}
			}
		})
	}
	return 0
}

// ---------------------------------------------------------------- export

func cmdExport(rest []string) int {
	format := "sh"
	if runtime.GOOS == "windows" {
		format = "bat"
	}
	if len(rest) > 0 {
		format = rest[0]
	}
	a, _, err := connect()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctxT(30 * time.Second)
	defer cancel()
	p, err := a.Plan(ctx)
	if err != nil {
		return fail(err)
	}
	var plan strategy.Plan
	if err := json.Unmarshal(p.Plan, &plan); err != nil {
		return fail(err)
	}
	quote := func(s string) string {
		if format == "bat" {
			if strings.ContainsAny(s, " &^") {
				return `"` + s + `"`
			}
			return s
		}
		if strings.ContainsAny(s, " '\"$`\\!*?") {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
		return s
	}
	var b strings.Builder
	if format == "bat" {
		b.WriteString("@echo off\r\nchcp 65001 >nul\r\n:: Просвет — выгрузка стратегии " + time.Now().Format("2006-01-02") + "\r\n")
		b.WriteString("start \"prosvet\" /min " + quote(p.Engine))
		for _, a := range plan.Args {
			b.WriteString(" ^\r\n " + quote(a))
		}
		b.WriteString("\r\n")
	} else {
		b.WriteString("#!/bin/sh\n# Просвет — выгрузка стратегии " + time.Now().Format("2006-01-02") + "\nset -e\n")
		if p.Firewall != "" {
			b.WriteString("nft -f - <<'NFT'\n" + p.Firewall + "NFT\n")
			b.WriteString("trap 'nft delete table inet prosvet' EXIT INT TERM\n")
		}
		b.WriteString(quote(p.Engine))
		for _, a := range plan.Args {
			b.WriteString(" \\\n  " + quote(a))
		}
		b.WriteString("\n")
	}
	fmt.Print(b.String())
	return 0
}

// ---------------------------------------------------------------- helpers

func confirm(q string, def bool) bool {
	hint := " [y/N] "
	if def {
		hint = " [Y/n] "
	}
	fmt.Print(q + ui.Dim(hint))
	var s string
	if _, err := fmt.Scanln(&s); err != nil {
		return def
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return def
	}
	return s == "y" || s == "yes" || s == "д" || s == "да"
}

func ownerUID() int {
	for _, k := range []string{"SUDO_UID", "PKEXEC_UID"} {
		if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
			return v
		}
	}
	return 0
}
