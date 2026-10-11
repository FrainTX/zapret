package strategy

import (
	"strconv"
	"strings"
)

// Cutoff returns N of an "--dpi-desync-cutoff=nN" option (the recipe only
// touches the first N-1 packets of a flow), or 0 if the recipe has no
// packet-number cutoff.
func Cutoff(args string) int {
	n := 0
	for _, f := range strings.Fields(args) {
		v, ok := strings.CutPrefix(f, "--dpi-desync-cutoff=n")
		if !ok {
			continue
		}
		if k, err := strconv.Atoi(v); err == nil && k > n {
			n = k
		}
	}
	return n
}

// QueuePackets is how many packets per flow the Linux firewall must hand to
// the engine for a recipe: with a packet cutoff, nothing after it, so the
// rest of a game session never leaves the kernel.
func QueuePackets(args string, def int) int {
	if n := Cutoff(args); n > 0 && n+1 < def {
		return n + 1
	}
	return def
}

// Cost estimates the extra packets a recipe puts on the wire per flow:
// decoys times repeats plus split segments. Among recipes that work equally
// well, the cheaper one adds less delay and burst to the first packets.
func Cost(args string) int {
	fakes, repeats, splits := 0, 1, 0
	for _, f := range strings.Fields(args) {
		name, val, _ := strings.Cut(f, "=")
		switch name {
		case "--dpi-desync":
			for _, m := range strings.Split(val, ",") {
				switch m {
				case "fake", "fakeknown", "syndata", "rst", "rstack":
					fakes++
				case "fakedsplit", "fakeddisorder", "hostfakesplit":
					fakes += 2
					splits++
				case "multisplit", "multidisorder", "ipfrag2", "udplen":
					splits++
				}
			}
		case "--dpi-desync-repeats":
			if k, err := strconv.Atoi(val); err == nil && k > 0 {
				repeats = k
			}
		case "--dpi-desync-split-pos":
			splits += strings.Count(val, ",")
		}
	}
	return fakes*repeats + splits
}
