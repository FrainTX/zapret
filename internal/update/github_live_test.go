package update

import (
	"context"
	"os"
	"testing"
)

// PROSVET_LIVE=1 go test -run GitHubLive ./internal/update
func TestGitHubLive(t *testing.T) {
	if os.Getenv("PROSVET_LIVE") == "" {
		t.Skip("set PROSVET_LIVE=1")
	}
	rel, err := GitHub{Repo: "FrainTX/zapret"}.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bin, err := Download(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: verified, binary %d bytes", rel.Tag, len(bin))
}
