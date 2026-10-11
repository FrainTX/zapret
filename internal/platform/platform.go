// Package platform figures out what kind of machine prosvet is running on so
// the rest of the program can pick the right engine, firewall backend and
// service manager without asking the user.
package platform

import (
	"runtime"
	"strings"
)

// Info is a snapshot of everything the installer and engine care about.
type Info struct {
	OS          string `json:"os"`   // linux, windows
	Arch        string `json:"arch"` // GOARCH of the running binary
	MachineArch string `json:"machineArch"`
	Kernel      string `json:"kernel"`
	Distro      Distro `json:"distro"`
	Init        string `json:"init"`     // systemd, openrc, runit, dinit, s6, sysv, windows-scm, unknown
	Firewall    string `json:"firewall"` // nftables, iptables, iptables-legacy, windivert, none
	Elevated    bool   `json:"elevated"`
	Immutable   bool   `json:"immutable"` // read-only /usr (SteamOS, Silverblue, MicroOS...)
	NixOS       bool   `json:"nixos"`
	WSL         bool   `json:"wsl"`
	Container   bool   `json:"container"`
	// Engine is the zapret flavour that fits: nfqws (Linux) or winws (Windows).
	Engine string `json:"engine"`
	// EngineBundled is false when no embedded binary exists for this
	// GOOS/GOARCH and a system-wide nfqws must be used instead.
	EngineBundled bool `json:"engineBundled"`
	// Missing lists required tools/kernel features that are absent.
	Missing []string `json:"missing,omitempty"`
	// Conflicts lists other DPI-bypass software that will fight with us.
	Conflicts []Conflict `json:"conflicts,omitempty"`
	// Notes are human readable remarks (Russian) shown in the UI.
	Notes []string `json:"notes,omitempty"`
	// PackageManager is used to print exact install hints.
	PackageManager string `json:"packageManager,omitempty"`
}

// Distro describes a Linux distribution (from os-release) or Windows edition.
type Distro struct {
	ID         string   `json:"id"`
	Like       []string `json:"like,omitempty"`
	Name       string   `json:"name"`
	Version    string   `json:"version,omitempty"`
	VariantID  string   `json:"variantId,omitempty"`
	BuildIDNum int      `json:"build,omitempty"` // Windows build number
}

// Conflict is a piece of software that also intercepts traffic.
type Conflict struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	// Fixable means prosvet can stop/disable it on request.
	Fixable bool   `json:"fixable"`
	Kind    string `json:"kind"` // process, service, nft-table, iptables
	Ref     string `json:"ref"`  // process name / service name / table name
}

// Family reports whether the distro is or derives from id.
func (d Distro) Family(id string) bool {
	if d.ID == id {
		return true
	}
	for _, l := range d.Like {
		if l == id {
			return true
		}
	}
	return false
}

// Detect gathers platform information. It never fails: unknown values are
// reported as such so that the UI can explain what is missing.
func Detect() Info {
	i := Info{OS: runtime.GOOS, Arch: runtime.GOARCH, MachineArch: runtime.GOARCH}
	detectOS(&i)
	return i
}

// InstallHint returns a shell command that installs pkg on this distro.
func (i Info) InstallHint(pkg string) string {
	switch i.PackageManager {
	case "apt":
		return "sudo apt install " + pkg
	case "dnf":
		return "sudo dnf install " + pkg
	case "yum":
		return "sudo yum install " + pkg
	case "pacman":
		return "sudo pacman -S " + pkg
	case "zypper":
		return "sudo zypper install " + pkg
	case "apk":
		return "sudo apk add " + pkg
	case "xbps":
		return "sudo xbps-install " + pkg
	case "emerge":
		return "sudo emerge --ask net-firewall/" + pkg
	case "eopkg":
		return "sudo eopkg install " + pkg
	case "nix":
		return "nix-env -iA nixos." + pkg
	case "rpm-ostree":
		return "rpm-ostree install " + pkg
	}
	return "установите пакет " + pkg + " средствами вашего дистрибутива"
}

func trimQuotes(s string) string {
	return strings.Trim(strings.TrimSpace(s), `"'`)
}
