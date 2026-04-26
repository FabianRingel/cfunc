// Command scheduler is the cfunc cron daemon. It loads jobs from the
// JSON store, registers them with cron, and triggers HTTP calls to the
// gateway when each job fires. SIGHUP reloads the store.
package main

import (
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fabianringel/cfunc/internal/scheduler"
)

func main() {
	store := flag.String("store", defaultStorePath(), "cron store JSON path")
	gw := flag.String("gateway", "http://127.0.0.1:8080", "gateway base URL")
	flag.Parse()

	st, err := scheduler.OpenStore(*store)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	sch := scheduler.New(st, &scheduler.HTTPTrigger{BaseURL: *gw}, nil)
	if err := sch.Start(); err != nil {
		slog.Error("start", "err", err)
		os.Exit(1)
	}
	defer sch.Stop()

	slog.Info("scheduler running", "store", *store, "gateway", *gw)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	for s := range sigs {
		switch s {
		case syscall.SIGHUP:
			if err := sch.Reload(); err != nil {
				slog.Error("reload", "err", err)
			} else {
				slog.Info("reloaded")
			}
		default:
			slog.Info("shutting down", "signal", s.String())
			return
		}
	}
}

func defaultStorePath() string {
	root := os.Getenv("CFUNC_STORE")
	if root == "" {
		root = "/var/lib/cfunc"
	}
	return filepath.Join(root, "cron.json")
}
