package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"prosvet/internal/core"
	"prosvet/internal/games"
	"prosvet/internal/netroute"
	"prosvet/internal/tgproxy"
	"prosvet/internal/tui"
	"prosvet/internal/ui"
)

// cmdGames: prosvet games [ping | pin [регион] | restore | direct on|off]
func cmdGames(rest []string) int {
	sub := "ping"
	if len(rest) > 0 {
		sub, rest = rest[0], rest[1:]
	}
	switch sub {
	case "ping", "dc":
		samples := measure()
		printRoute()
		printSiegeConfigs()
		if len(samples) > 0 {
			if best, ok := games.Pick(samples, currentHint()); ok {
				fmt.Println(ui.Dim("\n  Закрепить ближайший дата-центр в Siege: ") + ui.Bold("prosvet games pin") + ui.Dim("  ("+best.Region.ID+")"))
			}
		}
		return 0
	case "pin":
		cfgs := games.FindSiege()
		if len(cfgs) == 0 {
			return fail(fmt.Errorf("GameSettings.ini Rainbow Six Siege не найден — запустите игру хотя бы раз"))
		}
		var region string
		if len(rest) > 0 && rest[0] != "auto" {
			if rest[0] == "default" {
				region = "default"
			} else if r, ok := games.RegionByID(rest[0]); ok {
				region = r.ID
			} else {
				var ids []string
				for _, r := range games.SiegeRegions {
					ids = append(ids, r.ID)
				}
				return fail(fmt.Errorf("неизвестный регион %q; есть: %s, default", rest[0], strings.Join(ids, ", ")))
			}
		} else {
			samples := measure()
			best, ok := games.Pick(samples, cfgs[0].Hint)
			if !ok {
				return fail(fmt.Errorf("ни один дата-центр не ответил"))
			}
			region = best.Region.ID
		}
		for _, c := range cfgs {
			if err := games.SetHint(c.Path, games.HintValue(region)); err != nil {
				return fail(err)
			}
		}
		ok(fmt.Sprintf("Siege закреплён за %s в %d профил(е/ях). Перезапустите игру.", games.HintValue(region), len(cfgs)))
		fmt.Println(ui.Dim("  вернуть как было: prosvet games restore"))
		return 0
	case "restore":
		n := 0
		for _, c := range games.FindSiege() {
			if c.Backup {
				if err := games.Restore(c.Path); err != nil {
					return fail(err)
				}
				n++
			}
		}
		if n == 0 {
			fmt.Println(ui.Dim("  Просвет ничего не менял в настройках Siege"))
			return 0
		}
		ok("настройки Siege возвращены")
		return 0
	case "direct":
		if len(rest) == 0 || (rest[0] != "on" && rest[0] != "off" && rest[0] != "auto") {
			return fail(fmt.Errorf("prosvet games direct on|off"))
		}
		v := rest[0]
		if v == "on" {
			v = "auto"
		}
		return simple(func(ctx context.Context, a api) error { return a.Settings(ctx, core.Settings{GameDirect: &v}) },
			map[string]string{"auto": "игры идут мимо VPN, когда он включён", "off": "игры идут тем же путём, что и всё остальное"}[v])
	}
	return fail(fmt.Errorf("prosvet games [ping | pin [регион|default] | restore | direct on|off]"))
}

func currentHint() string {
	if cfgs := games.FindSiege(); len(cfgs) > 0 {
		return cfgs[0].Hint
	}
	return ""
}

func measure() []games.Sample {
	route := netroute.Detect()
	via := games.DirectVia(route)
	how := "7 замеров на регион"
	if via != (games.Via{}) {
		how += ", напрямую через " + route.Phys + " — мимо VPN"
	}
	fmt.Println(ui.Bold(" Пинг до дата-центров Rainbow Six Siege") + ui.Dim("  (PlayFab · Azure, "+how+")"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := 0
	ss := games.Measure(ctx, games.SiegeRegions, 7, via, func(games.Sample) {
		done++
		fmt.Printf("%s%s", ui.ClearLine(), ui.Dim(fmt.Sprintf(" замеряю… %d/%d", done, len(games.SiegeRegions))))
	})
	fmt.Print(ui.ClearLine())
	printSamples(ss, true)
	routed := games.Measure(ctx, games.UDPBeacons, 5, games.Via{}, nil)
	if via == (games.Via{}) {
		fmt.Println(ui.Bold("\n UDP через обход") + ui.Dim("  (эхо-маяки AWS GameLift)"))
		printSamples(routed, false)
		return ss
	}
	direct := games.Measure(ctx, games.UDPBeacons, 5, via, nil)
	fmt.Println(ui.Bold("\n Игровой UDP: через VPN и напрямую") + ui.Dim("  (эхо-маяки AWS GameLift)"))
	byID := map[string]games.Sample{}
	for _, s := range direct {
		byID[s.Region.ID] = s
	}
	for _, s := range routed {
		d := byID[s.Region.ID]
		line := " " + ui.Pad(s.Region.Title, 20) + ui.Dim(" через "+route.App+" ") + ui.Pad(sampleText(s), 9) + ui.Dim("→ напрямую ") + ui.Pad(sampleText(d), 9)
		if s.OK() && d.OK() && s.RTT > d.RTT {
			line += ui.Accent(fmt.Sprintf(" −%d мс", (s.RTT - d.RTT).Milliseconds()))
		}
		fmt.Println(line)
	}
	return ss
}

func sampleText(s games.Sample) string {
	if !s.OK() {
		if s.Err != "" {
			return ui.Red(s.Err)
		}
		return ui.Red("нет ответа")
	}
	return games.Ms(s.RTT)
}

func printSamples(ss []games.Sample, siege bool) {
	hi := int64(60)
	for _, s := range ss {
		if s.OK() {
			hi = max(hi, s.RTT.Milliseconds())
		}
	}
	best, _ := games.Pick(ss, "")
	for _, s := range ss {
		mark := "  "
		if s.Region.ID == best.Region.ID && s.OK() {
			mark = ui.Accent("★ ")
		}
		name := ui.Pad(ui.Trunc(s.Region.Title, 32), 32)
		if !s.OK() {
			fmt.Printf(" %s%s %s\n", mark, ui.Dim(name), ui.Red(s.Err))
			continue
		}
		v := s.RTT.Milliseconds()
		n := int(v * 28 / hi)
		bar := strings.Repeat("█", max(1, n)) + ui.Dim(strings.Repeat("·", max(0, 28-max(1, n))))
		col := ui.Accent
		switch {
		case v > 150:
			col = ui.Red
		case v > 80:
			col = ui.Amber
		}
		extra := ui.Dim(fmt.Sprintf(" ±%d", s.Jitter.Milliseconds()))
		if s.Loss() > 0 {
			extra += ui.Amber(fmt.Sprintf(" потери %.0f%%", s.Loss()*100))
		}
		id := ""
		if siege {
			id = ui.Dim(" " + s.Region.ID)
		}
		fmt.Printf(" %s%s %s %s%s%s\n", mark, name, col(bar), col(ui.Pad(games.Ms(s.RTT), 7)), extra, id)
	}
	if siege && best.OK() && best.RTT < 2*time.Millisecond {
		fmt.Println(ui.Amber(" ⚠ задержка ~0 мс: соединения перехватывает локальный прокси/VPN — цифры не отражают путь до серверов"))
	}
}

func printRoute() {
	r := netroute.Detect()
	fmt.Println(ui.Bold("\n Путь пакетов"))
	switch {
	case r.Iface == "":
		fmt.Println(ui.Dim("  маршрут не определён"))
	case !r.VPN:
		fmt.Println(ui.Accent("  ● ") + "напрямую через " + r.Iface + ui.Dim(" — VPN не удлиняет путь"))
	case runtime.GOOS == "linux" && r.CanBypass():
		fmt.Println(ui.Amber("  ◐ ") + "маршрут по умолчанию — VPN " + r.App + " (" + r.Iface + ")")
		fmt.Println(ui.Dim("    Пока работает обход с целями r6/forhonor, игровой UDP идёт мимо VPN через ") + r.Phys + ui.Dim(" (шлюз "+r.Gateway.String()+")."))
		fmt.Println(ui.Dim("    Отключить: prosvet games direct off"))
	default:
		fmt.Println(ui.Amber("  ◐ ") + "игры идут через VPN " + r.App + " (" + r.Iface + ") — каждый пакет делает крюк")
		fmt.Println(ui.Dim("    Добавьте в исключения VPN-клиента (прямое подключение): ") + strings.Join(netroute.GameExes, ", "))
	}
}

func printSiegeConfigs() {
	cfgs := games.FindSiege()
	fmt.Println(ui.Bold("\n Настройки Siege"))
	if len(cfgs) == 0 {
		fmt.Println(ui.Dim("  GameSettings.ini не найден (Windows Documents, Steam/Proton, Heroic, Lutris, Bottles)"))
		return
	}
	for _, c := range cfgs {
		h := c.Hint
		if h == "" {
			h = "не задан"
		}
		b := ""
		if c.Backup {
			b = ui.Dim(" · есть резервная копия")
		}
		fmt.Println("  DataCenterHint=" + ui.Bold(h) + b + ui.Dim("\n  "+c.Path))
	}
}

// cmdTelegram shows the proxy link from the service or, without one, runs
// the proxy in this window — it needs no administrator rights.
func cmdTelegram(rest []string) int {
	if a, _, err := connect(); err == nil && len(rest) == 0 {
		ctx, cancel := ctxT(10 * time.Second)
		defer cancel()
		if st, err := a.Status(ctx); err == nil && localCtl == nil {
			if st.Telegram != nil && st.Telegram.Running {
				printTelegram(st.Telegram.Link, st.Telegram.Listen)
				return 0
			}
			fmt.Println(ui.Dim("  прокси выключен — включите: ") + ui.Bold("prosvet enable telegram"))
			return 0
		}
	}
	listen := "127.0.0.1:1443"
	if len(rest) > 1 && rest[0] == "--listen" {
		listen = rest[1]
	}
	p := tgproxy.New(tgproxy.Config{Listen: listen, Secret: tui.LocalTelegramSecret(), Direct: tgproxy.AroundVPN})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- p.Serve(ctx) }()
	select {
	case err := <-errc:
		return fail(err)
	case <-time.After(300 * time.Millisecond):
	}
	printTelegram(p.Link(), listen)
	fmt.Println(ui.Dim("\n  Прокси работает, пока открыто это окно. Ctrl+C — выход."))
	intr := make(chan os.Signal, 1)
	signal.Notify(intr, os.Interrupt)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-intr:
			fmt.Println()
			return 0
		case err := <-errc:
			return fail(err)
		case <-t.C:
			s := p.Snapshot()
			fmt.Printf("%s%s", ui.ClearLine(), ui.Dim(fmt.Sprintf("  соединений %d · всего %d · WebSocket %d · TCP %d · ↓%d КБ ↑%d КБ",
				s.Active, s.Total, s.WS, s.TCP, s.Down>>10, s.Up>>10)))
		}
	}
}

func printTelegram(link, listen string) {
	fmt.Println(ui.Accent("● ") + ui.Bold("Telegram-прокси") + ui.Dim("  "+listen))
	fmt.Println(ui.Dim("  Откройте ссылку — Telegram предложит подключить прокси:"))
	fmt.Println("  " + ui.Accent(link))
	fmt.Println(ui.Dim("  Вручную: Настройки → Продвинутые → Тип соединения → MTProto, 127.0.0.1, порт " + listen[strings.LastIndexByte(listen, ':')+1:]))
}
