package strategy

import (
	"errors"
	"fmt"
	"strings"

	"prosvet/internal/fakes"
)

// fileOptions take a decoy file (or hex blob) as value. The seqovl size is
// listed too because a reference there means "the length of that decoy".
var fileOptions = map[string]bool{
	"--dpi-desync-fake-http":            true,
	"--dpi-desync-fake-tls":             true,
	"--dpi-desync-fake-unknown":         true,
	"--dpi-desync-fake-syndata":         true,
	"--dpi-desync-fake-quic":            true,
	"--dpi-desync-fake-wireguard":       true,
	"--dpi-desync-fake-dht":             true,
	"--dpi-desync-fake-discord":         true,
	"--dpi-desync-fake-stun":            true,
	"--dpi-desync-fake-unknown-udp":     true,
	"--dpi-desync-udplen-pattern":       true,
	"--dpi-desync-split-seqovl-pattern": true,
	"--dpi-desync-fakedsplit-pattern":   true,
	"--dpi-desync-split-seqovl":         true,
}

// allowedPrefixes are the only option families a stored recipe may use.
// Everything that touches files, privileges, filters or process management
// (--debug, --pidfile, --user, --hostlist*, --ipset*, --filter-*, --wf-*,
// --qnum, @config, --new, ...) is rejected: the group table owns filtering
// and the engine wrapper owns process options.
var allowedPrefixes = []string{
	"--dpi-desync", "--ip-id=", "--dup", "--orig-", "--hostcase", "--hostspell=",
	"--hostnospace", "--domcase", "--methodeol", "--wssize", "--synack-split",
}

var errEmpty = errors.New("strategy: empty value")

func validSNI(s string) bool { return fakes.ValidHostname(s) && strings.Contains(s, ".") }

// validRef checks a symbolic decoy reference.
func validRef(v string) bool {
	switch {
	case v == "@stun":
		return true
	case strings.HasPrefix(v, "@tls:"):
		return validSNI(v[5:])
	case strings.HasPrefix(v, "@quic:"):
		return validSNI(v[6:])
	case strings.HasPrefix(v, "@http:"):
		return validSNI(v[6:])
	case strings.HasPrefix(v, "@bin:"):
		name := v[5:]
		return name != "" && !strings.ContainsAny(name, `/\:`) && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".bin")
	}
	return false
}

func validHex(v string) bool {
	if !strings.HasPrefix(v, "0x") || len(v) < 4 || len(v)%2 != 0 {
		return false
	}
	for _, c := range v[2:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// safeScalar allows the characters zapret uses in non-file values:
// numbers, markers (midsld+1), lists, mod strings (host=ya.ru,altorder=1).
func safeScalar(v string) bool {
	if v == "" {
		return true
	}
	for _, c := range v {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("-+.,=:_", c)
		if !ok {
			return false
		}
	}
	return !strings.HasPrefix(v, "@") && !strings.HasPrefix(v, "$")
}

// Validate checks that a symbolic recipe only contains allowed options with
// safe values. It is the gatekeeper for anything coming over the local API.
func Validate(args string) error {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return nil
	}
	if len(fields) > 64 {
		return fmt.Errorf("strategy: too many arguments")
	}
	sawDesync := false
	for _, f := range fields {
		name, val, hasVal := strings.Cut(f, "=")
		allowed := false
		for _, p := range allowedPrefixes {
			if strings.HasPrefix(f, p) {
				allowed = true
				break
			}
		}
		if !allowed || !strings.HasPrefix(name, "--") {
			return fmt.Errorf("strategy: option %q is not allowed", name)
		}
		if name == "--dpi-desync" {
			sawDesync = true
		}
		if fileOptions[name] {
			if !hasVal || val == "" {
				return fmt.Errorf("strategy: %s: %w", name, errEmpty)
			}
			switch {
			case strings.HasPrefix(val, "@"):
				if !validRef(val) {
					return fmt.Errorf("strategy: %s: bad decoy reference %q", name, val)
				}
			case validHex(val), val == "!":
			case name == "--dpi-desync-split-seqovl" && safeScalar(val):
			default:
				return fmt.Errorf("strategy: %s: value %q must be a decoy reference or hex", name, val)
			}
			continue
		}
		if hasVal && !safeScalar(val) {
			return fmt.Errorf("strategy: %s: unsafe value %q", name, val)
		}
	}
	if !sawDesync {
		return fmt.Errorf("strategy: --dpi-desync is required")
	}
	return nil
}

// Refs lists the decoy references a recipe needs.
func Refs(args string) []string {
	var out []string
	for _, f := range strings.Fields(args) {
		if _, v, ok := strings.Cut(f, "="); ok && strings.HasPrefix(v, "@") {
			out = append(out, v)
		}
	}
	return out
}
