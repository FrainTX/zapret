package netroute

import (
	"encoding/binary"
	"syscall"

	"golang.org/x/sys/windows"
)

const ipUnicastIf = 31 // IP_UNICAST_IF

func (v Bind) control(_, _ string, c syscall.RawConn) error {
	if v.Index == 0 {
		return nil
	}
	// The index goes in network byte order for IPv4.
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v.Index))
	idx := int(binary.NativeEndian.Uint32(b[:]))
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIf, idx)
	})
	if err != nil {
		return err
	}
	return serr
}
