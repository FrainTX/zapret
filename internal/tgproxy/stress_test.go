package tgproxy

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// PROSVET_LIVE=1 go test -run Stress -v ./internal/tgproxy
func TestStress(t *testing.T) {
	if os.Getenv("PROSVET_LIVE") == "" {
		t.Skip("set PROSVET_LIVE=1")
	}
	secret := NewSecret()
	p := New(Config{Listen: "127.0.0.1:19444", Secret: secret, Logf: t.Logf, Direct: AroundVPN})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Serve(ctx)
	time.Sleep(300 * time.Millisecond)
	for _, dc := range []int{1, 5, -1, -5} {
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok, fail := 0, 0
		start := time.Now()
		for i := 0; i < stressN(); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tctx, c := context.WithTimeout(ctx, 25*time.Second)
				defer c()
				_, err := SelfTest(tctx, "127.0.0.1:19444", secret, dc)
				mu.Lock()
				if err != nil {
					fail++
					t.Logf("DC%d err: %v", dc, err)
				} else {
					ok++
				}
				mu.Unlock()
			}()
		}
		wg.Wait()
		t.Logf("DC%d: %d ok, %d fail in %v", dc, ok, fail, time.Since(start).Round(time.Millisecond))
	}
	t.Logf("stats %+v", p.Snapshot())
}

func stressN() int {
	if os.Getenv("PROSVET_STRESS") != "" {
		return 40
	}
	return 10
}
