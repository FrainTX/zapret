//go:build !windows

package games

import (
	"os"
	"syscall"
)

func chownLike(path string, st os.FileInfo) {
	if s, ok := st.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		_ = os.Chown(path, int(s.Uid), int(s.Gid))
	}
}
