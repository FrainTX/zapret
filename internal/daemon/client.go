package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"prosvet/internal/core"
	"prosvet/internal/logbuf"
	"prosvet/internal/platform"
	"prosvet/internal/probe"
	"prosvet/internal/tuner"
)

// ErrNoDaemon means the background service is not installed or not running.
var ErrNoDaemon = errors.New("служба Просвет не запущена")

// Client talks to the daemon.
type Client struct {
	socket string
	hc     *http.Client
	stream *http.Client
}

// NewClient returns a client for the given socket / pipe.
func NewClient(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return Dial(socket) },
		// One connection per request keeps peer checks simple and avoids
		// stale pooled pipes on Windows.
		DisableKeepAlives: true,
	}
	return &Client{socket: socket, hc: &http.Client{Transport: tr, Timeout: 15 * time.Minute}, stream: &http.Client{Transport: tr}}
}

// RawEvent is an event with its payload still encoded.
type RawEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://prosvet"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if errors.Is(err, ErrNoDaemon) || strings.Contains(err.Error(), ErrNoDaemon.Error()) {
			return ErrNoDaemon
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Available reports whether the daemon answers.
func (c *Client) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.do(ctx, "GET", "/v1/version", nil, nil) == nil
}

func (c *Client) Status(ctx context.Context) (core.Status, error) {
	var s core.Status
	return s, c.do(ctx, "GET", "/v1/status", nil, &s)
}

func (c *Client) Platform(ctx context.Context) (platform.Info, error) {
	var p platform.Info
	return p, c.do(ctx, "GET", "/v1/platform", nil, &p)
}

func (c *Client) Start(ctx context.Context) error { return c.do(ctx, "POST", "/v1/start", nil, nil) }
func (c *Client) Stop(ctx context.Context) error  { return c.do(ctx, "POST", "/v1/stop", nil, nil) }

func (c *Client) SetService(ctx context.Context, id string, on bool) error {
	return c.do(ctx, "POST", "/v1/service", map[string]any{"id": id, "on": on}, nil)
}

func (c *Client) SetDomains(ctx context.Context, domains []string, exclude bool) ([]string, error) {
	var out struct {
		Domains []string `json:"domains"`
	}
	err := c.do(ctx, "POST", "/v1/domains", map[string]any{"domains": domains, "exclude": exclude}, &out)
	return out.Domains, err
}

func (c *Client) SetPick(ctx context.Context, group, cand string) error {
	return c.do(ctx, "POST", "/v1/pick", map[string]string{"group": group, "candidate": cand}, nil)
}

// Update checks for a new version and, with install, installs it.
func (c *Client) Update(ctx context.Context, install bool) (core.UpdateStatus, error) {
	var st core.UpdateStatus
	err := c.do(ctx, "POST", "/v1/update", map[string]bool{"install": install}, &st)
	return st, err
}

func (c *Client) Settings(ctx context.Context, s core.Settings) error {
	return c.do(ctx, "POST", "/v1/settings", s, nil)
}

func (c *Client) Tune(ctx context.Context, o core.TuneOptions) error {
	return c.do(ctx, "POST", "/v1/tune", o, nil)
}

func (c *Client) CancelTune(ctx context.Context) error {
	return c.do(ctx, "DELETE", "/v1/tune", nil, nil)
}

func (c *Client) Report(ctx context.Context) (*tuner.Report, error) {
	var r *tuner.Report
	return r, c.do(ctx, "GET", "/v1/report", nil, &r)
}

func (c *Client) Check(ctx context.Context) ([]probe.Result, error) {
	var r []probe.Result
	return r, c.do(ctx, "POST", "/v1/check", nil, &r)
}

func (c *Client) UpdateLists(ctx context.Context) (map[string]int, error) {
	var r map[string]int
	return r, c.do(ctx, "POST", "/v1/lists/update", nil, &r)
}

func (c *Client) FixConflict(ctx context.Context, ref string) error {
	return c.do(ctx, "POST", "/v1/conflicts/fix", map[string]string{"ref": ref}, nil)
}

func (c *Client) Logs(ctx context.Context) ([]logbuf.Entry, error) {
	var r []logbuf.Entry
	return r, c.do(ctx, "GET", "/v1/logs", nil, &r)
}

// PlanInfo is the rendered configuration.
type PlanInfo struct {
	Engine   string          `json:"engine"`
	Plan     json.RawMessage `json:"plan"`
	Firewall string          `json:"firewall"`
}

func (c *Client) Plan(ctx context.Context) (PlanInfo, error) {
	var p PlanInfo
	return p, c.do(ctx, "GET", "/v1/plan", nil, &p)
}

func (c *Client) Catalog(ctx context.Context) (json.RawMessage, error) {
	var r json.RawMessage
	return r, c.do(ctx, "GET", "/v1/catalog", nil, &r)
}

// Events streams live events until ctx ends or the connection drops.
func (c *Client) Events(ctx context.Context, fn func(RawEvent)) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://prosvet/v1/events", nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("events: %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e RawEvent
		if json.Unmarshal([]byte(line[6:]), &e) == nil {
			fn(e)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return sc.Err()
}
