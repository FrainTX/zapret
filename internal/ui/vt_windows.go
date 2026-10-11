package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on ANSI escape processing in the Windows console (10+)
// and switches it to UTF-8 so Cyrillic renders correctly.
func enableVT() bool {
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleCP(65001)
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
