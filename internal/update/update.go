// Package update keeps Просвет on the latest release.
//
// Releases come from GitHub (or a mirror with the same files). Every release
// carries SHA256SUMS and SHA256SUMS.sig — an ed25519 signature made with a
// key that never leaves the maintainer's machine and CI secrets. The updater
// runs as root/SYSTEM on every installation, so it trusts nothing it cannot
// verify: not the transport, not the hosting account. An archive is only
// installed when its hash is listed in a correctly signed SHA256SUMS.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases come from ("owner/name"). Release
// builds set it with -ldflags "-X prosvet/internal/update.Repo=…"; empty
// means a development build that never updates itself.
var Repo = ""

// PublicKey verifies SHA256SUMS.sig (base64 ed25519).
var PublicKey = "AkZfxgMHxUIHzixB8h+0vLH8ZVamJCI+bVxipL4cfUw="

// Release is an available version.
type Release struct {
	Tag     string    `json:"tag"`
	Version string    `json:"version"`
	Notes   string    `json:"notes,omitempty"`
	Date    time.Time `json:"date"`
	files   map[string]string
}

// Source finds and downloads releases.
type Source interface {
	Latest(ctx context.Context) (Release, error)
	Name() string
}

// GitHub is the default source.
type GitHub struct{ Repo string }

func (g GitHub) Name() string { return "github.com/" + g.Repo }

func (g GitHub) Latest(ctx context.Context) (Release, error) {
	var r struct {
		Tag     string    `json:"tag_name"`
		Body    string    `json:"body"`
		Draft   bool      `json:"draft"`
		Pre     bool      `json:"prerelease"`
		Created time.Time `json:"published_at"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	b, err := get(ctx, "https://api.github.com/repos/"+g.Repo+"/releases/latest", 1<<20)
	if err != nil {
		return Release{}, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return Release{}, fmt.Errorf("ответ GitHub: %w", err)
	}
	rel := Release{Tag: r.Tag, Version: strings.TrimPrefix(r.Tag, "v"), Notes: r.Body, Date: r.Created, files: map[string]string{}}
	for _, a := range r.Assets {
		rel.files[a.Name] = a.URL
	}
	return rel, nil
}

// Mirror serves the same files from your own server:
//
//	<base>/latest          — one line, the tag (v1.2.3)
//	<base>/<tag>/<file>    — the release files, SHA256SUMS and SHA256SUMS.sig
type Mirror struct{ Base string }

func (m Mirror) Name() string { return m.Base }

func (m Mirror) Latest(ctx context.Context) (Release, error) {
	b, err := get(ctx, strings.TrimRight(m.Base, "/")+"/latest", 256)
	if err != nil {
		return Release{}, err
	}
	tag := strings.TrimSpace(string(b))
	if !validTag(tag) {
		return Release{}, fmt.Errorf("зеркало: странная версия %q", tag)
	}
	rel := Release{Tag: tag, Version: strings.TrimPrefix(tag, "v"), files: map[string]string{}}
	base := strings.TrimRight(m.Base, "/") + "/" + tag + "/"
	for _, f := range []string{"SHA256SUMS", "SHA256SUMS.sig", AssetName(rel.Version, runtime.GOOS, runtime.GOARCH)} {
		rel.files[f] = base + f
	}
	return rel, nil
}

func validTag(t string) bool {
	if !strings.HasPrefix(t, "v") || len(t) > 32 {
		return false
	}
	for _, c := range t[1:] {
		if !(c >= '0' && c <= '9' || c == '.' || c == '-' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// AssetName is the archive for a platform, as scripts/build.sh names it.
func AssetName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "prosvet-" + version + "-" + goos + "-" + goarch + ext
}

// Newer reports whether version a is newer than b (dotted numbers; a
// "-suffix" pre-release sorts before the plain version).
func Newer(a, b string) bool {
	pa, sa, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	pb, sb, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	xa, xb := strings.Split(pa, "."), strings.Split(pb, ".")
	for i := 0; i < max(len(xa), len(xb)); i++ {
		var na, nb int
		if i < len(xa) {
			na, _ = strconv.Atoi(xa[i])
		}
		if i < len(xb) {
			nb, _ = strconv.Atoi(xb[i])
		}
		if na != nb {
			return na > nb
		}
	}
	switch {
	case sa == sb:
		return false
	case sa == "":
		return true
	case sb == "":
		return false
	}
	return sa > sb
}

var client = &http.Client{Timeout: 3 * time.Minute}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "prosvet-updater")
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream;q=0.9, */*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path.Base(url), resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: файл больше %d байт", path.Base(url), limit)
	}
	return b, nil
}

var errNotFound = errors.New("релизов пока нет")

// ErrNoRelease means the source has nothing published yet.
var ErrNoRelease = errNotFound

// Verify checks a signature of SHA256SUMS against PublicKey.
func Verify(sums, sig []byte) error {
	pub, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("в программе нет ключа проверки обновлений")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || !ed25519.Verify(pub, sums, s) {
		return errors.New("подпись обновления не сходится — установка отменена")
	}
	return nil
}

// hashFor finds a file's hash in SHA256SUMS.
func hashFor(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// Download fetches this platform's binary from a release and proves it is
// genuine: signed SHA256SUMS, matching hash.
func Download(ctx context.Context, rel Release) ([]byte, error) {
	name := AssetName(rel.Version, runtime.GOOS, runtime.GOARCH)
	need := func(f string) (string, error) {
		u := rel.files[f]
		if u == "" {
			return "", fmt.Errorf("в релизе %s нет файла %s", rel.Tag, f)
		}
		return u, nil
	}
	uSums, err := need("SHA256SUMS")
	if err != nil {
		return nil, err
	}
	uSig, err := need("SHA256SUMS.sig")
	if err != nil {
		return nil, err
	}
	uArc, err := need(name)
	if err != nil {
		return nil, err
	}
	sums, err := get(ctx, uSums, 64<<10)
	if err != nil {
		return nil, err
	}
	sig, err := get(ctx, uSig, 1<<10)
	if err != nil {
		return nil, err
	}
	if err := Verify(sums, sig); err != nil {
		return nil, err
	}
	want, ok := hashFor(sums, name)
	if !ok {
		return nil, fmt.Errorf("%s нет в SHA256SUMS", name)
	}
	arc, err := get(ctx, uArc, 64<<20)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(arc)
	if hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("контрольная сумма %s не совпала — файл повреждён или подменён", name)
	}
	return extract(arc, runtime.GOOS == "windows")
}

func extract(arc []byte, isZip bool) ([]byte, error) {
	want := "prosvet"
	if isZip {
		want = "prosvet.exe"
		zr, err := zip.NewReader(bytes.NewReader(arc), int64(len(arc)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == want {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 128<<20))
			}
		}
		return nil, errors.New("в архиве нет prosvet.exe")
	}
	gz, err := gzip.NewReader(bytes.NewReader(arc))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, errors.New("в архиве нет prosvet")
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == want {
			return io.ReadAll(io.LimitReader(tr, 128<<20))
		}
	}
}

// Install puts a verified binary in place of dst. The new file is first
// asked for its version, so a binary that cannot even start on this machine
// never replaces a working one.
func Install(bin []byte, dst, version string) error {
	dir := filepath.Dir(dst)
	tmp := filepath.Join(dir, ".prosvet-update"+filepath.Ext(dst))
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmp, "version").Output()
	if err != nil || !strings.Contains(string(out), version) {
		os.Remove(tmp)
		return fmt.Errorf("новая версия не запускается на этой машине (%v)", err)
	}
	if runtime.GOOS == "windows" {
		// A running .exe cannot be overwritten but can be renamed.
		old := filepath.Join(dir, "prosvet.old.exe")
		_ = os.Remove(old)
		if err := os.Rename(dst, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Rename(old, dst)
			return err
		}
		return nil
	}
	return os.Rename(tmp, dst)
}

// Cleanup removes what an earlier update left behind (Windows keeps the
// replaced .exe until the next start).
func Cleanup(dst string) {
	if runtime.GOOS == "windows" {
		_ = os.Remove(filepath.Join(filepath.Dir(dst), "prosvet.old.exe"))
	}
}
