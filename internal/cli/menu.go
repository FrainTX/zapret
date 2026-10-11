package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"

	"prosvet/internal/platform"
	"prosvet/internal/service"
	"prosvet/internal/tui"
	"prosvet/internal/ui"
)

// menu is what a double-click on prosvet(.exe) or a bare `prosvet` opens:
// the full-screen console interface. Without a terminal it prints the
// status instead.
func menu() int {
	if !ui.Color || !isTerminal(os.Stdin) {
		fmt.Print(usage)
		return 0
	}
	for {
		a, isLocal, err := connect()
		o := tui.Options{Local: isLocal}
		switch {
		case err == nil:
			o.Backend = a
		case errors.Is(err, errNeedRoot):
			// No service and no rights: games and Telegram still work.
		default:
			return fail(err)
		}
		if !service.Query(platform.Detect()).Installed {
			o.Install = func() error { return nil }
		}
		res, err := tui.Run(o)
		if err != nil {
			return fail(err)
		}
		if !res.Install {
			return 0
		}
		if code := cmdInstall(nil); code != 0 {
			fmt.Print(ui.Dim("\n  Enter — назад"))
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
