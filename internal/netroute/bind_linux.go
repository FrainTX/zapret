package netroute

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func (v Bind) control(_, _ string, c syscall.RawConn) error {
	if v.Iface == "" {
		return nil
	}
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, v.Iface)
	})
	if err != nil {
		return err
	}
	return serr
}
