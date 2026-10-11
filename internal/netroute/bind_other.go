//go:build !linux && !windows

package netroute

import "syscall"

func (v Bind) control(_, _ string, _ syscall.RawConn) error { return nil }
