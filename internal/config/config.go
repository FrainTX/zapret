// Package config stores prosvet's persistent settings and knows where every
// file lives on each platform.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"prosvet/internal/strategy"
)

// Config is persisted as JSON. Zero values are valid defaults.
type Config struct {
	Version int `json:"version"`
	// Enabled means the daemon keeps the engine running (and restores it
	// after reboot).
	Enabled bool `json:"enabled"`
	// Services toggles user-facing targets (strategy.Svc*).
	Services map[string]bool `json:"services"`
	// Picks stores the chosen candidate id per group ("none" allowed).
	Picks map[string]string `json:"picks"`
	// Domains are the user's own hostnames to unblock.
	Domains []string `json:"domains"`
	// Exclude are hostnames never to touch.
	Exclude []string `json:"exclude"`
	// Providers restricts the "cloud" set; empty means all.
	Providers []string `json:"providers,omitempty"`
	// AutoHeal re-tunes groups that stop working.
	AutoHeal bool `json:"autoHeal"`
	// HealMinutes is the health-check period.
	HealMinutes int `json:"healMinutes"`
	// IPv6 enables IPv6 interception on Linux.
	IPv6 bool `json:"ipv6"`
	// QNum is the NFQUEUE number on Linux.
	QNum int `json:"qnum"`
	// TelegramPort and TelegramSecret configure the local MTProto proxy.
	TelegramPort   int    `json:"telegramPort,omitempty"`
	TelegramSecret string `json:"telegramSecret,omitempty"`
	// GameDirect sends game UDP around a VPN tunnel when one is the
	// default route: "" (auto, on when a VPN is detected) or "off".
	GameDirect string `json:"gameDirect,omitempty"`
	// NoAutoUpdate turns automatic updates off (they are on by default).
	NoAutoUpdate bool `json:"noAutoUpdate,omitempty"`
	// UpdateMirror is your own server with the release files, tried when
	// GitHub is unreachable (see package update).
	UpdateMirror string `json:"updateMirror,omitempty"`
	// Bred are recipes Просвет synthesized and found worth keeping.
	Bred map[string]strategy.Bred `json:"bred,omitempty"`
	// Tuned is when the last full tuning finished.
	Tuned time.Time `json:"tuned"`
	// TunedOn identifies the network the tuning was done on, so a laptop
	// moving between ISPs knows its picks may be stale.
	TunedOn string `json:"tunedOn,omitempty"`
}

// Default returns a fresh configuration.
func Default() *Config {
	return &Config{
		Version:     1,
		Services:    map[string]bool{strategy.SvcYouTube: true, strategy.SvcDiscord: true, strategy.SvcSites: true},
		Picks:       map[string]string{},
		AutoHeal:    true,
		HealMinutes: 15,
		QNum:        216,
	}
}

// Normalize fills in defaults after loading an older or hand-edited file.
func (c *Config) Normalize() {
	if c.Services == nil {
		c.Services = map[string]bool{}
	}
	if c.Picks == nil {
		c.Picks = map[string]string{}
	}
	if c.HealMinutes <= 0 {
		c.HealMinutes = 15
	}
	if c.QNum <= 0 || c.QNum > 65535 {
		c.QNum = 216
	}
	if c.Version == 0 {
		c.Version = 1
	}
	for g, id := range c.Picks {
		if _, ok := strategy.ParseCandidateID(id); !ok {
			delete(c.Picks, g)
		}
	}
}

// Pick returns the candidate for a group: the stored pick, the derived one
// for inheriting groups, or the group default.
func (c *Config) Pick(g strategy.Group) strategy.Candidate {
	if id, ok := c.Picks[g.ID]; ok {
		if cand, ok := strategy.ParseCandidateID(id); ok {
			return cand
		}
	}
	if g.Inherit != "" {
		if parent, ok := strategy.GroupByID(g.Inherit); ok {
			if _, picked := c.Picks[parent.ID]; picked {
				return strategy.Derive(g, c.Pick(parent))
			}
		}
	}
	cand, _ := strategy.ParseCandidateID(g.Default)
	return cand
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	out.Normalize()
	return &out
}

// Paths are the filesystem locations used by prosvet.
type Paths struct {
	ConfigFile string // settings
	StateDir   string // engine, generated decoys, lists, reports
	RunDir     string // socket, pid
	LogFile    string
	Socket     string // unix socket path or named pipe
}

// DefaultPaths returns the system-wide locations for this OS. PROSVET_HOME
// relocates everything into one directory (portable mode, tests).
func DefaultPaths() Paths {
	if home := os.Getenv("PROSVET_HOME"); home != "" {
		sock := filepath.Join(home, "prosvet.sock")
		if len(sock) > 100 { // sun_path is 108 bytes
			sum := sha256.Sum256([]byte(home))
			sock = filepath.Join(os.TempDir(), "prosvet-"+hex.EncodeToString(sum[:6])+".sock")
		}
		if runtime.GOOS == "windows" {
			sock = `\\.\pipe\prosvet-portable`
		}
		return Paths{
			ConfigFile: filepath.Join(home, "config.json"),
			StateDir:   filepath.Join(home, "state"),
			RunDir:     home,
			LogFile:    filepath.Join(home, "prosvet.log"),
			Socket:     sock,
		}
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		dir := filepath.Join(base, "Prosvet")
		return Paths{
			ConfigFile: filepath.Join(dir, "config.json"),
			StateDir:   dir,
			RunDir:     dir,
			LogFile:    filepath.Join(dir, "prosvet.log"),
			Socket:     `\\.\pipe\prosvet`,
		}
	}
	return Paths{
		ConfigFile: "/etc/prosvet/config.json",
		StateDir:   "/var/lib/prosvet",
		RunDir:     "/run/prosvet",
		LogFile:    "/var/log/prosvet.log",
		Socket:     "/run/prosvet/prosvet.sock",
	}
}

// Engine returns the directory holding the extracted engine.
func (p Paths) Engine() string { return filepath.Join(p.StateDir, "engine") }

// Fakes returns the directory holding generated decoys.
func (p Paths) Fakes() string { return filepath.Join(p.StateDir, "fakes") }

// Lists returns the directory holding hostlists and IP sets.
func (p Paths) Lists() string { return filepath.Join(p.StateDir, "lists") }

// Store loads and saves the configuration atomically.
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore returns a store for path.
func NewStore(path string) *Store { return &Store{path: path} }

// Load reads the config, returning defaults if the file does not exist.
func (s *Store) Load() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	c.Normalize()
	return c, nil
}

// Save writes the config via a temp file + rename.
func (s *Store) Save(c *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
