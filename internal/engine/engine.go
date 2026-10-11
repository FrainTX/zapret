// Package engine installs and runs the zapret packet engine (nfqws on Linux,
// winws on Windows) and produces every file it reads: decoys, hostlists and
// IP sets.
package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"prosvet/internal/logbuf"
)

// Version returns the bundled zapret release tag.
func Version() string {
	b, _ := bundleFS.ReadFile("bundle/VERSION")
	return strings.TrimSpace(string(b))
}

// Bundled reports whether this build carries an engine binary.
func Bundled() bool { return bundleDir != "" }

// BinaryName is the engine executable for this OS.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "winws.exe"
	}
	return "nfqws"
}

func manifest() map[string]string {
	m := map[string]string{}
	b, _ := bundleFS.ReadFile("bundle/MANIFEST.sha256")
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			m[f[1]] = f[0]
		}
	}
	return m
}

// Install extracts the bundled engine and stock decoys into dir, verifying
// every file against the embedded manifest. Files that already match are
// left alone, so it is cheap to call on every start; files that were
// quarantined or altered (antivirus) are restored.
func Install(engineDir, fakesDir string) error {
	man := manifest()
	if err := os.MkdirAll(engineDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(fakesDir, 0o755); err != nil {
		return err
	}
	type item struct{ src, dst, key string }
	var items []item
	if bundleDir != "" {
		entries, err := fs.ReadDir(bundleFS, bundleDir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			items = append(items, item{
				src: path.Join(bundleDir, e.Name()),
				dst: filepath.Join(engineDir, e.Name()),
				key: strings.TrimPrefix(path.Join(bundleDir, e.Name()), "bundle/"),
			})
		}
	}
	fakes, err := fs.ReadDir(bundleFS, "bundle/fake")
	if err != nil {
		return err
	}
	for _, e := range fakes {
		items = append(items, item{
			src: path.Join("bundle/fake", e.Name()),
			dst: filepath.Join(fakesDir, "stock_"+e.Name()),
			key: "fake/" + e.Name(),
		})
	}
	for _, it := range items {
		data, err := bundleFS.ReadFile(it.src)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		want := man[it.key]
		if want != "" && want != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("engine: embedded %s is corrupted", it.key)
		}
		if cur, err := os.ReadFile(it.dst); err == nil {
			cs := sha256.Sum256(cur)
			if cs == sum {
				continue
			}
		}
		mode := os.FileMode(0o644)
		if !strings.HasSuffix(it.dst, ".bin") {
			mode = 0o755
		}
		tmp := it.dst + ".new"
		if err := os.WriteFile(tmp, data, mode); err != nil {
			return err
		}
		if err := os.Rename(tmp, it.dst); err != nil {
			// winws.exe may be locked by a running instance.
			os.Remove(tmp)
			return fmt.Errorf("engine: cannot replace %s: %w", filepath.Base(it.dst), err)
		}
	}
	return nil
}

// Locate returns the engine executable to run: the extracted bundle or, on
// platforms without a bundled build, a system-wide nfqws.
func Locate(engineDir string) (string, error) {
	p := filepath.Join(engineDir, BinaryName())
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if runtime.GOOS != "windows" {
		for _, c := range []string{"/opt/zapret/nfq/nfqws", "/opt/zapret/binaries/my/nfqws", "/usr/bin/nfqws", "/usr/local/bin/nfqws", "/usr/sbin/nfqws"} {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		if p, err := exec.LookPath("nfqws"); err == nil {
			return p, nil
		}
	}
	return "", errors.New("engine: движок не найден — переустановите Просвет")
}

// Process supervises one engine instance.
type Process struct {
	Bin  string
	Log  *logbuf.Buffer
	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	args []string
	from time.Time
}

// ErrNotRunning is returned by Stop when nothing runs.
var ErrNotRunning = errors.New("engine: not running")

// Start launches the engine with args. It returns once the process has
// survived a short settling period, so argument errors surface immediately.
func (p *Process) Start(args []string) error {
	p.mu.Lock()
	if p.cmd != nil {
		p.mu.Unlock()
		return errors.New("engine: already running")
	}
	cmd := exec.Command(p.Bin, args...)
	cmd.Dir = filepath.Dir(p.Bin)
	out := &logbuf.Writer{B: p.Log, Level: "engine", Source: strings.TrimSuffix(filepath.Base(p.Bin), ".exe")}
	cmd.Stdout = out
	cmd.Stderr = out
	prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("engine: %w", err)
	}
	done := make(chan struct{})
	p.cmd, p.done, p.err, p.args, p.from = cmd, done, nil, args, time.Now()
	p.mu.Unlock()

	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.err = err
		if p.cmd == cmd {
			p.cmd = nil
		}
		p.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		return fmt.Errorf("engine: завершился сразу после запуска (%v) — подробности в журнале", err)
	case <-time.After(settleTime):
		return nil
	}
}

// Stop terminates the engine and waits for it to exit.
func (p *Process) Stop() error {
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	p.mu.Unlock()
	if cmd == nil {
		return ErrNotRunning
	}
	terminate(cmd)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	return nil
}

// Running reports whether the engine is alive.
func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

// Done returns a channel closed when the current process exits.
func (p *Process) Done() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	return p.done
}

// Since returns when the running process was started.
func (p *Process) Since() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.from
}

// Args returns the arguments of the current/last run.
func (p *Process) Args() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.args...)
}

// DryRun asks the engine to validate a command line without touching
// traffic (nfqws/winws --dry-run).
func DryRun(ctx context.Context, bin string, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"--dry-run"}, args...)...)
	cmd.Dir = filepath.Dir(bin)
	prepareCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 && len(msg) > 400 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("engine rejected arguments: %s", msg)
	}
	return nil
}
