package platform

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func detectOS(i *Info) {
	i.Engine = "winws"
	i.Init = "windows-scm"
	i.Firewall = "windivert"
	i.Elevated = windows.GetCurrentProcessToken().IsElevated()
	i.EngineBundled = i.Arch == "amd64" || i.Arch == "386"

	v := windows.RtlGetVersion()
	i.Kernel = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	i.Distro = Distro{ID: "windows", Name: "Windows", BuildIDNum: int(v.BuildNumber)}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProductName")
		disp, _, _ := k.GetStringValue("DisplayVersion")
		k.Close()
		// Windows 11 still reports "Windows 10" in ProductName.
		if v.BuildNumber >= 22000 {
			name = strings.Replace(name, "Windows 10", "Windows 11", 1)
		}
		if name != "" {
			i.Distro.Name = name
		}
		i.Distro.Version = disp
	}
	i.MachineArch = machineArch()
	if i.MachineArch == "arm64" {
		i.EngineBundled = false
		i.Notes = append(i.Notes, "Windows на ARM: драйвер WinDivert64 не подписан для arm64, обход работать не будет")
	}
	if v.MajorVersion < 10 {
		i.Notes = append(i.Notes, "Windows 7/8 поддерживается частично: нужен KB3033929 для подписи драйвера SHA-256")
	}
	if wd := os.Getenv("SystemRoot"); wd != "" {
		if _, err := os.Stat(wd + `\System32\curl.exe`); err != nil {
			i.Notes = append(i.Notes, "в системе нет curl.exe — Просвет он и не нужен, проверки встроены")
		}
	}
	i.Conflicts = detectConflicts()
}

func machineArch() string {
	var processMachine, nativeMachine uint16
	if err := windows.IsWow64Process2(windows.CurrentProcess(), &processMachine, &nativeMachine); err == nil {
		switch nativeMachine {
		case 0xaa64:
			return "arm64"
		case 0x8664:
			return "amd64"
		case 0x14c:
			return "386"
		}
	}
	a := os.Getenv("PROCESSOR_ARCHITEW6432")
	if a == "" {
		a = os.Getenv("PROCESSOR_ARCHITECTURE")
	}
	switch strings.ToUpper(a) {
	case "AMD64":
		return "amd64"
	case "ARM64":
		return "arm64"
	case "X86":
		return "386"
	}
	return a
}

// FindTool is a no-op on Windows; everything we need is bundled.
func FindTool(name string) string { return "" }

// Processes returns the lower-cased image names of running processes.
func Processes() map[string]uint32 {
	out := map[string]uint32{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out[strings.ToLower(windows.UTF16ToString(e.ExeFile[:]))] = e.ProcessID
	}
	return out
}

type serviceRef struct {
	name   string
	detail string
	fix    bool
}

func detectConflicts() []Conflict {
	var c []Conflict
	procs := Processes()
	for _, p := range []struct{ img, name, detail string }{
		{"winws.exe", "zapret (winws)", "запущена другая копия zapret (например, Flowseal) — WinDivert нельзя делить"},
		{"goodbyedpi.exe", "GoodbyeDPI", "GoodbyeDPI перехватывает те же пакеты"},
		{"ciadpi.exe", "ByeDPI", "ByeDPI работает параллельно"},
		{"spoofdpi.exe", "SpoofDPI", "SpoofDPI работает параллельно"},
		{"adguardsvc.exe", "AdGuard", "AdGuard фильтрует трафик и может ломать Discord"},
	} {
		if pid, ok := procs[p.img]; ok {
			c = append(c, Conflict{Name: p.name, Kind: "process", Ref: p.img, Fixable: p.img != "adguardsvc.exe",
				Detail: fmt.Sprintf("%s (PID %d)", p.detail, pid)})
		}
	}

	m, err := mgr.Connect()
	if err != nil {
		return c
	}
	defer m.Disconnect()
	names, err := m.ListServices()
	if err != nil {
		return c
	}
	for _, n := range names {
		ln := strings.ToLower(n)
		var ref *serviceRef
		switch {
		case ln == "zapret" || strings.HasPrefix(ln, "zapret_") || ln == "winws1" || ln == "winws2":
			ref = &serviceRef{n, "служба zapret от другой сборки установлена в автозапуск", true}
		case ln == "goodbyedpi":
			ref = &serviceRef{n, "служба GoodbyeDPI", true}
		case strings.Contains(ln, "killer"):
			ref = &serviceRef{n, "Killer Network Service ломает WinDivert — рекомендуется отключить", false}
		case strings.Contains(ln, "smartbyte"):
			ref = &serviceRef{n, "SmartByte конфликтует с WinDivert", false}
		case ln == "tracsrvwrapper" || ln == "epwd":
			ref = &serviceRef{n, "Check Point конфликтует с WinDivert", false}
		case strings.Contains(ln, "intel") && strings.Contains(ln, "connectivity"):
			ref = &serviceRef{n, "Intel Connectivity Network Service конфликтует с WinDivert", false}
		}
		if ref == nil {
			continue
		}
		if !serviceActive(m, n) && ref.fix {
			// Installed but stopped is still worth reporting for zapret
			// services, since they come back at the next boot.
			if !strings.HasPrefix(ln, "zapret") && ln != "goodbyedpi" {
				continue
			}
		}
		c = append(c, Conflict{Name: n, Kind: "service", Ref: n, Fixable: ref.fix, Detail: ref.detail})
	}
	return c
}

func serviceActive(m *mgr.Mgr, name string) bool {
	s, err := m.OpenService(name)
	if err != nil {
		return false
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return false
	}
	return st.State == svc.Running || st.State == svc.StartPending
}

// FixConflict stops/disables a conflicting piece of software.
func FixConflict(c Conflict) error {
	switch c.Kind {
	case "process":
		pid, ok := Processes()[strings.ToLower(c.Ref)]
		if !ok {
			return nil
		}
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(h)
		return windows.TerminateProcess(h, 1)
	case "service":
		m, err := mgr.Connect()
		if err != nil {
			return err
		}
		defer m.Disconnect()
		s, err := m.OpenService(c.Ref)
		if err != nil {
			return err
		}
		defer s.Close()
		_, _ = s.Control(svc.Stop)
		cfg, err := s.Config()
		if err == nil {
			cfg.StartType = mgr.StartDisabled
			_ = s.UpdateConfig(cfg)
		}
		return nil
	}
	return fmt.Errorf("не знаю, как исправить %s", c.Kind)
}

// InUserNamespace is a Linux concept.
func InUserNamespace() bool { return false }
