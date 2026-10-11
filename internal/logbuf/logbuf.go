// Package logbuf is a small in-memory log with live subscribers, used for the
// engine output and the daemon journal shown in the UI.
package logbuf

import (
	"fmt"
	"sync"
	"time"
)

// Entry is one log line.
type Entry struct {
	Time   time.Time `json:"time"`
	Level  string    `json:"level"` // info, warn, error, engine
	Source string    `json:"source"`
	Text   string    `json:"text"`
}

// Buffer keeps the last N entries and fans them out to subscribers.
type Buffer struct {
	mu   sync.Mutex
	max  int
	ring []Entry
	subs map[chan Entry]struct{}
	sink func(Entry)
}

// New returns a buffer holding up to max entries.
func New(max int) *Buffer {
	return &Buffer{max: max, subs: map[chan Entry]struct{}{}}
}

// SetSink installs a function that receives every entry (e.g. a file writer).
func (b *Buffer) SetSink(f func(Entry)) {
	b.mu.Lock()
	b.sink = f
	b.mu.Unlock()
}

// Add appends an entry.
func (b *Buffer) Add(level, source, text string) {
	e := Entry{Time: time.Now(), Level: level, Source: source, Text: text}
	b.mu.Lock()
	b.ring = append(b.ring, e)
	if len(b.ring) > b.max {
		b.ring = append(b.ring[:0:0], b.ring[len(b.ring)-b.max:]...)
	}
	sink := b.sink
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // slow subscriber: drop rather than block the engine
		}
	}
	b.mu.Unlock()
	if sink != nil {
		sink(e)
	}
}

// Infof logs at info level.
func (b *Buffer) Infof(source, format string, a ...any) {
	b.Add("info", source, fmt.Sprintf(format, a...))
}

// Warnf logs at warn level.
func (b *Buffer) Warnf(source, format string, a ...any) {
	b.Add("warn", source, fmt.Sprintf(format, a...))
}

// Errorf logs at error level.
func (b *Buffer) Errorf(source, format string, a ...any) {
	b.Add("error", source, fmt.Sprintf(format, a...))
}

// Snapshot returns a copy of the stored entries.
func (b *Buffer) Snapshot() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Entry(nil), b.ring...)
}

// Subscribe returns a channel receiving new entries and a cancel function.
func (b *Buffer) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Writer adapts the buffer to io.Writer, splitting on newlines.
type Writer struct {
	B      *Buffer
	Level  string
	Source string
	mu     sync.Mutex
	part   []byte
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.part = append(w.part, p...)
	for {
		i := -1
		for j, c := range w.part {
			if c == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			break
		}
		line := string(w.part[:i])
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		if line != "" {
			w.B.Add(w.Level, w.Source, line)
		}
		w.part = w.part[i+1:]
	}
	if len(w.part) > 4096 {
		w.B.Add(w.Level, w.Source, string(w.part))
		w.part = nil
	}
	return len(p), nil
}
