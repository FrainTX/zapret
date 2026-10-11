// Package service installs prosvet as a background service on every
// supported init system and on Windows.
package service

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
)

// Name is the service identifier.
const Name = "prosvet"

// Display is the human-readable service name.
const Display = "Просвет — обход DPI"

// State is the installation state of the service.
type State struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Manager   string `json:"manager"`
	Binary    string `json:"binary"`
	Detail    string `json:"detail,omitempty"`
}

// copyFile copies src to dst atomically, skipping identical files.
func copyFile(src, dst string, mode os.FileMode) error {
	if same(src, dst) {
		return os.Chmod(dst, mode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return replace(tmp, dst)
}

func same(a, b string) bool {
	ha, err := hashFile(a)
	if err != nil {
		return false
	}
	hb, err := hashFile(b)
	return err == nil && ha == hb
}

func hashFile(p string) ([32]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}
