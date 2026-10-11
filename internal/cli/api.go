package cli

import (
	"context"
	"encoding/json"
	"errors"

	"prosvet/internal/config"
	"prosvet/internal/core"
	"prosvet/internal/daemon"
	"prosvet/internal/logbuf"
	"prosvet/internal/platform"
	"prosvet/internal/probe"
	"prosvet/internal/tuner"
)

// api is what the CLI needs; the daemon client and the in-process
// controller both provide it.
type api interface {
	Status(ctx context.Context) (core.Status, error)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	SetService(ctx context.Context, id string, on bool) error
	SetDomains(ctx context.Context, domains []string, exclude bool) ([]string, error)
	SetPick(ctx context.Context, group, cand string) error
	Settings(ctx context.Context, s core.Settings) error
	Tune(ctx context.Context, o core.TuneOptions) error
	CancelTune(ctx context.Context) error
	Events(ctx context.Context, fn func(daemon.RawEvent)) error
	Check(ctx context.Context) ([]probe.Result, error)
	UpdateLists(ctx context.Context) (map[string]int, error)
	FixConflict(ctx context.Context, ref string) error
	Report(ctx context.Context) (*tuner.Report, error)
	Logs(ctx context.Context) ([]logbuf.Entry, error)
	Plan(ctx context.Context) (daemon.PlanInfo, error)
	Update(ctx context.Context, install bool) (core.UpdateStatus, error)
}

// local adapts an in-process controller (no service installed, running
// with root/admin rights directly).
type local struct{ c *core.Controller }

func (l local) Status(context.Context) (core.Status, error) { return l.c.Status(), nil }
func (l local) Start(context.Context) error                 { return l.c.Start() }
func (l local) Stop(context.Context) error                  { return l.c.Stop() }
func (l local) SetService(_ context.Context, id string, on bool) error {
	return l.c.SetService(id, on)
}
func (l local) SetDomains(_ context.Context, d []string, ex bool) ([]string, error) {
	return l.c.SetDomains(d, ex)
}
func (l local) SetPick(_ context.Context, g, c string) error            { return l.c.SetPick(g, c) }
func (l local) Settings(_ context.Context, s core.Settings) error       { return l.c.ApplySettings(s) }
func (l local) Tune(_ context.Context, o core.TuneOptions) error        { return l.c.Tune(o) }
func (l local) CancelTune(context.Context) error                        { l.c.CancelTune(); return nil }
func (l local) Check(ctx context.Context) ([]probe.Result, error)       { return l.c.Check(ctx), nil }
func (l local) UpdateLists(ctx context.Context) (map[string]int, error) { return l.c.UpdateLists(ctx) }
func (l local) FixConflict(_ context.Context, ref string) error         { return l.c.FixConflict(ref) }
func (l local) Report(context.Context) (*tuner.Report, error)           { return l.c.LastReport(), nil }
func (l local) Logs(context.Context) ([]logbuf.Entry, error)            { return l.c.Log.Snapshot(), nil }
func (l local) Update(ctx context.Context, install bool) (core.UpdateStatus, error) {
	return l.c.CheckUpdate(ctx, install)
}
func (l local) Plan(context.Context) (daemon.PlanInfo, error) {
	plan, err := l.c.Plan()
	if err != nil {
		return daemon.PlanInfo{}, err
	}
	b, _ := json.Marshal(plan)
	return daemon.PlanInfo{Engine: l.c.EngineBinary(), Plan: b, Firewall: l.c.FirewallScript(plan)}, nil
}

func (l local) Events(ctx context.Context, fn func(daemon.RawEvent)) error {
	ch, cancel := l.c.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			b, _ := json.Marshal(e.Data)
			fn(daemon.RawEvent{Type: e.Type, Data: b})
		}
	}
}

// localCtl is the in-process controller, if any; Main shuts it down on exit
// so a one-off `sudo prosvet tune` never leaves rules behind.
var localCtl *core.Controller

var errNeedRoot = errors.New("служба Просвет не установлена, а для работы без неё нужны права администратора")

// connect returns the daemon client if the service runs, or an in-process
// controller when we have the rights to drive the engine ourselves.
func connect() (api, bool, error) {
	paths := config.DefaultPaths()
	cl := daemon.NewClient(paths.Socket)
	if cl.Available() {
		return cl, false, nil
	}
	if !platform.Detect().Elevated {
		return nil, false, errNeedRoot
	}
	if localCtl != nil {
		return local{localCtl}, true, nil
	}
	c, err := core.New(paths, nil)
	if err != nil {
		return nil, false, err
	}
	localCtl = c
	return local{c}, true, nil
}
