package daemon

import (
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// OwnerFile records the uid of the user who installed prosvet; that user
// may control the daemon without sudo.
const OwnerFile = "/etc/prosvet/owner"

// adminGroups may control the daemon.
var adminGroups = []string{"wheel", "sudo", "admin", "prosvet"}

func listen(socket string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	// Everyone may connect; mutating calls are checked per peer.
	if err := os.Chmod(socket, 0o666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func peerOf(c net.Conn) Peer {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return Peer{UID: -1}
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{UID: -1}
	}
	var cred *unix.Ucred
	var cerr error
	_ = raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if cerr != nil || cred == nil {
		return Peer{UID: -1}
	}
	return authorize(int(cred.Uid))
}

func authorize(uid int) Peer {
	p := Peer{UID: uid}
	if uid == 0 {
		p.Authorized, p.Name = true, "root"
		return p
	}
	if b, err := os.ReadFile(OwnerFile); err == nil {
		if owner, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && owner == uid {
			p.Authorized = true
		}
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return p
	}
	p.Name = u.Username
	if p.Authorized {
		return p
	}
	gids, err := u.GroupIds()
	if err != nil {
		return p
	}
	for _, gid := range gids {
		if g, err := user.LookupGroupId(gid); err == nil && slices.Contains(adminGroups, g.Name) {
			p.Authorized = true
			return p
		}
	}
	return p
}

// Dial connects to the daemon socket.
func Dial(socket string) (net.Conn, error) {
	c, err := net.Dial("unix", socket)
	if err != nil {
		var se *net.OpError
		if errors.As(err, &se) {
			return nil, ErrNoDaemon
		}
		return nil, err
	}
	return c, nil
}
