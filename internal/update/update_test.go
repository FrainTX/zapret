package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "0.9.0", true}, {"0.10.0", "0.9.9", true}, {"v1.2", "1.2.0", false},
		{"1.2.0", "1.2.0-rc1", true}, {"1.2.0-rc1", "1.2.0", false}, {"0.9.0", "1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s,%s)=%v", c.a, c.b, got)
		}
	}
}

func tgz(t *testing.T, name string, body []byte) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "prosvet-1.0.0-linux-amd64/" + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return b.Bytes()
}

// A mirror serving a signed release: the binary comes out; a tampered
// archive, a forged signature or a wrong key are all refused.
func TestDownloadVerifies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := PublicKey
	PublicKey = base64.StdEncoding.EncodeToString(pub)
	defer func() { PublicKey = old }()

	name := AssetName("1.0.0", runtime.GOOS, runtime.GOARCH)
	arc := tgz(t, "prosvet", []byte("NEW BINARY"))
	sum := sha256.Sum256(arc)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)))
	files := map[string][]byte{"/latest": []byte("v1.0.0\n"), "/v1.0.0/SHA256SUMS": sums, "/v1.0.0/SHA256SUMS.sig": sig, "/v1.0.0/" + name: arc}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	src := Mirror{Base: srv.URL}
	ctx := context.Background()
	rel, err := src.Latest(ctx)
	if err != nil || rel.Version != "1.0.0" {
		t.Fatalf("latest: %+v %v", rel, err)
	}
	bin, err := Download(ctx, rel)
	if err != nil || string(bin) != "NEW BINARY" {
		t.Fatalf("download: %q %v", bin, err)
	}
	// tampered archive
	files["/v1.0.0/"+name] = tgz(t, "prosvet", []byte("EVIL"))
	if _, err := Download(ctx, rel); err == nil || !strings.Contains(err.Error(), "контрольная сумма") {
		t.Fatalf("tampered archive accepted: %v", err)
	}
	// attacker rewrites SHA256SUMS too, but cannot sign it
	evil := sha256.Sum256(files["/v1.0.0/"+name])
	files["/v1.0.0/SHA256SUMS"] = []byte(hex.EncodeToString(evil[:]) + "  " + name + "\n")
	if _, err := Download(ctx, rel); err == nil || !strings.Contains(err.Error(), "подпись") {
		t.Fatalf("unsigned sums accepted: %v", err)
	}
	// signed with some other key
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	files["/v1.0.0/SHA256SUMS.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(other, files["/v1.0.0/SHA256SUMS"])))
	if _, err := Download(ctx, rel); err == nil {
		t.Fatal("foreign key accepted")
	}
}
