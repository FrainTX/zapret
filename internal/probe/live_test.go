package probe

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// TestLive probes the real network: PROSVET_LIVE=1 go test -run Live -v ./internal/probe
func TestLive(t *testing.T) {
	if os.Getenv("PROSVET_LIVE") == "" {
		t.Skip("set PROSVET_LIVE=1")
	}
	var all []Target
	for _, g := range []string{"youtube", "youtube-quic", "discord", "ubisoft", "game-udp", "cloud", "sites"} {
		all = append(all, Targets(g, []string{"rutracker.org", "instagram.com"})...)
	}
	for _, r := range RunAll(context.Background(), all, Options{}, 16) {
		fmt.Printf("%-13s %-36s %-9s %5dms %7dB  %s\n", r.Group, r.Host, r.Class, r.Latency.Milliseconds(), r.Bytes, r.Detail)
	}
}
