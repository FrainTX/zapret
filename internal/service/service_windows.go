package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"prosvet/internal/platform"
)

func replace(tmp, dst string) error {
	// A running service locks the old exe: move it aside first.
	if fileExists(dst) {
		old := dst + ".old"
		os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Binaries are the executables shipped for Windows.
var Binaries = []string{"prosvet.exe"}

// InstallDir is %ProgramFiles%\Prosvet.
func InstallDir(platform.Info) string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "Prosvet")
}

func waitState(s *mgr.Service, want svc.State, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("служба не перешла в состояние %d", want)
}

// Install copies the binaries to Program Files and registers an
// auto-start service that runs `prosvet.exe daemon`.
func Install(exe string, info platform.Info, _ int) (State, error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return State{}, errors.New("нужны права администратора")
	}
	dir := InstallDir(info)
	bin := filepath.Join(dir, "prosvet.exe")
	m, err := mgr.Connect()
	if err != nil {
		return State{}, err
	}
	defer m.Disconnect()

	// Stop an existing instance before replacing its binary.
	if s, err := m.OpenService(Name); err == nil {
		_, _ = s.Control(svc.Stop)
		_ = waitState(s, svc.Stopped, 10*time.Second)
		s.Close()
	}
	if err := copyFile(exe, bin, 0o755); err != nil {
		return State{}, err
	}

	s, err := m.OpenService(Name)
	if err != nil {
		s, err = m.CreateService(Name, bin, mgr.Config{
			DisplayName:      Display,
			Description:      "Обход DPI для YouTube, Discord и игр на движке zapret (winws + WinDivert).",
			StartType:        mgr.StartAutomatic,
			DelayedAutoStart: false,
		}, "daemon")
		if err != nil {
			return State{}, err
		}
	} else {
		cfg, _ := s.Config()
		cfg.BinaryPathName = `"` + bin + `" daemon`
		cfg.StartType = mgr.StartAutomatic
		cfg.DisplayName = Display
		_ = s.UpdateConfig(cfg)
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 3 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)
	_ = s.SetRecoveryActionsOnNonCrashFailures(true)
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return State{Installed: true, Binary: bin}, err
	}
	_ = waitState(s, svc.Running, 15*time.Second)
	return State{Installed: true, Running: true, Manager: "windows-scm", Binary: bin}, nil
}

// Uninstall removes the service; WinDivert is unloaded too.
func Uninstall(info platform.Info, purge bool) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("нужны права администратора")
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil {
		_, _ = s.Control(svc.Stop)
		_ = waitState(s, svc.Stopped, 10*time.Second)
		_ = s.Delete()
		s.Close()
	}
	// The WinDivert driver registers its own service; remove it unless some
	// other winws still uses it.
	if _, busy := platform.Processes()["winws.exe"]; !busy {
		for _, n := range []string{"WinDivert", "WinDivert14"} {
			if s, err := m.OpenService(n); err == nil {
				_, _ = s.Control(svc.Stop)
				_ = s.Delete()
				s.Close()
			}
		}
	}
	dir := InstallDir(info)
	for _, name := range Binaries {
		os.Remove(filepath.Join(dir, name))
		os.Remove(filepath.Join(dir, name+".old"))
	}
	os.Remove(dir)
	if purge {
		pd := os.Getenv("ProgramData")
		if pd != "" {
			os.RemoveAll(filepath.Join(pd, "Prosvet"))
		}
	}
	return nil
}

// Query returns the service state.
func Query(info platform.Info) State {
	st := State{Manager: "windows-scm"}
	m, err := mgr.Connect()
	if err != nil {
		return st
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return st
	}
	defer s.Close()
	st.Installed = true
	if cfg, err := s.Config(); err == nil {
		st.Binary = cfg.BinaryPathName
	}
	if q, err := s.Query(); err == nil {
		st.Running = q.State == svc.Running
	}
	return st
}

// NixModule is meaningless on Windows.
func NixModule(string) string { return "" }

// AfterStart runs inside the service once it is up: the recovery actions
// must also fire when the service exits with an error on purpose (that is
// how an update restarts it), and the .exe an update replaced can go now.
func AfterStart() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(filepath.Join(filepath.Dir(exe), "prosvet.old.exe"))
	}
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return
	}
	defer s.Close()
	_ = s.SetRecoveryActionsOnNonCrashFailures(true)
}
