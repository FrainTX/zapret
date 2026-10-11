package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLinkCommand(t *testing.T) {
	tmp := t.TempDir()
	commandBinDir = filepath.Join(tmp, "bin")
	commandRecord = filepath.Join(tmp, "command.json")
	os.MkdirAll(commandBinDir, 0o755)
	// The user's older zapret-discord-youtube owns the name.
	oldTarget := filepath.Join(tmp, "opt", "zapret")
	os.MkdirAll(filepath.Dir(oldTarget), 0o755)
	os.WriteFile(oldTarget, []byte("#!/bin/sh\necho old\n"), 0o755)
	link := filepath.Join(commandBinDir, "zapret")
	os.Symlink(oldTarget, link)
	bin := filepath.Join(commandBinDir, "prosvet")
	os.WriteFile(bin, []byte("prosvet"), 0o755)

	_, err := LinkCommand(bin, 0, false)
	var taken *ErrCommandTaken
	if !errors.As(err, &taken) || taken.Target != oldTarget {
		t.Fatalf("foreign command not reported: %v", err)
	}
	if got, _ := os.Readlink(link); got != oldTarget {
		t.Fatal("touched the foreign command without permission")
	}
	rec, err := LinkCommand(bin, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(link); got != bin {
		t.Fatalf("zapret -> %s", got)
	}
	if got, _ := os.Readlink(rec.Old); got != oldTarget || filepath.Base(rec.Old) != "zapret-old" {
		t.Fatalf("old command not kept: %+v", rec)
	}
	// Reinstall is a no-op and remembers what it displaced.
	if rec2, err := LinkCommand(bin, 0, false); err != nil || rec2.Old != rec.Old {
		t.Fatalf("reinstall: %+v %v", rec2, err)
	}
	UnlinkCommand(bin)
	if got, _ := os.Readlink(link); got != oldTarget {
		t.Fatalf("old command not restored: %q", got)
	}
	if _, err := os.Lstat(rec.Old); err == nil {
		t.Fatal("zapret-old left behind")
	}
	// Fresh system: no previous command at all.
	os.Remove(link)
	if _, err := LinkCommand(bin, 0, false); err != nil {
		t.Fatal(err)
	}
	UnlinkCommand(bin)
	if _, err := os.Lstat(link); err == nil {
		t.Fatal("link not removed")
	}
}
