//go:build !windows

package daemon

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// RunService runs fn until SIGINT/SIGTERM.
func RunService(fn func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return fn(ctx)
}

// IsService reports whether we were started by the service manager.
func IsService() bool { return os.Getenv("INVOCATION_ID") != "" || os.Getppid() == 1 }
