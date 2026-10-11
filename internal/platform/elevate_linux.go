package platform

import (
	"errors"
	"os"
	"os/exec"
)

// RunElevated runs exe with args as root: through pkexec when graphical
// (polkit dialog), otherwise through sudo/doas in the current terminal.
func RunElevated(exe string, args []string, graphical bool) (int, error) {
	var tools []string
	if graphical {
		tools = []string{"pkexec"}
	} else {
		tools = []string{"sudo", "doas", "pkexec"}
	}
	for _, t := range tools {
		p, err := exec.LookPath(t)
		if err != nil {
			continue
		}
		cmd := exec.Command(p, append([]string{exe}, args...)...)
		if !graphical {
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		}
		err = cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		if err != nil {
			return -1, err
		}
		return 0, nil
	}
	return -1, errors.New("не найден sudo/doas/pkexec")
}
