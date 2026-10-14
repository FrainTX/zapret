// Package tui is the full-screen console interface of Просвет.
//
// The look follows the name — "light through a gap": greyscale throughout,
// white for what passes, grey for what passes in part, and one muted red
// for what is blocked. State is always carried by the glyph too (● ◐ ○).
package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette.
var (
	cInk     = lipgloss.Color("#E6E6E6") // primary text
	cMuted   = lipgloss.Color("#8E8E8E") // secondary text
	cFaint   = lipgloss.Color("#4A4A4A") // rules, borders, empty cells
	cDeep    = lipgloss.Color("#2A2A2A") // inactive tab, bar track
	cBeam    = lipgloss.Color("#FFFFFF") // the accent: light that passes
	cBeamHot = lipgloss.Color("#FFFFFF")
	cViolet  = lipgloss.Color("#C8C8C8") // synthesized / special (marked ✦ / ◆)
	cPartial = lipgloss.Color("#A6A6A6") // works in part (marked ◐)
	cBlock   = lipgloss.Color("#D9605A") // blocked / error — the only colour
	cOnBeam  = lipgloss.Color("#111111") // text on the accent
)

// Gradient stops of the beam, from deep indigo through ice to white.
var beam = []rgb{{0x3A, 0x3A, 0x3A}, {0x70, 0x70, 0x70}, {0xB4, 0xB4, 0xB4}, {0xFF, 0xFF, 0xFF}}

var (
	sInk     = lipgloss.NewStyle().Foreground(cInk)
	sMuted   = lipgloss.NewStyle().Foreground(cMuted)
	sFaint   = lipgloss.NewStyle().Foreground(cFaint)
	sBeam    = lipgloss.NewStyle().Foreground(cBeam)
	sBeamB   = lipgloss.NewStyle().Foreground(cBeam).Bold(true)
	sViolet  = lipgloss.NewStyle().Foreground(cViolet)
	sPartial = lipgloss.NewStyle().Foreground(cPartial)
	sBlock   = lipgloss.NewStyle().Foreground(cBlock)
	sBold    = lipgloss.NewStyle().Foreground(cInk).Bold(true)
	sKey     = lipgloss.NewStyle().Foreground(cOnBeam).Background(cBeam).Bold(true).Padding(0, 1)
	sKeyDim  = lipgloss.NewStyle().Foreground(cInk).Background(cDeep).Padding(0, 1)
	sTabOn   = lipgloss.NewStyle().Foreground(cOnBeam).Background(cBeam).Bold(true).Padding(0, 1)
	sTabOff  = lipgloss.NewStyle().Foreground(cMuted).Padding(0, 1)
	// sPanel is a plain section: no frame, a little air below. sBox is the
	// one framed element (dialogs), square like a console window.
	sPanel = lipgloss.NewStyle().Padding(0, 1).MarginBottom(1)
	sBox   = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(cMuted).Padding(1, 3)
	sTitle   = lipgloss.NewStyle().Foreground(cMuted).Bold(true)
)

type rgb struct{ r, g, b uint8 }

func (c rgb) hex() string { return fmt.Sprintf("#%02X%02X%02X", c.r, c.g, c.b) }

func lerp(a, b rgb, t float64) rgb {
	f := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return rgb{f(a.r, b.r), f(a.g, b.g), f(a.b, b.b)}
}

// beamAt samples the beam gradient at t in [0,1].
func beamAt(t float64) rgb {
	t = math.Max(0, math.Min(1, t))
	seg := t * float64(len(beam)-1)
	i := int(seg)
	if i >= len(beam)-1 {
		return beam[len(beam)-1]
	}
	return lerp(beam[i], beam[i+1], seg-float64(i))
}

// gradient paints text along the beam. phase shifts it for a slow shimmer.
func gradient(s string, phase float64, bold bool) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		t := float64(i) / math.Max(1, float64(len(rs)-1))
		t = 0.5 + 0.5*math.Sin((t+phase)*math.Pi)
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(beamAt(t).hex()))
		if bold {
			st = st.Bold(true)
		}
		b.WriteString(st.Render(string(r)))
	}
	return b.String()
}

// beamLine is a horizontal ray: bright in the middle, fading to nothing at
// the edges. pos moves the hot spot (0..1).
func beamLine(w int, pos float64) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < w; i++ {
		x := float64(i) / float64(max(1, w-1))
		d := math.Abs(x - pos)
		glow := math.Exp(-d * d * 18)
		base := 0.18 + 0.82*glow
		c := lerp(rgb{0x22, 0x22, 0x22}, beamAt(glow), base)
		ch := "─"
		if glow > 0.85 {
			ch = "━"
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.hex())).Render(ch))
	}
	return b.String()
}

// Health glyphs.
func dot(health string) string {
	switch health {
	case "ok":
		return sBeam.Render("●")
	case "partial":
		return sPartial.Render("◐")
	case "down", "blocked":
		return sBlock.Render("○")
	case "off":
		return sFaint.Render("·")
	}
	return sFaint.Render("◌")
}

// ratioStyle colours a share of successful probes.
func ratioStyle(ok, total int) lipgloss.Style {
	switch {
	case total == 0:
		return sFaint
	case ok == total:
		return sBeam
	case ok == 0:
		return sBlock
	}
	return sPartial
}

// cell is one tested recipe in the tuning matrix.
func cell(ok, total int, bred bool) string {
	ch := "■"
	if bred {
		ch = "◆"
	}
	return ratioStyle(ok, total).Render(ch)
}

// bar draws a horizontal meter of v/maxv in w cells with eighth-blocks.
func bar(v, maxv float64, w int, st lipgloss.Style) string {
	if maxv <= 0 || w <= 0 {
		return sFaint.Render(strings.Repeat("·", max(0, w)))
	}
	f := math.Max(0, math.Min(1, v/maxv)) * float64(w)
	full := int(f)
	parts := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	s := strings.Repeat("█", full)
	if rem := int((f - float64(full)) * 8); full < w && rem > 0 {
		s += parts[rem]
		full++
	}
	return st.Render(s) + sFaint.Render(strings.Repeat("·", max(0, w-full)))
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// spark renders a latency history; lower is better, so it is drawn as is
// (tall = slow) in muted ink with the latest value in the beam colour.
func spark(xs []int, w int) string {
	if len(xs) > w {
		xs = xs[len(xs)-w:]
	}
	hi := 1
	for _, x := range xs {
		hi = max(hi, x)
	}
	var b strings.Builder
	for i, x := range xs {
		r := sparkRunes[0]
		if x > 0 {
			r = sparkRunes[min(len(sparkRunes)-1, x*(len(sparkRunes)-1)/hi)]
		}
		st := sMuted
		if x == 0 {
			st = sFaint
		}
		if i == len(xs)-1 {
			st = sBeam
		}
		b.WriteString(st.Render(string(r)))
	}
	return sFaint.Render(strings.Repeat(" ", max(0, w-len(xs)))) + b.String()
}

func ms(v int) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d мс", v)
}

// keyHint renders "[key] label".
func keyHint(k, label string) string { return sKeyDim.Render(k) + " " + sMuted.Render(label) }

// truncate cuts plain text to w display cells.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	rs := []rune(s)
	for len(rs) > 0 && lipgloss.Width(string(rs))+1 > w {
		rs = rs[:len(rs)-1]
	}
	return string(rs) + "…"
}

// pad pads styled text to w cells.
func pad(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
