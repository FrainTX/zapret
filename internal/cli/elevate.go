package cli

import (
	"os"
	"path/filepath"

	"prosvet/internal/platform"
	"prosvet/internal/ui"
)

// elevate re-runs the command with administrator rights and waits for it.
func elevate(args []string) int {
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	println(ui.Dim("  нужны права администратора — подтвердите запрос системы"))
	code, err := platform.RunElevated(exe, args, false)
	if err != nil {
		return fail(err)
	}
	return code
}
