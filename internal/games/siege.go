package games

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SiegeAppID is Rainbow Six Siege on Steam (Proton prefixes are named by it).
const SiegeAppID = "359550"

const backupSuffix = ".prosvet-backup"

// HintValue turns a region id into a DataCenterHint value.
func HintValue(region string) string {
	if region == "" || region == "default" {
		return "default"
	}
	return "playfab/" + region
}

// SiegeConfig is one GameSettings.ini (one per Ubisoft account).
type SiegeConfig struct {
	Path   string `json:"path"`
	Hint   string `json:"hint"`   // current DataCenterHint
	Backup bool   `json:"backup"` // an original copy from before Просвет exists
}

// UserHome is the home of the person at the keyboard, also when running
// elevated through sudo, pkexec or doas.
func UserHome() string {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		for _, env := range []string{"SUDO_USER", "DOAS_USER"} {
			if n := os.Getenv(env); n != "" && n != "root" {
				if u, err := user.Lookup(n); err == nil {
					return u.HomeDir
				}
			}
		}
		if id := os.Getenv("PKEXEC_UID"); id != "" {
			if u, err := user.LookupId(id); err == nil {
				return u.HomeDir
			}
		}
	}
	h, _ := os.UserHomeDir()
	return h
}

// FindSiege returns every GameSettings.ini of Rainbow Six Siege it can find:
// the Windows Documents folder (also when redirected to OneDrive), and on
// Linux the Proton, Heroic, Lutris and Bottles Wine prefixes.
func FindSiege() []SiegeConfig {
	home := UserHome()
	var docs []string
	if runtime.GOOS == "windows" {
		docs = append(docs, filepath.Join(home, "Documents"))
		if p := os.Getenv("OneDrive"); p != "" {
			docs = append(docs, filepath.Join(p, "Documents"))
		}
		m, _ := filepath.Glob(filepath.Join(home, "OneDrive*", "Documents"))
		docs = append(docs, m...)
		// Localised folder names live in the registry; the profile root
		// also catches "Мои документы" redirected by older setups.
		m, _ = filepath.Glob(filepath.Join(home, "*", "My Games"))
		for _, p := range m {
			docs = append(docs, filepath.Dir(p))
		}
	} else {
		for _, root := range steamRoots(home) {
			docs = append(docs, filepath.Join(root, "steamapps", "compatdata", SiegeAppID, "pfx", "drive_c", "users", "steamuser", "Documents"))
		}
		for _, pat := range []string{
			"Games/Heroic/Prefixes/*/drive_c/users/*/Documents",
			"Games/Heroic/Prefixes/*/pfx/drive_c/users/*/Documents",
			"Games/*/drive_c/users/*/Documents", // Lutris
			".local/share/bottles/bottles/*/drive_c/users/*/Documents",
			".var/app/com.usebottles.bottles/data/bottles/bottles/*/drive_c/users/*/Documents",
			".wine/drive_c/users/*/Documents",
		} {
			m, _ := filepath.Glob(filepath.Join(home, pat))
			docs = append(docs, m...)
		}
	}
	seen := map[string]bool{}
	var out []SiegeConfig
	for _, d := range docs {
		m, _ := filepath.Glob(filepath.Join(d, "My Games", "Rainbow Six - Siege", "*", "GameSettings.ini"))
		for _, p := range m {
			key := p
			if r, err := filepath.EvalSymlinks(p); err == nil {
				key = r
			}
			if runtime.GOOS == "windows" {
				key = strings.ToLower(key)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			c := SiegeConfig{Path: p}
			if b, err := os.ReadFile(p); err == nil {
				c.Hint, _ = readHint(b)
			}
			_, err := os.Stat(p + backupSuffix)
			c.Backup = err == nil
			out = append(out, c)
		}
	}
	return out
}

var libPathRe = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// steamRoots lists Steam installations and their extra library folders.
func steamRoots(home string) []string {
	var roots []string
	for _, p := range []string{
		".steam/steam", ".local/share/Steam", ".steam/root",
		".var/app/com.valvesoftware.Steam/.local/share/Steam",
		"snap/steam/common/.local/share/Steam",
	} {
		if st, err := os.Stat(filepath.Join(home, p)); err == nil && st.IsDir() {
			roots = append(roots, filepath.Join(home, p))
		}
	}
	for _, r := range append([]string(nil), roots...) {
		b, err := os.ReadFile(filepath.Join(r, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, m := range libPathRe.FindAllSubmatch(b, -1) {
			p := strings.ReplaceAll(string(m[1]), `\\`, `\`)
			roots = append(roots, p)
		}
	}
	return roots
}

var hintRe = regexp.MustCompile(`(?i)^\s*DataCenterHint\s*=\s*(.*?)\s*$`)

func readHint(b []byte) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if m := hintRe.FindStringSubmatch(sc.Text()); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// SetHint writes DataCenterHint into a GameSettings.ini, keeping every
// other byte (line endings included). The first change saves the original
// next to it, so Restore can put things back exactly.
func SetHint(path, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("bad value")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path + backupSuffix); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path+backupSuffix, b, 0o644); err != nil {
			return fmt.Errorf("резервная копия: %w", err)
		}
	}
	return writeKeepOwner(path, setHint(b, value))
}

func setHint(b []byte, value string) []byte {
	eol := "\n"
	if bytes.Contains(b, []byte("\r\n")) {
		eol = "\r\n"
	}
	lines := strings.SplitAfter(string(b), "\n")
	section := ""
	onlineEnd := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = strings.ToUpper(t)
			continue
		}
		if hintRe.MatchString(strings.TrimRight(l, "\r\n")) {
			lines[i] = "DataCenterHint=" + value + eol
			return []byte(strings.Join(lines, ""))
		}
		if section == "[ONLINE]" && t != "" {
			onlineEnd = i
		}
	}
	add := "DataCenterHint=" + value + eol
	if onlineEnd >= 0 {
		lines = append(lines[:onlineEnd+1], append([]string{add}, lines[onlineEnd+1:]...)...)
		return []byte(strings.Join(lines, ""))
	}
	out := string(b)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += eol
	}
	return []byte(out + eol + "[ONLINE]" + eol + add)
}

// Restore puts the original GameSettings.ini back.
func Restore(path string) error {
	b, err := os.ReadFile(path + backupSuffix)
	if err != nil {
		return fmt.Errorf("нет резервной копии: %w", err)
	}
	if err := writeKeepOwner(path, b); err != nil {
		return err
	}
	return os.Remove(path + backupSuffix)
}

// writeKeepOwner replaces a file atomically and, when running as root on a
// file in a user's home, hands it back to its owner.
func writeKeepOwner(path string, b []byte) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp := path + ".prosvet-tmp"
	if err := os.WriteFile(tmp, b, st.Mode().Perm()); err != nil {
		return err
	}
	chownLike(tmp, st)
	return os.Rename(tmp, path)
}

// Short describes a hint for people: "playfab/westeurope" -> "westeurope".
func Short(hint string) string {
	if h, ok := strings.CutPrefix(hint, "playfab/"); ok {
		return h
	}
	if hint == "" {
		return "не задан"
	}
	return hint
}

// RegionByID finds a Siege region.
func RegionByID(id string) (Region, bool) {
	for _, r := range SiegeRegions {
		if r.ID == id {
			return r, true
		}
	}
	return Region{}, false
}

// Pick chooses the region to pin: the best scored one that answered, but
// it keeps the current hint when that is within 5 ms — needless switching
// only reshuffles matchmaking.
func Pick(ss []Sample, current string) (Sample, bool) {
	var best *Sample
	for i := range ss {
		if ss[i].OK() {
			best = &ss[i]
			break
		}
	}
	if best == nil {
		return Sample{}, false
	}
	cur := Short(current)
	for _, s := range ss {
		if s.Region.ID == cur && s.OK() && s.Score() <= best.Score()+5*time.Millisecond {
			return s, true
		}
	}
	return *best, true
}

// Ms formats a duration in whole milliseconds.
func Ms(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return strconv.Itoa(int((d+500*time.Microsecond)/time.Millisecond)) + " мс"
}
