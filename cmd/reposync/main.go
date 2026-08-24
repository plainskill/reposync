package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"reposync/internal/config"
	"reposync/internal/engine"
	"reposync/internal/hook"
	"reposync/internal/single"
	"reposync/internal/store"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.StateRoot, 0o700); err != nil {
		log.Error("state_root", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.HubsDir(), 0o700); err != nil {
		log.Error("hubs", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SQLite), 0o700); err != nil {
		log.Error("sqlite dir", "err", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg.SQLite)
	if err != nil {
		log.Error("sqlite", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	eng := engine.New(cfg, db, log)
	mux := hook.Mux(eng.Enqueue, hook.Secrets{
		GitHub:  cfg.GitHub.WebhookSecret,
		Forgejo: cfg.Forgejo.WebhookSecret,
	}, log)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	lk, err := single.Acquire(cfg.StateRoot)
	if err != nil {
		log.Error("instance lock", "err", err)
		os.Exit(1)
	}
	defer lk.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listen", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http", "err", err)
			stop()
		}
	}()
	go eng.Run(ctx)

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
