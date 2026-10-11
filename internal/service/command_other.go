//go:build !linux

package service

// Command is the short name that opens the panel (on Windows: zapret.bat).
const Command = "zapret"

// CommandLink describes the short command (Linux only).
type CommandLink struct {
	Link string `json:"link"`
	Old  string `json:"old,omitempty"`
}

// ErrCommandTaken is never returned outside Linux.
type ErrCommandTaken struct{ Path, Target string }

func (e *ErrCommandTaken) Error() string { return "command taken" }

// LinkCommand is a no-op outside Linux.
func LinkCommand(string, int, bool) (CommandLink, error) { return CommandLink{}, nil }

// UnlinkCommand is a no-op outside Linux.
func UnlinkCommand(string) {}
