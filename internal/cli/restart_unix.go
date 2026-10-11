//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// restartSelf replaces this process with the freshly installed binary:
// same PID, so systemd, OpenRC, runit and the rest never notice.
func restartSelf() int {
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fail(err)
	}
	return 0
}
