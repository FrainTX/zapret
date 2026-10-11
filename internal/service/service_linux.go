package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"prosvet/internal/platform"
)

func replace(tmp, dst string) error { return os.Rename(tmp, dst) }

// InstallDir picks where the binary lives: /usr/local/bin normally,
// /var/lib/prosvet/bin on immutable systems (SteamOS, Silverblue...).
func InstallDir(info platform.Info) string {
	if !info.Immutable {
		if err := os.MkdirAll("/usr/local/bin", 0o755); err == nil {
			probe := "/usr/local/bin/.prosvet-probe"
			if f, err := os.Create(probe); err == nil {
				f.Close()
				os.Remove(probe)
				return "/usr/local/bin"
			}
		}
	}
	return "/var/lib/prosvet/bin"
}

func sh(name string, args ...string) error {
	p := platform.FindTool(name)
	if p == "" {
		return fmt.Errorf("не найдена утилита %s", name)
	}
	out, err := exec.Command(p, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

const systemdUnit = `[Unit]
Description=Просвет — обход DPI на движке zapret
After=network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=on-failure
RestartSec=3
PrivateTmp=yes
ProtectHome=yes

[Install]
WantedBy=multi-user.target
`

const openrcScript = `#!/sbin/openrc-run
name="prosvet"
description="Просвет — обход DPI"
command="%s"
command_args="daemon"
command_background=true
pidfile="/run/prosvet.pid"
output_log="/var/log/prosvet.log"
error_log="/var/log/prosvet.log"

depend() {
	need net
	after firewall
}
`

const sysvScript = `#!/bin/sh
### BEGIN INIT INFO
# Provides:          prosvet
# Required-Start:    $network $remote_fs
# Required-Stop:     $network $remote_fs
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: Просвет — обход DPI
### END INIT INFO
BIN=%s
PID=/run/prosvet.pid
case "$1" in
  start)
    start-stop-daemon --start --background --make-pidfile --pidfile $PID --exec $BIN -- daemon ;;
  stop)
    start-stop-daemon --stop --pidfile $PID --retry 5; rm -f $PID ;;
  restart)
    $0 stop; $0 start ;;
  status)
    start-stop-daemon --status --pidfile $PID && echo running || { echo stopped; exit 3; } ;;
  *) echo "Usage: $0 {start|stop|restart|status}"; exit 1 ;;
esac
`

const dinitService = `type = process
command = %s daemon
restart = true
smooth-recovery = true
logfile = /var/log/prosvet.log
`

// runitDirs returns (service source dir, enabled-services dir).
func runitDirs() (string, string) {
	if _, err := os.Stat("/etc/runit/sv"); err == nil { // Artix
		return "/etc/runit/sv", "/run/runit/service"
	}
	return "/etc/sv", "/var/service" // Void
}

// Install copies exe into place and registers the daemon with the init
// system. ownerUID (if >0) may control the daemon without sudo.
func Install(exe string, info platform.Info, ownerUID int) (State, error) {
	if os.Geteuid() != 0 {
		return State{}, errors.New("нужны права root: запустите через sudo")
	}
	if info.NixOS {
		return State{}, errors.New("NixOS: используйте `prosvet install --print-nix` и добавьте модуль в configuration.nix")
	}
	dir := InstallDir(info)
	bin := filepath.Join(dir, "prosvet")
	if err := copyFile(exe, bin, 0o755); err != nil {
		return State{}, err
	}
	if err := os.MkdirAll("/etc/prosvet", 0o755); err != nil {
		return State{}, err
	}
	if ownerUID > 0 {
		_ = os.WriteFile("/etc/prosvet/owner", []byte(strconv.Itoa(ownerUID)+"\n"), 0o644)
	}
	st := State{Installed: true, Manager: info.Init, Binary: bin}
	var err error
	switch info.Init {
	case "systemd":
		if err = os.WriteFile("/etc/systemd/system/prosvet.service", fmt.Appendf(nil, systemdUnit, bin), 0o644); err == nil {
			if err = sh("systemctl", "daemon-reload"); err == nil {
				err = sh("systemctl", "enable", "--now", "prosvet.service")
				if err == nil {
					_ = sh("systemctl", "restart", "prosvet.service")
				}
			}
		}
	case "openrc":
		if err = os.WriteFile("/etc/init.d/prosvet", fmt.Appendf(nil, openrcScript, bin), 0o755); err == nil {
			_ = sh("rc-update", "add", "prosvet", "default")
			err = sh("rc-service", "prosvet", "restart")
		}
	case "runit":
		src, enabled := runitDirs()
		sv := filepath.Join(src, "prosvet")
		if err = os.MkdirAll(sv, 0o755); err == nil {
			run := "#!/bin/sh\nexec " + bin + " daemon 2>&1\n"
			if err = os.WriteFile(filepath.Join(sv, "run"), []byte(run), 0o755); err == nil {
				link := filepath.Join(enabled, "prosvet")
				if _, e := os.Lstat(link); e != nil {
					err = os.Symlink(sv, link)
				} else {
					_ = sh("sv", "restart", "prosvet")
				}
			}
		}
	case "dinit":
		if err = os.WriteFile("/etc/dinit.d/prosvet", fmt.Appendf(nil, dinitService, bin), 0o644); err == nil {
			if err = sh("dinitctl", "enable", "prosvet"); err != nil && strings.Contains(err.Error(), "already") {
				err = sh("dinitctl", "restart", "prosvet")
			}
		}
	case "sysv":
		if err = os.WriteFile("/etc/init.d/prosvet", fmt.Appendf(nil, sysvScript, bin), 0o755); err == nil {
			switch {
			case platform.FindTool("update-rc.d") != "":
				_ = sh("update-rc.d", "prosvet", "defaults")
			case platform.FindTool("chkconfig") != "":
				_ = sh("chkconfig", "--add", "prosvet")
			}
			err = sh("/etc/init.d/prosvet", "restart")
		}
	default:
		return st, fmt.Errorf("init-система %q не поддерживается для автозапуска — запускайте `%s daemon` вручную", info.Init, bin)
	}
	if err != nil {
		return st, err
	}
	st.Running = true
	return st, nil
}

// Uninstall removes the service registration and the installed binaries.
// Configuration and state are kept unless purge is set.
func Uninstall(info platform.Info, purge bool) error {
	if os.Geteuid() != 0 {
		return errors.New("нужны права root: запустите через sudo")
	}
	switch info.Init {
	case "systemd":
		_ = sh("systemctl", "disable", "--now", "prosvet.service")
		os.Remove("/etc/systemd/system/prosvet.service")
		_ = sh("systemctl", "daemon-reload")
	case "openrc":
		_ = sh("rc-service", "prosvet", "stop")
		_ = sh("rc-update", "del", "prosvet", "default")
		os.Remove("/etc/init.d/prosvet")
	case "runit":
		src, enabled := runitDirs()
		os.Remove(filepath.Join(enabled, "prosvet"))
		_ = sh("sv", "down", filepath.Join(src, "prosvet"))
		os.RemoveAll(filepath.Join(src, "prosvet"))
	case "dinit":
		_ = sh("dinitctl", "disable", "prosvet")
		_ = sh("dinitctl", "stop", "prosvet")
		os.Remove("/etc/dinit.d/prosvet")
	case "sysv":
		_ = sh("/etc/init.d/prosvet", "stop")
		if platform.FindTool("update-rc.d") != "" {
			_ = sh("update-rc.d", "-f", "prosvet", "remove")
		} else if platform.FindTool("chkconfig") != "" {
			_ = sh("chkconfig", "--del", "prosvet")
		}
		os.Remove("/etc/init.d/prosvet")
	}
	UnlinkCommand(filepath.Join(InstallDir(info), "prosvet"))
	for _, d := range []string{"/usr/local/bin", "/var/lib/prosvet/bin"} {
		os.Remove(filepath.Join(d, "prosvet"))
	}
	if purge {
		os.RemoveAll("/etc/prosvet")
		os.RemoveAll("/var/lib/prosvet")
		os.Remove("/var/log/prosvet.log")
	}
	return nil
}

// Query returns the service state.
func Query(info platform.Info) State {
	st := State{Manager: info.Init}
	for _, d := range []string{"/usr/local/bin", "/var/lib/prosvet/bin"} {
		if p := filepath.Join(d, "prosvet"); fileExists(p) {
			st.Binary = p
		}
	}
	switch info.Init {
	case "systemd":
		st.Installed = fileExists("/etc/systemd/system/prosvet.service")
		if p := platform.FindTool("systemctl"); p != "" {
			st.Running = exec.Command(p, "is-active", "--quiet", "prosvet.service").Run() == nil
		}
	case "openrc", "sysv":
		st.Installed = fileExists("/etc/init.d/prosvet")
	case "runit":
		src, _ := runitDirs()
		st.Installed = fileExists(filepath.Join(src, "prosvet", "run"))
	case "dinit":
		st.Installed = fileExists("/etc/dinit.d/prosvet")
	}
	return st
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// NixModule prints a NixOS module for declarative installation.
func NixModule(bin string) string {
	return `# Просвет — добавьте в configuration.nix и выполните nixos-rebuild switch
{ pkgs, ... }: {
  networking.nftables.enable = true;
  systemd.services.prosvet = {
    description = "Просвет — обход DPI";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    path = [ pkgs.nftables pkgs.kmod ];
    serviceConfig = {
      ExecStart = "` + bin + ` daemon";
      Restart = "on-failure";
      StateDirectory = "prosvet";
    };
  };
}
`
}

// AfterStart is a no-op on Linux: an update re-executes the daemon in place.
func AfterStart() {}
