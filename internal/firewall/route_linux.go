package firewall

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"prosvet/internal/platform"
)

const srcValidMark = "/proc/sys/net/ipv4/conf/all/src_valid_mark"

var (
	directMu    sync.Mutex
	directOn    bool
	prevSrcMark string
)

func ipCmd(args ...string) error {
	bin := platform.FindTool("ip")
	if bin == "" {
		return fmt.Errorf("не найдена утилита ip (iproute2)")
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %s", strings.Join(args, " "), firstLine(string(out)))
	}
	return nil
}

// applyDirectRoute installs "fwmark DirectMark -> table DirectTable ->
// default via the physical gateway". It is idempotent.
func applyDirectRoute(d *Direct) error {
	directMu.Lock()
	defer directMu.Unlock()
	if !d.Gateway.Is4() || d.Iface == "" || strings.ContainsAny(d.Iface, " /") {
		return fmt.Errorf("маршрут мимо VPN: нет физического шлюза")
	}
	t := strconv.Itoa(DirectTable)
	if err := ipCmd("-4", "route", "replace", "default", "via", d.Gateway.String(), "dev", d.Iface, "table", t); err != nil {
		return err
	}
	mark := fmt.Sprintf("0x%x/0x%x", DirectMark, DirectMark)
	// Remove a stale copy first so repeated applies never stack rules.
	for ipCmd("-4", "rule", "del", "fwmark", mark, "table", t, "priority", t) == nil {
	}
	if err := ipCmd("-4", "rule", "add", "fwmark", mark, "table", t, "priority", t); err != nil {
		return err
	}
	// Replies carry the mark back (see direct_in); the reverse-path filter
	// only honours it with src_valid_mark.
	if !directOn {
		if b, err := os.ReadFile(srcValidMark); err == nil {
			prevSrcMark = strings.TrimSpace(string(b))
		}
	}
	_ = os.WriteFile(srcValidMark, []byte("1"), 0o644)
	directOn = true
	return nil
}

func clearDirectRoute() {
	directMu.Lock()
	defer directMu.Unlock()
	t := strconv.Itoa(DirectTable)
	mark := fmt.Sprintf("0x%x/0x%x", DirectMark, DirectMark)
	for ipCmd("-4", "rule", "del", "fwmark", mark, "table", t, "priority", t) == nil {
	}
	_ = ipCmd("-4", "route", "flush", "table", t)
	if directOn && prevSrcMark != "" && prevSrcMark != "1" {
		_ = os.WriteFile(srcValidMark, []byte(prevSrcMark), 0o644)
	}
	directOn = false
}
