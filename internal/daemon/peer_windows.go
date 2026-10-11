package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
)

// pipeSDDL: SYSTEM and Administrators get full access, interactive users may
// read and write. Non-elevated admins carry Administrators as a deny-only
// group, which is why IU is needed for the console UI to talk to the service.
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

func listen(pipe string) (net.Listener, error) {
	return winio.ListenPipe(pipe, &winio.PipeConfig{SecurityDescriptor: pipeSDDL, InputBufferSize: 64 * 1024, OutputBufferSize: 64 * 1024})
}

// The pipe ACL is the gate: anyone who could open it is authorized.
func peerOf(net.Conn) Peer { return Peer{UID: 0, Authorized: true} }

// Dial connects to the daemon pipe.
func Dial(pipe string) (net.Conn, error) {
	timeout := 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, pipe)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrNoDaemon
		}
		return nil, err
	}
	return c, nil
}
