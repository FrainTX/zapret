//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
	"time"
)

const settleTime = 600 * time.Millisecond

func prepareCmd(cmd *exec.Cmd) {
	// The engine must never outlive the daemon: an orphaned nfqws with no
	// firewall rules is harmless, but one left behind after an upgrade would
	// fight the new instance for the queue.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

func terminate(cmd *exec.Cmd) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
}
