package engine

import (
	"os/exec"
	"syscall"
	"time"
)

// winws needs a moment to open the WinDivert driver (it may be installing it).
const settleTime = 1200 * time.Millisecond

const createNoWindow = 0x08000000

func prepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func terminate(cmd *exec.Cmd) {
	// winws has no graceful shutdown signal reachable from a service; the
	// WinDivert handle is released by the kernel when the process dies.
	_ = cmd.Process.Kill()
}
