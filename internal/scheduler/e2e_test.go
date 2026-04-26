package scheduler_test

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fabianringel/cfunc/internal/gateway"
	"github.com/fabianringel/cfunc/internal/scheduler"
)

// TestE2E_SchedulerTriggersGateway proves the full integration:
// Scheduler.FireNow -> HTTPTrigger -> Gateway -> spawned function ->
// response. Uses the host-subprocess spawner so this runs on macOS.
func TestE2E_SchedulerTriggersGateway(t *testing.T) {
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "example")
	cmd := exec.Command("go", "build", "-o", bin, "./templates/go/example")
	cmd.Dir = repo
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}

	gw := gateway.New()
	defer gw.Close()
	gw.Register("daily-report", bin)

	srv := httptest.NewServer(gw)
	defer srv.Close()

	store, _ := scheduler.OpenStore(filepath.Join(t.TempDir(), "cron.json"))
	if err := store.Add(scheduler.Job{
		ID:       "j1",
		Schedule: "0 0 1 1 *", // never fires during the test
		Function: "daily-report",
		Method:   "GET",
	}); err != nil {
		t.Fatal(err)
	}

	tg := &scheduler.HTTPTrigger{BaseURL: srv.URL}
	sch := scheduler.New(store, tg, nil)
	if err := sch.Start(); err != nil {
		t.Fatal(err)
	}
	defer sch.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sch.FireNow(ctx, "j1"); err != nil {
		t.Fatalf("FireNow: %v", err)
	}
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
