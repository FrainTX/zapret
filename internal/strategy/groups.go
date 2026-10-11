package strategy

// Group is a slice of traffic that gets its own recipe. Groups are rendered
// as separate nfqws/winws profiles in this order, so specific hostname groups
// come before the broad address-space ones.
type Group struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  Kind   `json:"kind"`
	Proto string `json:"proto"` // tcp or udp
	Ports string `json:"ports"`
	L7    string `json:"l7,omitempty"`
	// Hostlists are include lists by name; "custom" is the user's own list.
	Hostlists []string `json:"hostlists,omitempty"`
	// IPSets are include IP sets by name ("cloud", "game").
	IPSets []string `json:"ipsets,omitempty"`
	// Extra arguments always added to this group's profile.
	Extra string `json:"extra,omitempty"`
	// Exclude adds the exclude hostlists/IP sets to the profile.
	Exclude bool `json:"exclude"`
	// Services that enable this group (any of them).
	Services []string `json:"services"`
	// Default candidate id used before tuning or when untestable.
	Default string `json:"default"`
	// Testable is false when no reliable probe exists for the group.
	Testable bool `json:"testable"`
	// Inherit derives the recipe from another group's choice (see Derive).
	Inherit string `json:"inherit,omitempty"`
	// Packets is how many packets per connection the Linux firewall hands to
	// nfqws. Kernel-side cut-off keeps CPU usage near zero.
	Packets int `json:"packets"`
	// PreferActive makes the tuner keep a working recipe even when the
	// baseline also passes. Used where probes cannot reach the real servers
	// (game UDP: PlayFab QoS endpoints need a game login), but the recipe is
	// a known fix and cheap (a couple of decoys per flow).
	PreferActive bool `json:"preferActive,omitempty"`
}

// Services visible to the user.
const (
	SvcYouTube  = "youtube"
	SvcDiscord  = "discord"
	SvcR6       = "r6"
	SvcForHonor = "forhonor"
	SvcCloud    = "cloud"
	SvcSites    = "sites"
	SvcTelegram = "telegram"
)

// Service is a user-facing target with its description.
type Service struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

// Services lists the targets in display order.
var Services = []Service{
	{ID: SvcYouTube, Title: "YouTube", Note: "сайт, видео, превью, QUIC"},
	{ID: SvcDiscord, Title: "Discord", Note: "клиент, сервера, голосовые каналы"},
	{ID: SvcR6, Title: "Rainbow Six Siege", Note: "Ubisoft Connect, поиск матча, серверы PlayFab/Azure"},
	{ID: SvcForHonor, Title: "For Honor", Note: "Ubisoft Connect, выделенные серверы"},
	{ID: SvcTelegram, Title: "Telegram", Note: "Telegram Desktop через WebSocket, web.telegram.org, t.me"},
	{ID: SvcCloud, Title: "Зарубежные хостинги", Note: "сайты на AWS, Azure, Cloudflare, Hetzner… — снимает «заморозку 16–20 КБ»"},
	{ID: SvcSites, Title: "Свои сайты", Note: "ваш список доменов, DoH-резолверы"},
}

// Groups is the full ordered group table.
var Groups = []Group{
	{ID: "discord-voice", Title: "Discord — голос", Kind: KindVoice, Proto: "udp",
		Ports: "19294-19344,50000-50100", L7: "discord,stun",
		Services: []string{SvcDiscord}, Default: "voice-fake6@steamcommunity.com", Packets: 12},
	{ID: "youtube-quic", Title: "YouTube — QUIC", Kind: KindQUIC, Proto: "udp", Ports: "443",
		Hostlists: []string{"youtube"}, Exclude: true,
		Services: []string{SvcYouTube}, Default: "quic-fake6@www.google.com", Testable: true, Packets: 6},
	{ID: "sites-quic", Title: "Свои сайты — QUIC", Kind: KindQUIC, Proto: "udp", Ports: "443",
		Hostlists: []string{"custom", "general"}, Exclude: true,
		Services: []string{SvcSites}, Default: "quic-fake6@www.google.com", Inherit: "youtube-quic", Packets: 6},
	{ID: "discord", Title: "Discord", Kind: KindTLS, Proto: "tcp", Ports: "80,443,2053,2083,2087,2096,8443",
		Hostlists: []string{"discord"}, Exclude: true,
		Services: []string{SvcDiscord}, Default: "msplit-seqovl@www.google.com", Testable: true, Packets: 6},
	{ID: "youtube", Title: "YouTube", Kind: KindTLS, Proto: "tcp", Ports: "80,443",
		Hostlists: []string{"youtube"}, Extra: "--ip-id=zero", Exclude: true,
		Services: []string{SvcYouTube}, Default: "msplit-seqovl@www.google.com", Testable: true, Packets: 6},
	{ID: "ubisoft", Title: "Ubisoft Connect", Kind: KindTLS, Proto: "tcp", Ports: "80,443",
		Hostlists: []string{"ubisoft"}, Exclude: true,
		Services: []string{SvcR6, SvcForHonor}, Default: None, Testable: true, Packets: 6},
	{ID: "telegram", Title: "Telegram — веб", Kind: KindTLS, Proto: "tcp", Ports: "80,443",
		Hostlists: []string{"telegram"}, Exclude: true,
		Services: []string{SvcTelegram}, Default: None, Testable: true, Packets: 6},
	{ID: "sites", Title: "Свои сайты", Kind: KindTLS, Proto: "tcp", Ports: "80,443",
		Hostlists: []string{"custom", "general"}, Exclude: true,
		Services: []string{SvcSites}, Default: "msplit-seqovl@www.google.com", Testable: true, Packets: 6},
	{ID: "cloud", Title: "Зарубежные хостинги", Kind: KindTLS, Proto: "tcp", Ports: "443",
		IPSets: []string{"cloud"}, Exclude: true,
		Services: []string{SvcCloud}, Default: "msplit-seqovl@max.ru", Testable: true, Packets: 6},
	{ID: "game-udp", Title: "Игры — UDP", Kind: KindGameUDP, Proto: "udp", Ports: "1024-65535",
		IPSets: []string{"game"}, Exclude: true,
		Services: []string{SvcR6, SvcForHonor}, Default: "game-fake12@4pda.to", Testable: true, Packets: 12, PreferActive: true},
	{ID: "game-tcp", Title: "Игры — TCP", Kind: KindGameTCP, Proto: "tcp", Ports: "1024-65535",
		IPSets: []string{"game"}, Exclude: true,
		Services: []string{SvcR6, SvcForHonor}, Default: "gtcp-msplit-seqovl@4pda.to", Inherit: "cloud", Packets: 6},
}

// GroupByID finds a group.
func GroupByID(id string) (Group, bool) {
	for _, g := range Groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// ActiveGroups returns the groups enabled by the given services, in order.
func ActiveGroups(services map[string]bool) []Group {
	var out []Group
	for _, g := range Groups {
		for _, s := range g.Services {
			if services[s] {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// Derive computes the recipe of an inheriting group from its parent's choice.
// For game TCP the parent is the "cloud" TLS group: if TLS to the same
// hosting providers needs no tricks, game TCP does not either; otherwise the
// matching any-protocol recipe is used with the parent's decoy hostname.
func Derive(g Group, parent Candidate) Candidate {
	if parent.ID == None {
		return NoneCandidate()
	}
	switch g.Kind {
	case KindGameTCP:
		t, _ := TemplateByID("gtcp-msplit-seqovl")
		if parent.Base == "fake-ts" || parent.Base == "fake-badseq" || parent.Base == "fake-fakedsplit-ts" {
			t, _ = TemplateByID("gtcp-fake-ts")
		}
		return t.Instantiate(parent.SNI)
	}
	return parent
}
