// Package daemon exposes the controller over a local, permission-checked
// HTTP API: a unix socket on Linux, a named pipe on Windows. The console
// interface and the commands are its clients.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"prosvet/internal/core"
	"prosvet/internal/engine"
	"prosvet/internal/lists"
	"prosvet/internal/strategy"
)

type ctxKey int

const peerKey ctxKey = 1

// Peer describes the client on the other side of the socket.
type Peer struct {
	UID        int
	Authorized bool
	Name       string
}

// Server wraps the controller in an HTTP API.
type Server struct {
	C   *core.Controller
	srv *http.Server
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

// Handler returns the API routes.
func (s *Server) Handler() http.Handler {
	c := s.C
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.Status())
	})
	mux.HandleFunc("GET /v1/platform", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.Platform())
	})
	mux.HandleFunc("GET /v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		type tpl struct {
			strategy.Template
			UsesSNI bool `json:"usesSni"`
		}
		var ts []tpl
		for _, t := range strategy.Templates {
			ts = append(ts, tpl{t, t.UsesSNI()})
		}
		builtin := map[string][]string{}
		for _, n := range []string{lists.YouTube, lists.Discord, lists.Ubisoft, lists.General, lists.Exclude} {
			builtin[n] = lists.Builtin(n)
		}
		writeJSON(w, 200, map[string]any{
			"services": strategy.Services, "groups": strategy.Groups, "templates": ts, "builtin": builtin,
			"sni": map[string][]string{"tls": strategy.TLSSNIPool, "quic": strategy.QUICSNIPool, "game": strategy.GameSNIPool},
		})
	})
	mux.HandleFunc("GET /v1/logs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.Log.Snapshot())
	})
	mux.HandleFunc("GET /v1/report", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.LastReport())
	})
	mux.HandleFunc("GET /v1/plan", func(w http.ResponseWriter, r *http.Request) {
		plan, err := c.Plan()
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"engine": c.EngineBinary(), "plan": plan, "firewall": c.FirewallScript(plan)})
	})
	mux.HandleFunc("GET /v1/events", s.events)

	// Mutating endpoints.
	mux.HandleFunc("POST /v1/start", func(w http.ResponseWriter, r *http.Request) {
		if err := c.Start(); err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, c.Status())
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Install bool `json:"install"`
		}
		if err := decode(r, &req); err != nil {
			fail(w, 400, err)
			return
		}
		st, err := c.CheckUpdate(r.Context(), req.Install)
		if err != nil {
			fail(w, 502, err)
			return
		}
		writeJSON(w, 200, st)
	})
	mux.HandleFunc("POST /v1/stop", func(w http.ResponseWriter, r *http.Request) {
		_ = c.Stop()
		writeJSON(w, 200, c.Status())
	})
	mux.HandleFunc("POST /v1/service", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
			On bool   `json:"on"`
		}
		if err := decode(r, &req); err != nil {
			fail(w, 400, err)
			return
		}
		if err := c.SetService(req.ID, req.On); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, c.Status())
	})
	mux.HandleFunc("POST /v1/domains", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domains []string `json:"domains"`
			Exclude bool     `json:"exclude"`
		}
		if err := decode(r, &req); err != nil {
			fail(w, 400, err)
			return
		}
		clean, err := c.SetDomains(req.Domains, req.Exclude)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"domains": clean})
	})
	mux.HandleFunc("POST /v1/pick", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Group     string `json:"group"`
			Candidate string `json:"candidate"`
		}
		if err := decode(r, &req); err != nil {
			fail(w, 400, err)
			return
		}
		if err := c.SetPick(req.Group, req.Candidate); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, c.Status())
	})
	mux.HandleFunc("POST /v1/settings", func(w http.ResponseWriter, r *http.Request) {
		var req core.Settings
		if err := decode(r, &req); err != nil {
			fail(w, 400, err)
			return
		}
		if err := c.ApplySettings(req); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, c.Status())
	})
	mux.HandleFunc("POST /v1/tune", func(w http.ResponseWriter, r *http.Request) {
		var req core.TuneOptions
		if r.ContentLength != 0 {
			if err := decode(r, &req); err != nil {
				fail(w, 400, err)
				return
			}
		}
		if err := c.Tune(req); err != nil {
			fail(w, 409, err)
			return
		}
		writeJSON(w, 202, c.Status())
	})
	mux.HandleFunc("DELETE /v1/tune", func(w http.ResponseWriter, r *http.Request) {
		c.CancelTune()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /v1/check", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		writeJSON(w, 200, c.Check(ctx))
	})
	mux.HandleFunc("POST /v1/lists/update", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		counts, err := c.UpdateLists(ctx)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, counts)
	})
	mux.HandleFunc("POST /v1/conflicts/fix", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Ref string `json:"ref"`
		}
		if err := decode(r, &req); err != nil {
			fail(w, 400, err)
			return
		}
		if err := c.FixConflict(req.Ref); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, c.Platform())
	})
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"prosvet": core.Version, "zapret": engine.Version()})
	})
	return s.guard(mux)
}

// guard enforces: reads are open to every local user, changes need an
// authorized peer (root/admin group/installing user, see peer_*.go).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Local API only: reject browsers trying DNS-rebinding style tricks.
		if r.Header.Get("Origin") != "" {
			fail(w, 403, errors.New("cross-origin requests are not allowed"))
			return
		}
		if r.Method != http.MethodGet {
			p, _ := r.Context().Value(peerKey).(Peer)
			if !p.Authorized {
				fail(w, 403, errors.New("нет прав: нужен администратор или пользователь, установивший Просвет"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel := s.C.Subscribe()
	defer cancel()
	enc := json.NewEncoder(w)
	write := func(e core.Event) bool {
		if _, err := w.Write([]byte("data: ")); err != nil {
			return false
		}
		if err := enc.Encode(e); err != nil { // Encode adds '\n'
			return false
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !write(core.Event{Type: "status", Data: s.C.Status()}) {
		return
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok || !write(e) {
				return
			}
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// Serve runs the API until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, socket string) error {
	ln, err := listen(socket)
	if err != nil {
		return err
	}
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, peerKey, peerOf(conn))
		},
	}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(sh)
	}()
	err = s.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
