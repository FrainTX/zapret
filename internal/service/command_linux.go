package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// Command is the short name that opens the panel from any directory.
const Command = "zapret"

// Paths, variables for tests.
var (
	commandRecord = "/etc/prosvet/command.json"
	commandBinDir = "/usr/local/bin"
)

// CommandLink describes where the short command lives and what it
// displaced, so Uninstall can put the previous one back.
type CommandLink struct {
	Link string `json:"link"`
	// Old is where a foreign "zapret" that was there before now lives.
	Old string `json:"old,omitempty"`
}

// ErrCommandTaken means another program already answers to "zapret".
type ErrCommandTaken struct {
	Path, Target string
}

func (e *ErrCommandTaken) Error() string {
	return fmt.Sprintf("команда %s уже занята: %s → %s", Command, e.Path, e.Target)
}

// commandDir is a directory on everyone's PATH we can write to: normally
// /usr/local/bin; on read-only systems the owner's ~/.local/bin.
func commandDir(ownerUID int) string {
	if dir := commandBinDir; writable(dir) {
		return dir
	}
	if ownerUID > 0 {
		if u, err := user.LookupId(strconv.Itoa(ownerUID)); err == nil {
			d := filepath.Join(u.HomeDir, ".local", "bin")
			if os.MkdirAll(d, 0o755) == nil {
				_ = os.Chown(d, ownerUID, -1)
				return d
			}
		}
	}
	return ""
}

func writable(dir string) bool {
	probe := filepath.Join(dir, ".prosvet-probe")
	f, err := os.Create(probe)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

// LinkCommand makes "zapret" start Просвет. If another program already
// owns the name, it is renamed to "zapret-old" only when replace is set;
// otherwise *ErrCommandTaken is returned so the caller can ask first.
func LinkCommand(bin string, ownerUID int, replace bool) (CommandLink, error) {
	dir := commandDir(ownerUID)
	if dir == "" {
		return CommandLink{}, errors.New("нет каталога из PATH, доступного для записи")
	}
	link := filepath.Join(dir, Command)
	rec := CommandLink{Link: link}
	if prev, err := readCommandRecord(); err == nil && prev.Link == link {
		rec.Old = prev.Old // a reinstall keeps the memory of what we displaced
	}
	if _, err := os.Lstat(link); err == nil {
		target, _ := filepath.EvalSymlinks(link)
		if ours(target, bin) {
			return rec, writeCommandRecord(rec)
		}
		if !replace {
			shown := target
			if shown == "" {
				shown = link
			}
			return rec, &ErrCommandTaken{Path: link, Target: shown}
		}
		old := filepath.Join(dir, Command+"-old")
		for i := 2; exists(old); i++ {
			old = filepath.Join(dir, Command+"-old"+strconv.Itoa(i))
		}
		if err := os.Rename(link, old); err != nil {
			return rec, err
		}
		rec.Old = old
	}
	if err := os.Symlink(bin, link); err != nil {
		return rec, err
	}
	if ownerUID > 0 && dir != commandBinDir {
		_ = os.Lchown(link, ownerUID, -1)
	}
	return rec, writeCommandRecord(rec)
}

// UnlinkCommand removes our "zapret" and restores whatever it displaced.
func UnlinkCommand(bin string) {
	rec, err := readCommandRecord()
	if err != nil {
		return
	}
	if target, err := filepath.EvalSymlinks(rec.Link); err == nil && ours(target, bin) || err != nil {
		os.Remove(rec.Link)
	}
	if rec.Old != "" && exists(rec.Old) && !exists(rec.Link) {
		_ = os.Rename(rec.Old, rec.Link)
	}
	os.Remove(commandRecord)
}

func ours(target, bin string) bool {
	if target == "" {
		return false
	}
	a, err1 := os.Stat(target)
	b, err2 := os.Stat(bin)
	if err1 == nil && err2 == nil && os.SameFile(a, b) {
		return true
	}
	// A dangling link to a former install location is ours too.
	return filepath.Base(target) == "prosvet" && (filepath.Dir(target) == "/usr/local/bin" || filepath.Dir(target) == "/var/lib/prosvet/bin")
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil || !errors.Is(err, syscall.ENOENT)
}

func readCommandRecord() (CommandLink, error) {
	var r CommandLink
	b, err := os.ReadFile(commandRecord)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

func writeCommandRecord(r CommandLink) error {
	b, _ := json.Marshal(r)
	return os.WriteFile(commandRecord, b, 0o644)
}
