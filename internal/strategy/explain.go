package strategy

import (
	"strings"
)

var modeNames = map[string]string{
	"fake":          "фейк",
	"fakeknown":     "фейк",
	"multisplit":    "разрез",
	"multidisorder": "разрез ⇄",
	"fakedsplit":    "разрез+фейки",
	"fakeddisorder": "разрез+фейки ⇄",
	"hostfakesplit": "подмена хоста",
	"syndata":       "данные в SYN",
	"udplen":        "длина UDP",
	"ipfrag2":       "IP-фрагменты",
	"rst":           "RST",
	"rstack":        "RST,ACK",
}

var foolingNames = map[string]string{
	"ts":        "TS",
	"badseq":    "SEQ",
	"md5sig":    "MD5",
	"badsum":    "CRC",
	"datanoack": "ACK",
}

// Tags returns short labels describing a recipe, for chips in the UI.
func Tags(args string) []string {
	if strings.TrimSpace(args) == "" {
		return []string{"без обхода"}
	}
	var tags []string
	repeats := ""
	sni := ""
	for _, f := range strings.Fields(args) {
		name, val, _ := strings.Cut(f, "=")
		switch name {
		case "--dpi-desync":
			var ms []string
			for _, m := range strings.Split(val, ",") {
				if n, ok := modeNames[m]; ok {
					ms = append(ms, n)
				} else {
					ms = append(ms, m)
				}
			}
			tags = append(tags, strings.Join(ms, " + "))
		case "--dpi-desync-repeats":
			repeats = "×" + val
		case "--dpi-desync-fooling":
			for _, x := range strings.Split(val, ",") {
				if n, ok := foolingNames[x]; ok {
					tags = append(tags, n)
				}
			}
		case "--dpi-desync-autottl":
			tags = append(tags, "TTL")
		case "--dpi-desync-split-seqovl":
			tags = append(tags, "перекрытие")
		case "--dpi-desync-cutoff":
			tags = append(tags, "до "+strings.TrimPrefix(val, "n")+" пак.")
		}
		if sni == "" && strings.HasPrefix(val, "@") {
			if _, host, ok := strings.Cut(val, ":"); ok && !strings.HasSuffix(host, ".bin") {
				sni = host
			}
		}
		if sni == "" && strings.Contains(val, "host=") {
			h := val[strings.Index(val, "host=")+5:]
			if i := strings.IndexByte(h, ','); i >= 0 {
				h = h[:i]
			}
			sni = h
		}
		if sni == "" && strings.Contains(val, "sni=") {
			h := val[strings.Index(val, "sni=")+4:]
			if i := strings.IndexByte(h, ','); i >= 0 {
				h = h[:i]
			}
			sni = h
		}
	}
	if repeats != "" && len(tags) > 0 {
		tags[0] += " " + repeats
	}
	if sni != "" {
		tags = append(tags, sni)
	}
	return tags
}
