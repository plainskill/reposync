package engine

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"reposync/internal/catalog"
	"reposync/internal/config"
	fjclient "reposync/internal/forgejo"
	ghclient "reposync/internal/github"
	"reposync/internal/gitrec"
	"reposync/internal/store"
)

type Engine struct {
	Cfg  *config.Config
	DB   *store.DB
	Log  *slog.Logger
	Cat  *catalog.Runner
	Git  *gitrec.Runner
	wake chan struct{}
	mu   sync.Mutex
	busy map[string]struct{}
}

func New(cfg *config.Config, db *store.DB, log *slog.Logger) *Engine {
	gh := ghclient.New(cfg.GitHub.API, cfg.GitHub.Token)
	fj := fjclient.New(cfg.Forgejo.API, cfg.Forgejo.Token)
	return &Engine{
		Cfg: cfg,
		DB:  db,
		Log: log,
		Cat: &catalog.Runner{
			Cfg: cfg, DB: db, GitHub: gh, Forgejo: fj, Log: log,
			GHNF: ghclient.IsNotFound, FJNF: fjclient.IsNotFound,
		},
		Git:  &gitrec.Runner{Cfg: cfg, DB: db, Log: log},
		wake: make(chan struct{}, 1),
		busy: map[string]struct{}{},
	}
}

func (e *Engine) Enqueue(pairKey string) {
	if pairKey == "" {
		pairKey = store.AllKey
	}
	if _, err := e.DB.Enqueue(pairKey); err != nil {
		e.Log.Error("enqueue", "err", err)
		return
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.Cfg.ReconcileEvery)
	defer t.Stop()
	e.Enqueue(store.AllKey)
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
			e.drain(ctx)
		case <-t.C:
			e.Enqueue(store.AllKey)
			e.drain(ctx)
		}
	}
}

func (e *Engine) drain(ctx context.Context) {
	for {
		job, err := e.DB.NextQueued()
		if err != nil {
			e.Log.Error("queue", "err", err)
			return
		}
		if job == nil {
			return
		}
		runErr := e.apply(ctx, job.PairKey)
		if finErr := e.DB.Finish(job.ID, runErr); finErr != nil {
			e.Log.Error("finish", "err", finErr)
		}
		if runErr != nil {
			e.Log.Error("reconcile", "key", job.PairKey, "err", runErr)
		}
	}
}

func (e *Engine) apply(ctx context.Context, key string) error {
	e.mu.Lock()
	if _, ok := e.busy[key]; ok {
		e.mu.Unlock()
		return nil
	}
	e.busy[key] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.busy, key)
		e.mu.Unlock()
	}()

	if key == store.AllKey {
		if err := e.Cat.ReconcileAll(ctx); err != nil {
			return err
		}
		pairs, err := e.DB.ListPairs()
		if err != nil {
			return err
		}
		for _, p := range pairs {
			if !e.Cfg.KnownGitHubOwner(p.GitHubOwner) || !e.Cfg.KnownForgejoOwner(p.ForgejoOwner) {
				continue
			}
			if err := e.Git.SyncPair(ctx, p); err != nil {
				e.Log.Error("git", "repo", p.GitHubOwner+"/"+p.GitHubName, "err", err)
			}
		}
		return nil
	}
	ghOwner, ghName, fjOwner, fjName := e.splitKey(key)
	if ghOwner == "" {
		e.Log.Info("skip unknown owner", "key", key)
		return nil
	}
	if err := e.Cat.ReconcileOne(ctx, ghOwner, ghName, fjOwner, fjName); err != nil {
		return err
	}
	p, err := e.pairFor(ghOwner, ghName, fjOwner, fjName)
	if err != nil || p == nil {
		return err
	}
	return e.Git.SyncPair(ctx, *p)
}

func (e *Engine) splitKey(key string) (ghOwner, ghName, fjOwner, fjName string) {
	owner, name, ok := strings.Cut(key, "/")
	if !ok {
		return "", "", "", ""
	}
	if fj, ok := e.Cfg.ForgejoOwner(owner); ok {
		return owner, name, fj, name
	}
	if gh, ok := e.Cfg.GitHubOwner(owner); ok {
		return gh, name, owner, name
	}
	return "", "", "", ""
}

func (e *Engine) pairFor(ghOwner, ghName, fjOwner, fjName string) (*store.Pair, error) {
	if p, err := e.DB.PairByGitHub(ghOwner, ghName); err != nil || p != nil {
		return p, err
	}
	return e.DB.PairByForgejo(fjOwner, fjName)
}
