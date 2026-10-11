package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"prosvet/internal/update"
)

// UpdateStatus is what the interface shows about updates.
type UpdateStatus struct {
	Auto       bool      `json:"auto"`
	Source     string    `json:"source"`
	Current    string    `json:"current"`
	Latest     string    `json:"latest,omitempty"`
	Available  bool      `json:"available"`
	Checked    time.Time `json:"checked"`
	Installing bool      `json:"installing,omitempty"`
	Error      string    `json:"error,omitempty"`
	Notes      string    `json:"notes,omitempty"`
}

// ErrRestart asks the daemon loop to start the freshly installed binary.
var ErrRestart = errors.New("restart for update")

const updateEvery = 6 * time.Hour

func (c *Controller) sourcesLocked() []update.Source {
	var out []update.Source
	if update.Repo != "" {
		out = append(out, update.GitHub{Repo: update.Repo})
	}
	if m := c.cfg.UpdateMirror; m != "" {
		out = append(out, update.Mirror{Base: m})
	}
	return out
}

func (c *Controller) updateStatusLocked() *UpdateStatus {
	s := c.upd
	s.Auto = !c.cfg.NoAutoUpdate
	s.Current = Version
	var names []string
	for _, src := range c.sourcesLocked() {
		names = append(names, src.Name())
	}
	s.Source = strings.Join(names, " · ")
	if len(names) == 0 {
		s.Source = "сборка без источника обновлений"
	}
	return &s
}

// RestartRequested fires once a new version is installed.
func (c *Controller) RestartRequested() <-chan struct{} { return c.restart }

// CheckUpdate looks for a newer release and, if install is set, installs it
// and asks the daemon to restart into it.
func (c *Controller) CheckUpdate(ctx context.Context, install bool) (UpdateStatus, error) {
	c.mu.Lock()
	srcs := c.sourcesLocked()
	if c.upd.Installing {
		c.mu.Unlock()
		return *c.updateStatusLocked(), errors.New("обновление уже устанавливается")
	}
	c.mu.Unlock()
	if len(srcs) == 0 {
		return c.setUpdate(func(u *UpdateStatus) {
			u.Error = "эта сборка не знает, откуда брать обновления"
		}), nil
	}
	var rel update.Release
	var src update.Source
	var errs []string
	for _, s := range srcs {
		r, err := s.Latest(ctx)
		if err == nil {
			rel, src = r, s
			break
		}
		errs = append(errs, s.Name()+": "+err.Error())
	}
	if src == nil {
		msg := strings.Join(errs, "; ")
		st := c.setUpdate(func(u *UpdateStatus) { u.Checked, u.Error = time.Now(), msg })
		return st, errors.New(msg)
	}
	avail := update.Newer(rel.Version, Version)
	st := c.setUpdate(func(u *UpdateStatus) {
		u.Checked, u.Latest, u.Available, u.Error, u.Notes = time.Now(), rel.Version, avail, "", rel.Notes
	})
	if !avail || !install {
		return st, nil
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return st, err
	}
	c.setUpdate(func(u *UpdateStatus) { u.Installing = true })
	defer c.setUpdate(func(u *UpdateStatus) { u.Installing = false })
	c.Log.Infof("update", "скачиваю %s из %s", rel.Tag, src.Name())
	bin, err := update.Download(ctx, rel)
	if err == nil {
		err = update.Install(bin, exe, rel.Version)
	}
	if err != nil {
		c.Log.Errorf("update", "%s: %v", rel.Tag, err)
		st = c.setUpdate(func(u *UpdateStatus) { u.Error = err.Error() })
		return st, err
	}
	c.Log.Infof("update", "установлена версия %s, перезапускаюсь", rel.Version)
	select {
	case c.restart <- struct{}{}:
	default:
	}
	return c.setUpdate(func(u *UpdateStatus) { u.Available = false }), nil
}

func (c *Controller) setUpdate(fn func(*UpdateStatus)) UpdateStatus {
	c.mu.Lock()
	fn(&c.upd)
	st := *c.updateStatusLocked()
	c.mu.Unlock()
	go c.statusChanged()
	return st
}

// StartUpdater checks for updates a few minutes after start and then every
// six hours (with jitter, so installations do not all hit GitHub at once),
// installing them unless the user turned automatic updates off.
func (c *Controller) StartUpdater(ctx context.Context) {
	go func() {
		wait := 3*time.Minute + time.Duration(rand.Int64N(int64(2*time.Minute)))
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = updateEvery + time.Duration(rand.Int64N(int64(time.Hour)))
			c.mu.Lock()
			auto := !c.cfg.NoAutoUpdate
			tuning := c.tune != nil && c.tune.Active
			c.mu.Unlock()
			if tuning {
				wait = 10 * time.Minute
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			if _, err := c.CheckUpdate(cctx, auto); err != nil && !errors.Is(err, update.ErrNoRelease) {
				c.Log.Warnf("update", "проверка обновлений: %v", err)
				wait = time.Hour
			}
			cancel()
		}
	}()
}

func validMirror(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("зеркало обновлений: нужен адрес https://…")
	}
	return nil
}
