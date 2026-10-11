// Package ui renders the console interface: colours, the status board and
// the live tuning matrix.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// Color support is decided once at start-up.
var Color = detectColor()

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return enableVT()
}

func sgr(code, s string) string {
	if !Color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Palette, shared with the full-screen interface (package tui): cold ink
// and slate, one pale ice-blue beam for "light passes", soft yellow for
// "in part", rose for "blocked". No green, no orange.
func Accent(s string) string { return sgr("38;2;169;214;255", s) }
func AccentBG(s string) string {
	return sgr("48;2;169;214;255;38;2;15;18;32;1", s)
}
func Dim(s string) string    { return sgr("38;2;138;143;173", s) }
func Bold(s string) string   { return sgr("1", s) }
func Red(s string) string    { return sgr("38;2;255;111;156", s) }
func Amber(s string) string  { return sgr("38;2;242;229;140", s) }
func Ink(s string) string    { return sgr("38;2;231;233;244", s) }
func Violet(s string) string { return sgr("38;2;179;166;255", s) }

// Width returns the display width of s, ignoring escape sequences.
func Width(s string) int {
	n := 0
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc:
			if r == 'm' {
				esc = false
			}
		default:
			n++
		}
	}
	return n
}

// Pad pads s to w display columns.
func Pad(s string, w int) string {
	if d := w - Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// Trunc shortens plain text to w runes.
func Trunc(s string, w int) string {
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	r := []rune(s)
	return string(r[:w-1]) + "…"
}

// Rule prints a thin horizontal line with an optional label.
func Rule(w io.Writer, label string, width int) {
	if label == "" {
		fmt.Fprintln(w, Dim(strings.Repeat("─", width)))
		return
	}
	l := " " + label + " "
	fmt.Fprintln(w, Dim("──")+Bold(l)+Dim(strings.Repeat("─", max(0, width-2-Width(l)))))
}

// ClearLine erases the current terminal line (for live updates).
func ClearLine() string {
	if !Color {
		return "\r"
	}
	return "\r\x1b[2K"
}

// Up moves the cursor up n lines.
func Up(n int) string {
	if !Color || n <= 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dA", n)
}
