package platform

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func detectOS(i *Info) {
	i.Engine = "nfqws"
	i.Elevated = os.Geteuid() == 0

	var u unix.Utsname
	if unix.Uname(&u) == nil {
		i.Kernel = unix.ByteSliceToString(u.Release[:])
		i.MachineArch = unix.ByteSliceToString(u.Machine[:])
	}
	i.Distro = readOSRelease()
	i.NixOS = i.Distro.ID == "nixos" || exists("/etc/NIXOS")
	i.WSL = strings.Contains(strings.ToLower(i.Kernel), "microsoft") || exists("/proc/sys/fs/binfmt_misc/WSLInterop")
	i.Container = exists("/.dockerenv") || exists("/run/.containerenv") || fileContains("/proc/1/cgroup", "docker")
	i.Immutable = detectImmutable(i.Distro)
	i.Init = detectInit()
	i.PackageManager = detectPackageManager(i.Distro)
	i.Firewall = detectFirewall()
	i.EngineBundled = bundledArch(i.Arch)

	if i.Firewall == "none" {
		i.Missing = append(i.Missing, "nftables")
	}
	if !kernelHasQueue() {
		i.Missing = append(i.Missing, "nfnetlink_queue")
	}
	if !i.EngineBundled {
		if p, err := exec.LookPath("nfqws"); err == nil {
			i.Notes = append(i.Notes, "для архитектуры "+i.MachineArch+" нет встроенного движка — будет использован системный "+p)
		} else {
			i.Missing = append(i.Missing, "nfqws")
		}
	}
	if i.WSL {
		i.Notes = append(i.Notes, "WSL не управляет трафиком Windows: запустите Просвет в самой Windows")
	}
	if i.Container {
		i.Notes = append(i.Notes, "запуск в контейнере: нужны права NET_ADMIN и доступ к netfilter хоста")
	}
	if i.NixOS {
		i.Notes = append(i.Notes, "NixOS: службу лучше описать декларативно — команда `prosvet install --print-nix` выведет готовый модуль")
	}
	if i.Immutable {
		i.Notes = append(i.Notes, "неизменяемая система: файлы будут установлены в /var/lib/prosvet, /usr не трогаем")
	}
	i.Conflicts = detectConflicts()
}

// bundledArch must stay in sync with the embedded engine builds.
func bundledArch(goarch string) bool {
	switch goarch {
	case "amd64", "386", "arm64", "arm", "mips", "mipsle":
		return true
	}
	return false
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fileContains(p, sub string) bool {
	b, err := os.ReadFile(p)
	return err == nil && strings.Contains(string(b), sub)
}

func readOSRelease() Distro {
	d := Distro{ID: "linux", Name: "Linux"}
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), "=")
			if !ok {
				continue
			}
			v = trimQuotes(v)
			switch k {
			case "ID":
				d.ID = v
			case "ID_LIKE":
				d.Like = strings.Fields(v)
			case "PRETTY_NAME":
				d.Name = v
			case "VERSION_ID":
				d.Version = v
			case "VARIANT_ID":
				d.VariantID = v
			}
		}
		f.Close()
		break
	}
	return d
}

func detectImmutable(d Distro) bool {
	switch d.ID {
	case "steamos", "bazzite", "aurora", "bluefin", "kinoite", "silverblue", "opensuse-microos", "opensuse-aeon", "opensuse-kalpa", "endless", "vanilla":
		return true
	}
	switch d.VariantID {
	case "silverblue", "kinoite", "sericea", "onyx", "iot", "coreos":
		return true
	}
	if exists("/run/ostree-booted") {
		return true
	}
	// Generic check: can we write to /usr/local?
	if os.Geteuid() == 0 {
		probe := "/usr/local/.prosvet-rw-probe"
		if f, err := os.Create(probe); err == nil {
			f.Close()
			os.Remove(probe)
			return false
		}
		return true
	}
	return false
}

func detectInit() string {
	switch {
	case exists("/run/systemd/system"):
		return "systemd"
	case exists("/run/openrc") || exists("/sbin/openrc-run") || exists("/usr/sbin/openrc-run"):
		return "openrc"
	case exists("/run/dinit") || lookPath("dinitctl"):
		return "dinit"
	case exists("/run/runit") || exists("/etc/runit/runsvdir") || (lookPath("sv") && exists("/etc/sv")):
		return "runit"
	case exists("/run/s6-rc") || lookPath("s6-rc"):
		return "s6"
	case exists("/etc/init.d") && (lookPath("update-rc.d") || lookPath("chkconfig") || lookPath("insserv")):
		return "sysv"
	}
	if b, err := os.ReadFile("/proc/1/comm"); err == nil {
		switch strings.TrimSpace(string(b)) {
		case "systemd":
			return "systemd"
		case "runit":
			return "runit"
		case "dinit":
			return "dinit"
		case "openrc-init":
			return "openrc"
		}
	}
	return "unknown"
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	if err == nil {
		return true
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
		if exists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

// FindTool resolves a system tool even when /usr/sbin is not in PATH, which
// is common for non-root sessions and minimal service environments.
func FindTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/local/bin"} {
		p := filepath.Join(dir, name)
		if exists(p) {
			return p
		}
	}
	return ""
}

func detectPackageManager(d Distro) string {
	switch {
	case d.ID == "nixos":
		return "nix"
	case exists("/run/ostree-booted") && lookPath("rpm-ostree"):
		return "rpm-ostree"
	case lookPath("apt"):
		return "apt"
	case lookPath("dnf"):
		return "dnf"
	case lookPath("pacman"):
		return "pacman"
	case lookPath("zypper"):
		return "zypper"
	case lookPath("apk"):
		return "apk"
	case lookPath("xbps-install"):
		return "xbps"
	case lookPath("emerge"):
		return "emerge"
	case lookPath("eopkg"):
		return "eopkg"
	case lookPath("yum"):
		return "yum"
	}
	return ""
}

func detectFirewall() string {
	if FindTool("nft") != "" {
		return "nftables"
	}
	if p := FindTool("iptables"); p != "" {
		out, _ := exec.Command(p, "--version").Output()
		if strings.Contains(string(out), "legacy") {
			return "iptables-legacy"
		}
		return "iptables"
	}
	return "none"
}

func kernelHasQueue() bool {
	if exists("/sys/module/nfnetlink_queue") || exists("/proc/net/netfilter/nfnetlink_queue") {
		return true
	}
	// Built as a module but not loaded yet: modprobe will take care of it.
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		rel := strings.TrimSpace(string(b))
		for _, base := range []string{"/lib/modules/", "/usr/lib/modules/"} {
			dir := base + rel + "/kernel/net/netfilter/"
			if m, _ := filepath.Glob(dir + "nfnetlink_queue.ko*"); len(m) > 0 {
				return true
			}
		}
		if fileContains("/lib/modules/"+rel+"/modules.builtin", "nfnetlink_queue") {
			return true
		}
	}
	return false
}

func detectConflicts() []Conflict {
	var c []Conflict
	procs := map[string]bool{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil {
			continue
		}
		procs[strings.TrimSpace(string(b))] = true
	}
	for _, name := range []string{"nfqws", "nfqws2", "tpws", "byedpi", "ciadpi", "spoofdpi", "goodbyedpi", "zapret"} {
		if procs[name] {
			c = append(c, Conflict{
				Name: name, Kind: "process", Ref: name, Fixable: true,
				Detail: "запущен сторонний обход DPI (" + name + ") — две копии будут мешать друг другу",
			})
		}
	}
	for _, unit := range []string{"zapret.service", "zapret2.service", "zapret-discord-youtube.service", "byedpi.service"} {
		for _, dir := range []string{"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"} {
			if exists(filepath.Join(dir, unit)) && exists(filepath.Join("/etc/systemd/system/multi-user.target.wants", unit)) {
				c = append(c, Conflict{
					Name: unit, Kind: "service", Ref: unit, Fixable: true,
					Detail: "служба " + unit + " включена в автозагрузку",
				})
				break
			}
		}
	}
	if nft := FindTool("nft"); nft != "" && os.Geteuid() == 0 {
		out, err := exec.Command(nft, "list", "tables").Output()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				f := strings.Fields(line)
				if len(f) == 3 && f[0] == "table" && (strings.Contains(f[2], "zapret") || f[2] == "byedpi") {
					c = append(c, Conflict{
						Name: "nft " + f[1] + " " + f[2], Kind: "nft-table", Ref: f[1] + " " + f[2], Fixable: true,
						Detail: "таблица nftables от другой установки zapret перехватывает те же пакеты",
					})
				}
			}
		}
	}
	return c
}

// FixConflict stops/disables a conflicting piece of software.
func FixConflict(c Conflict) error {
	switch c.Kind {
	case "process":
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			b, err := os.ReadFile("/proc/" + e.Name() + "/comm")
			if err == nil && strings.TrimSpace(string(b)) == c.Ref {
				_ = unix.Kill(pid, unix.SIGTERM)
			}
		}
		return nil
	case "service":
		if p := FindTool("systemctl"); p != "" {
			out, err := exec.Command(p, "disable", "--now", c.Ref).CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl: %s", strings.TrimSpace(string(out)))
			}
			return nil
		}
		return fmt.Errorf("остановите службу %s вручную", c.Ref)
	case "nft-table":
		f := strings.Fields(c.Ref)
		if len(f) != 2 {
			return fmt.Errorf("bad table ref")
		}
		out, err := exec.Command(FindTool("nft"), "delete", "table", f[0], f[1]).CombinedOutput()
		if err != nil {
			return fmt.Errorf("nft: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	return fmt.Errorf("не знаю, как исправить %s", c.Kind)
}

// InUserNamespace reports whether we run inside a non-initial user
// namespace (rootless containers, unshare -r).
func InUserNamespace() bool {
	b, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false
	}
	f := strings.Fields(string(b))
	return !(len(f) == 3 && f[0] == "0" && f[1] == "0" && f[2] == "4294967295")
}
