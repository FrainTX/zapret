// Package strategy describes traffic groups, the catalogue of DPI-desync
// recipes that can be applied to each of them, and how a selection of recipes
// is composed into one nfqws/winws command line.
//
// Recipes are stored symbolically: decoy files are referenced as @tls:<sni>,
// @quic:<sni>, @http:<host>, @stun or @bin:<bundled file>. They are generated
// or resolved only when the plan is rendered, which keeps stored strategies
// portable between Linux and Windows and makes them safe to accept over the
// local API (no arbitrary paths ever reach the privileged engine).
package strategy

import (
	"strings"
)

// Kind is the family of traffic a recipe is meant for.
type Kind string

const (
	KindTLS     Kind = "tls"      // TCP 80/443, matched by hostname
	KindQUIC    Kind = "quic"     // UDP 443, matched by hostname
	KindVoice   Kind = "voice"    // Discord voice / STUN
	KindGameUDP Kind = "game-udp" // UDP to game/cloud address space
	KindGameTCP Kind = "game-tcp" // TCP to game/cloud address space
)

// None is the candidate that leaves traffic untouched. It is always tested
// first: if a service works without tricks, prosvet does not touch it.
const None = "none"

// Template is a recipe with a $SNI placeholder.
type Template struct {
	ID     string
	Label  string // short Russian description for the UI
	Kind   Kind
	Args   string
	SNI    string // default decoy hostname
	Origin string // where the idea comes from
}

// Candidate is a concrete recipe ready to be tested or stored.
type Candidate struct {
	ID    string `json:"id"`
	Base  string `json:"base"`
	Label string `json:"label"`
	SNI   string `json:"sni,omitempty"`
	Args  string `json:"args"`
}

// TLS decoy hostnames worth trying. Most are on the TSPU "TCP 16-20"
// whitelist (see hyperion-cs/dpi-checkers) or are big domestic services that
// no Russian DPI will ever block.
var (
	TLSSNIPool  = []string{"www.google.com", "max.ru", "vk.com", "4pda.to", "ya.ru", "gosuslugi.ru", "steamcommunity.com", "www.microsoft.com"}
	QUICSNIPool = []string{"www.google.com", "4pda.to", "max.ru", "vk.com", "steamcommunity.com", "rutube.ru"}
	GameSNIPool = []string{"4pda.to", "max.ru", "steamcommunity.com", "vk.com", "www.google.com"}
)

const fakeHTTP = " --dpi-desync-fake-http=@http:$SNI"

// Templates is the recipe catalogue. Order matters: earlier entries are tried
// first and win ties, so the list goes from the least to the most invasive.
var Templates = []Template{
	// ---- TLS / HTTP over TCP -------------------------------------------
	{ID: "msplit-seqovl", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal general",
		Label: "разрез на 1-м байте + перекрытие фейковым ClientHello",
		Args:  "--dpi-desync=multisplit --dpi-desync-split-seqovl=@tls:$SNI --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=@tls:$SNI"},
	{ID: "msplit-seqovl-p2", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal ALT2",
		Label: "разрез на 2-м байте + перекрытие фейком",
		Args:  "--dpi-desync=multisplit --dpi-desync-split-seqovl=@tls:$SNI --dpi-desync-split-pos=2 --dpi-desync-split-seqovl-pattern=@tls:$SNI"},
	{ID: "msplit-sniext", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal ALT7",
		Label: "разрезы у SNI + перекрытие 679 байт",
		Args:  "--dpi-desync=multisplit --dpi-desync-split-pos=2,sniext+1 --dpi-desync-split-seqovl=679 --dpi-desync-split-seqovl-pattern=@bin:tls_clienthello_www_google_com.bin"},
	{ID: "fake-ts", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal SIMPLE FAKE",
		Label: "фейковый ClientHello ×6, отсев по TCP timestamp",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fooling=ts --dpi-desync-fake-tls=@tls:$SNI" + fakeHTTP},
	{ID: "fake-fakedsplit-ts", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal ALT",
		Label: "фейк + разрез с фейковыми сегментами, отсев по timestamp",
		Args:  "--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts --dpi-desync-fakedsplit-pattern=0x00 --dpi-desync-fake-tls=@tls:$SNI" + fakeHTTP},
	{ID: "fake-msplit-seqovl-ts", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal ALT11",
		Label: "фейк ×8 + разрез с перекрытием, отсев по timestamp",
		Args:  "--dpi-desync=fake,multisplit --dpi-desync-split-seqovl=@tls:$SNI --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern=@tls:$SNI --dpi-desync-fake-tls=@tls:$SNI" + fakeHTTP},
	{ID: "hostfakesplit-ts", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal ALT9",
		Label: "подмена имени хоста фейковым сегментом",
		Args:  "--dpi-desync=hostfakesplit --dpi-desync-repeats=4 --dpi-desync-fooling=ts --dpi-desync-hostfakesplit-mod=host=$SNI"},
	{ID: "fake-hostfakesplit", Kind: KindTLS, SNI: "ya.ru", Origin: "Flowseal ALT3",
		Label: "фейк с чужим SNI + подмена хоста, обратный порядок",
		Args:  "--dpi-desync=fake,hostfakesplit --dpi-desync-fake-tls-mod=rnd,dupsid,sni=$SNI --dpi-desync-hostfakesplit-mod=host=$SNI,altorder=1 --dpi-desync-fooling=ts" + fakeHTTP},
	{ID: "fake-badseq", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal SIMPLE FAKE ALT",
		Label: "фейк ×6 с неверным SEQ",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=2 --dpi-desync-fake-tls=@tls:$SNI" + fakeHTTP},
	{ID: "fake-msplit-badseq", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal ALT4",
		Label: "фейк + разрез, неверный SEQ (+1000)",
		Args:  "--dpi-desync=fake,multisplit --dpi-desync-repeats=6 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=1000 --dpi-desync-fake-tls=@tls:$SNI" + fakeHTTP},
	{ID: "fake-mdisorder-auto", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal FAKE TLS AUTO",
		Label: "фейки ×11 + разрез в обратном порядке по середине домена",
		Args:  "--dpi-desync=fake,multidisorder --dpi-desync-split-pos=1,midsld --dpi-desync-repeats=11 --dpi-desync-fooling=badseq --dpi-desync-fake-tls=0x00000000 --dpi-desync-fake-tls=! --dpi-desync-fake-tls-mod=rnd,dupsid,sni=$SNI" + fakeHTTP},
	{ID: "fake-fakedsplit-auto", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal FAKE TLS AUTO ALT",
		Label: "фейк ×8 + фейковые сегменты, неверный SEQ",
		Args:  "--dpi-desync=fake,fakedsplit --dpi-desync-split-pos=1 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=2 --dpi-desync-repeats=8 --dpi-desync-fake-tls-mod=rnd,dupsid,sni=$SNI" + fakeHTTP},
	{ID: "fake-msplit-seqovl-badseq", Kind: KindTLS, SNI: "www.google.com", Origin: "Flowseal FAKE TLS AUTO ALT2",
		Label: "фейк + разрез с перекрытием, далёкий неверный SEQ",
		Args:  "--dpi-desync=fake,multisplit --dpi-desync-split-seqovl=@tls:$SNI --dpi-desync-split-pos=1 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=10000000 --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern=@tls:$SNI --dpi-desync-fake-tls-mod=rnd,dupsid,sni=$SNI" + fakeHTTP},
	{ID: "fake-hostfakesplit-alt", Kind: KindTLS, SNI: "max.ru", Origin: "Flowseal ALT13",
		Label: "фейк ×5 + подмена хоста, обратный порядок",
		Args:  "--dpi-desync=fake,hostfakesplit --dpi-desync-fooling=ts --dpi-desync-hostfakesplit-mod=host=$SNI,altorder=1 --dpi-desync-repeats=5 --dpi-desync-fake-tls=@tls:$SNI" + fakeHTTP},
	{ID: "fake-mdisorder-md5", Kind: KindTLS, SNI: "www.google.com", Origin: "blockcheck",
		Label: "фейк + обратный порядок сегментов, отсев по MD5-подписи",
		Args:  "--dpi-desync=fake,multidisorder --dpi-desync-split-pos=midsld --dpi-desync-fooling=md5sig --dpi-desync-fake-tls=@tls:$SNI" + fakeHTTP},
	{ID: "fake-autottl", Kind: KindTLS, SNI: "www.google.com", Origin: "blockcheck",
		Label: "фейк с TTL, рассчитанным до DPI",
		Args:  "--dpi-desync=fake,multisplit --dpi-desync-autottl=2 --dpi-desync-split-pos=1,midsld --dpi-desync-fake-tls=@tls:$SNI" + fakeHTTP},
	{ID: "mdisorder-midsld", Kind: KindTLS, Origin: "blockcheck",
		Label: "сегменты в обратном порядке, разрез по середине домена",
		Args:  "--dpi-desync=multidisorder --dpi-desync-split-pos=1,midsld"},
	{ID: "syndata-mdisorder", Kind: KindTLS, Origin: "Flowseal ALT5",
		Label: "данные в SYN + сегменты в обратном порядке",
		Args:  "--dpi-desync=syndata,multidisorder"},
	{ID: "syndata", Kind: KindTLS, Origin: "Flowseal ALT7",
		Label: "данные в SYN-пакете",
		Args:  "--dpi-desync=syndata"},

	// ---- QUIC ------------------------------------------------------------
	{ID: "quic-fake6", Kind: KindQUIC, SNI: "www.google.com", Origin: "Flowseal general",
		Label: "фейковый QUIC Initial ×6",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fake-quic=@quic:$SNI"},
	{ID: "quic-fake11", Kind: KindQUIC, SNI: "www.google.com", Origin: "Flowseal ALT11",
		Label: "фейковый QUIC Initial ×11",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=11 --dpi-desync-fake-quic=@quic:$SNI"},
	{ID: "quic-udplen", Kind: KindQUIC, SNI: "4pda.to", Origin: "blockcheck",
		Label: "фейк QUIC + изменение длины датаграммы",
		Args:  "--dpi-desync=fake,udplen --dpi-desync-udplen-increment=2 --dpi-desync-repeats=4 --dpi-desync-fake-quic=@quic:$SNI"},
	{ID: "quic-ipfrag2", Kind: KindQUIC, Origin: "blockcheck",
		Label: "IP-фрагментация QUIC Initial",
		Args:  "--dpi-desync=ipfrag2 --dpi-desync-ipfrag-pos-udp=8"},

	// ---- Discord voice ---------------------------------------------------
	{ID: "voice-fake6", Kind: KindVoice, SNI: "steamcommunity.com", Origin: "Flowseal general",
		Label: "фейковые пакеты перед голосовым потоком ×6",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fake-discord=@quic:$SNI --dpi-desync-fake-stun=@quic:$SNI"},
	{ID: "voice-stun", Kind: KindVoice, Origin: "Flowseal ALT12",
		Label: "фейковый STUN ×3",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=3 --dpi-desync-fake-discord=@stun --dpi-desync-fake-stun=@stun"},

	// ---- Game UDP ----------------------------------------------------------
	{ID: "game-fake12", Kind: KindGameUDP, SNI: "4pda.to", Origin: "Flowseal Game Filter",
		Label: "QUIC-фейк с белым SNI ×12 в начале потока",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=12 --dpi-desync-any-protocol=1 --dpi-desync-fake-unknown-udp=@quic:$SNI --dpi-desync-cutoff=n2"},
	{ID: "game-stun6", Kind: KindGameUDP, Origin: "гайд игроков R6 (Steam)",
		Label: "STUN-фейк ×6 в начале потока",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-any-protocol=1 --dpi-desync-fake-unknown-udp=@stun --dpi-desync-cutoff=n2"},
	{ID: "game-fake10-n4", Kind: KindGameUDP, SNI: "4pda.to", Origin: "Flowseal ALT11",
		Label: "QUIC-фейк ×10, держится 4 пакета",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=10 --dpi-desync-any-protocol=1 --dpi-desync-fake-unknown-udp=@quic:$SNI --dpi-desync-cutoff=n4"},
	{ID: "game-udplen", Kind: KindGameUDP, SNI: "4pda.to", Origin: "blockcheck",
		Label: "QUIC-фейк + изменение длины датаграммы",
		Args:  "--dpi-desync=fake,udplen --dpi-desync-repeats=6 --dpi-desync-any-protocol=1 --dpi-desync-udplen-increment=2 --dpi-desync-fake-unknown-udp=@quic:$SNI --dpi-desync-cutoff=n3"},

	// ---- Game TCP ----------------------------------------------------------
	{ID: "gtcp-msplit-seqovl", Kind: KindGameTCP, SNI: "4pda.to", Origin: "Flowseal Game Filter",
		Label: "разрез с перекрытием для любых TCP-протоколов",
		Args:  "--dpi-desync=multisplit --dpi-desync-any-protocol=1 --dpi-desync-cutoff=n3 --dpi-desync-split-seqovl=@tls:$SNI --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=@tls:$SNI"},
	{ID: "gtcp-fake-ts", Kind: KindGameTCP, SNI: "www.google.com", Origin: "Flowseal SIMPLE FAKE",
		Label: "фейк ×6 для любых TCP-протоколов",
		Args:  "--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-any-protocol=1 --dpi-desync-cutoff=n4 --dpi-desync-fooling=ts --dpi-desync-fake-tls=@tls:$SNI --dpi-desync-fake-unknown=@tls:$SNI"},
}

// TemplateByID finds a template.
func TemplateByID(id string) (Template, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// Instantiate builds a candidate from a template with a given decoy name.
func (t Template) Instantiate(sni string) Candidate {
	if sni == "" {
		sni = t.SNI
	}
	c := Candidate{Base: t.ID, Label: t.Label, Args: t.Args}
	if strings.Contains(t.Args, "$SNI") {
		c.SNI = sni
		c.Args = strings.ReplaceAll(t.Args, "$SNI", sni)
		c.ID = t.ID + "@" + sni
	} else {
		c.ID = t.ID
	}
	return c
}

// UsesSNI reports whether the template has a decoy hostname to vary.
func (t Template) UsesSNI() bool { return strings.Contains(t.Args, "$SNI") }

// NoneCandidate is the "do nothing" baseline.
func NoneCandidate() Candidate {
	return Candidate{ID: None, Base: None, Label: "без обхода"}
}

// Candidates returns the first-stage candidates for a kind: the baseline and
// every template with its default decoy.
func Candidates(k Kind) []Candidate {
	out := []Candidate{NoneCandidate()}
	for _, t := range Templates {
		if t.Kind == k {
			out = append(out, t.Instantiate(""))
		}
	}
	// Recipes bred on earlier runs go last: they won once, on some network.
	return append(out, bredFor(k)...)
}

// Variants returns the second-stage candidates: the given template with every
// decoy hostname from the pool for its kind.
func Variants(base string) []Candidate {
	t, ok := TemplateByID(base)
	if !ok || !t.UsesSNI() {
		return nil
	}
	pool := TLSSNIPool
	switch t.Kind {
	case KindQUIC, KindVoice:
		pool = QUICSNIPool
	case KindGameUDP:
		pool = GameSNIPool
	}
	var out []Candidate
	for _, s := range pool {
		if s != t.SNI {
			out = append(out, t.Instantiate(s))
		}
	}
	return out
}

// ParseCandidateID rebuilds a candidate from its stable id ("base@sni").
func ParseCandidateID(id string) (Candidate, bool) {
	if id == None || id == "" {
		return NoneCandidate(), true
	}
	if IsBred(id) {
		return lookupBred(id)
	}
	base, sni, _ := strings.Cut(id, "@")
	t, ok := TemplateByID(base)
	if !ok {
		return Candidate{}, false
	}
	if sni != "" && !validSNI(sni) {
		return Candidate{}, false
	}
	return t.Instantiate(sni), true
}
