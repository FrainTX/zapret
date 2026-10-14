package tui

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"prosvet/internal/core"
	"prosvet/internal/games"
	"prosvet/internal/netroute"
	"prosvet/internal/strategy"
	"prosvet/internal/tgproxy"
	"prosvet/internal/tuner"
)

// PROSVET_SNAPSHOT=dir go test -run Snapshot ./internal/tui writes every
// screen as ANSI text for visual review.
func TestSnapshot(t *testing.T) {
	dir := os.Getenv("PROSVET_SNAPSHOT")
	if dir == "" {
		t.Skip("set PROSVET_SNAPSHOT")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	now := time.Now()
	bred, _ := strategy.Register(strategy.Bred{Kind: strategy.KindTLS, Args: "--dpi-desync=fake,multisplit --dpi-desync-split-pos=1,sniext+1 --dpi-desync-fooling=datanoack --dpi-desync-repeats=3 --dpi-desync-fake-tls=@tls:vk.com"})
	gs := []core.GroupStatus{
		{ID: "youtube", Title: "YouTube", Pick: bred.ID, PickTags: strategy.Tags(bred.Args), Testable: true, Health: "ok", OK: 4, Total: 4, Latency: 182, Checked: now},
		{ID: "youtube-quic", Title: "YouTube — QUIC", Pick: "quic-fake6@www.google.com", PickTags: []string{"фейк ×6", "www.google.com"}, Testable: true, Health: "ok", OK: 2, Total: 2, Latency: 96, Checked: now},
		{ID: "discord", Title: "Discord", Pick: bred.ID, PickTags: strategy.Tags(bred.Args), Testable: true, Health: "ok", OK: 4, Total: 4, Latency: 141, Checked: now},
		{ID: "discord-voice", Title: "Discord — голос", Pick: "voice-fake6@steamcommunity.com", PickTags: []string{"фейк ×6", "steamcommunity.com"}, Health: "unknown"},
		{ID: "telegram", Title: "Telegram", Pick: strategy.None, Testable: true, Health: "ok", OK: 3, Total: 3, Latency: 77, Checked: now},
		{ID: "ubisoft", Title: "Ubisoft Connect", Pick: strategy.None, Testable: true, Health: "partial", OK: 3, Total: 4, Latency: 210, Checked: now},
		{ID: "game-udp", Title: "Игры — UDP", Pick: "game-stun6", PickTags: []string{"фейк ×6", "до 1 пак.", "STUN"}, Testable: true, Health: "ok", OK: 4, Total: 4, Latency: 47, Checked: now},
		{ID: "cloud", Title: "Хостинги (16–20 КБ)", Pick: "msplit-seqovl@max.ru", PickTags: []string{"разрез", "перекрытие", "max.ru"}, Testable: true, Health: "fail", OK: 5, Total: 12, Latency: 320, Checked: now},
	}
	var evs []tuner.Event
	cands := []string{"none", "msplit-seqovl@www.google.com", "fake-ts@www.google.com", "fake-badseq@www.google.com", "hostfakesplit-ts@www.google.com", bred.ID, "syn-00000001", bred.ID}
	for i, c := range cands {
		for _, g := range []string{"youtube", "discord", "cloud", "game-udp"} {
			ok := (i*7+len(g))%5 - 1
			if ok < 0 {
				ok = 0
			}
			if c == bred.ID {
				ok = 4
			}
			evs = append(evs, tuner.Event{Type: "trial", Stage: 1 + i/3, Group: g, Candidate: c, OK: min(ok, 4), Total: 4, Latency: 120 + i*9, Tags: strategy.Tags(bred.Args), Best: bred.ID})
		}
	}
	evs = append(evs, tuner.Event{Type: "generation", Round: 2, Rounds: 4}, tuner.Event{Type: "universal", Candidate: bred.ID})
	st := core.Status{
		Version: "0.9.0", Running: true, Enabled: true, Since: now.Add(-134 * time.Minute), Engine: "nfqws", EngineVersion: "v72.13", Firewall: "nftables",
		Groups: gs, Services: map[string]bool{"youtube": true, "discord": true, "telegram": true, "r6": true, "forhonor": true}, ServiceList: strategy.Services,
		LastTune: now.Add(-3 * time.Hour), Domains: []string{"rutracker.org", "x.com"}, Exclude: []string{"sberbank.ru"},
		Route:  netroute.Info{Iface: "happ-xray", VPN: true, App: "Happ", Phys: "eno1", Gateway: netip.MustParseAddr("192.168.1.1")},
		Direct: true,
		Telegram: &core.TelegramStatus{Running: true, Listen: "127.0.0.1:1443", Link: "tg://proxy?server=127.0.0.1&port=1443&secret=dd0123456789abcdef0123456789abcdef",
			Stats:  tgproxy.Stats{Active: 4, Total: 37, WS: 33, Fronted: 2, TCP: 2, Up: 3 << 20, Down: 148 << 20},
			Routes: map[string][]tgproxy.Route{"2": {{IP: "149.154.167.220", Domain: "kws2.web.telegram.org", RTT: 72 * time.Millisecond}}, "4": {{IP: "149.154.167.220", Domain: "kws4.web.telegram.org", RTT: 81 * time.Millisecond}, {IP: "149.154.167.220", Domain: "kws4-1.web.telegram.org", Front: true, RTT: 140 * time.Millisecond}}}},
		Tuning: &core.TuneProgress{Active: true, Deep: true, Stage: 3, Round: 31, Started: now.Add(-197 * time.Second), Message: "вывожу новые рецепты", Events: evs},
	}
	mk := func(id string, rtt, jit int, got int) games.Sample {
		r, ok := games.RegionByID(id)
		if !ok {
			for _, b := range games.UDPBeacons {
				if b.ID == id {
					r = b
				}
			}
		}
		return games.Sample{Region: r, RTT: time.Duration(rtt) * time.Millisecond, Jitter: time.Duration(jit) * time.Millisecond, Sent: 7, Got: got}
	}
	m := newModel(Options{})
	m.st, m.w, m.h, m.phase = &st, 132, 40, 0.37
	if os.Getenv("PROSVET_SNAPSHOT_IDLE") != "" {
		m.st.Tuning = nil // the steady state, for the README screenshot
	}
	if v := os.Getenv("PROSVET_SNAPSHOT_SIZE"); v != "" {
		fmt.Sscanf(v, "%dx%d", &m.w, &m.h)
	}
	m.hist = map[string][]int{"youtube": {190, 185, 200, 176, 182, 182, 179, 181}, "discord": {150, 144, 141, 160, 141}, "game-udp": {49, 47, 51, 47, 46, 47}, "cloud": {300, 340, 310, 320}}
	m.siege = []games.Sample{mk("westeurope", 52, 1, 7), mk("northeurope", 63, 1, 7), mk("eastus", 132, 1, 7), mk("uaenorth", 140, 2, 7), mk("southcentralus", 154, 2, 7), mk("centralus", 168, 5, 7), mk("westus", 192, 2, 7), mk("southafricanorth", 213, 1, 7), mk("brazilsouth", 234, 1, 7), mk("japaneast", 268, 2, 7), mk("australiaeast", 331, 3, 7), mk("eastasia", 0, 0, 0)}
	m.siege[len(m.siege)-1].Err = "таймаут"
	m.udp = []games.Sample{mk("eu-central-1", 274, 13, 5), mk("eu-north-1", 290, 20, 5), mk("eu-west-1", 286, 18, 5)}
	m.udpDirect = []games.Sample{mk("eu-central-1", 47, 2, 5), mk("eu-north-1", 29, 1, 5), mk("eu-west-1", 61, 2, 5)}
	m.configs = []games.SiegeConfig{{Path: "/home/user/.local/share/Steam/steamapps/compatdata/359550/pfx/drive_c/users/steamuser/Documents/My Games/Rainbow Six - Siege/4e1c…/GameSettings.ini", Hint: "playfab/westeurope", Backup: true}}
	m.toast, m.toastOK, m.toastAt = "Siege закреплён за Западная Европа · Амстердам (52 мс)", true, now
	os.MkdirAll(dir, 0o755)
	m.inMenu = true
	os.WriteFile(filepath.Join(dir, "Меню.ans"), []byte(m.View()), 0o644)
	m.inMenu = false
	for i := range tabNames {
		m.tab = i
		os.WriteFile(filepath.Join(dir, tabNames[i]+".ans"), []byte(m.View()), 0o644)
	}
	m.tab = 0
	m.w = 92
	os.WriteFile(filepath.Join(dir, "narrow.ans"), []byte(m.View()), 0o644)
	_ = tea.Quit
}
