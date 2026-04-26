package gateway_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fabianringel/cfunc/internal/gateway"
)

// TestPoolConcurrencyCap proves the per-function pool never exceeds the
// configured MaxConcurrency, even under concurrent load that's an order
// of magnitude larger.
func TestPoolConcurrencyCap(t *testing.T) {
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "slow")
	cmd := exec.Command("go", "build", "-o", bin, "./internal/gateway/testdata/slow")
	cmd.Dir = repo
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build slow handler: %v", err)
	}

	const max = 4
	const total = 32

	gw := gateway.New()
	defer gw.Close()
	gw.RegisterDef(gateway.FunctionDef{
		Name:           "slow",
		Binary:         bin,
		MaxConcurrency: max,
	})
	srv := httptest.NewServer(gw)
	defer srv.Close()

	// Sample pool size while the load is in flight; record the peak.
	var peak atomic.Int32
	stop := make(chan struct{})
	sampler := make(chan struct{})
	go func() {
		defer close(sampler)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				snap := gw.Stats()
				for _, fn := range snap.Functions {
					if fn.Name != "slow" {
						continue
					}
					if int32(fn.PoolSize) > peak.Load() {
						peak.Store(int32(fn.PoolSize))
					}
				}
			}
		}
	}()

	var wg sync.WaitGroup
	var ok atomic.Int32
	client := &http.Client{Timeout: 30 * time.Second}
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL+"/fn/slow", nil)
			req.Header.Set("X-Sleep-Ms", "150")
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-sampler

	if got := ok.Load(); got != int32(total) {
		t.Fatalf("only %d/%d requests succeeded", got, total)
	}
	if got := peak.Load(); got > max {
		t.Fatalf("pool exceeded MaxConcurrency: peak=%d max=%d", got, max)
	}
	if peak.Load() < 2 {
		t.Fatalf("never observed concurrent instances — sampler raced or pool didn't scale")
	}
	t.Logf("succeeded=%d/%d  peak_pool=%d  max=%d", ok.Load(), total, peak.Load(), max)
}
