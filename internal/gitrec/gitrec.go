package gitrec

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"reposync/internal/config"
	"reposync/internal/gitops"
	"reposync/internal/model"
	"reposync/internal/store"
)

type Runner struct {
	Cfg *config.Config
	DB  *store.DB
	Log *slog.Logger
}

func (r *Runner) SyncPair(ctx context.Context, p store.Pair) error {
	if p.PeerGone {
		return nil
	}
	dir := filepath.Join(r.Cfg.HubsDir(), sanitize(p.GitHubOwner+"_"+p.GitHubName))
	wt := filepath.Join(r.Cfg.StateRoot, "worktrees", sanitize(p.GitHubOwner+"_"+p.GitHubName))
	repo := gitops.New(dir, wt, r.Cfg.Bot.Name, r.Cfg.Bot.Email)
	repo.Timeout = r.Cfg.GitTimeout
	ghURL := r.Cfg.GitHubGitURL(p.GitHubOwner, p.GitHubName)
	fjURL := r.Cfg.ForgejoGitURL(p.ForgejoOwner, p.ForgejoName)
	if err := repo.Ensure(ctx, map[string]string{"github": ghURL, "forgejo": fjURL}); err != nil {
		return err
	}
	if err := repo.FetchHeadsAndTags(ctx, "github"); err != nil {
		return fmt.Errorf("fetch github: %w", err)
	}
	if err := repo.FetchHeadsAndTags(ctx, "forgejo"); err != nil {
		return fmt.Errorf("fetch forgejo: %w", err)
	}
	if err := r.syncKind(ctx, repo, p, "heads"); err != nil {
		return err
	}
	return r.syncKind(ctx, repo, p, "tags")
}

func (r *Runner) syncKind(ctx context.Context, repo *gitops.Repo, p store.Pair, kind string) error {
	ghNames, err := repo.ListNames(ctx, "github", kind)
	if err != nil {
		return err
	}
	fjNames, err := repo.ListNames(ctx, "forgejo", kind)
	if err != nil {
		return err
	}
	seen := map[string]struct{}{}
	var names []string
	for _, n := range append(ghNames, fjNames...) {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	for _, name := range names {
		if err := r.syncRef(ctx, repo, p, kind, name); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) syncRef(ctx context.Context, repo *gitops.Repo, p store.Pair, kind, name string) error {
	gh, err := repo.TrackingSHA(ctx, "github", kind, name)
	if err != nil {
		return err
	}
	fj, err := repo.TrackingSHA(ctx, "forgejo", kind, name)
	if err != nil {
		return err
	}
	if gh == "" && fj == "" {
		return nil
	}
	ref := "refs/" + kind + "/" + name
	if kind == "heads" {
		ref = "refs/heads/" + name
	} else {
		ref = "refs/tags/" + name
	}
	if gh == fj {
		return nil
	}
	if kind == "tags" || model.IsConflictRef(ref) {
		return r.fastForwardOnly(ctx, repo, gh, fj, ref)
	}
	if gh == "" {
		return r.pushBothRemember(ctx, repo, "github", ref, fj)
	}
	if fj == "" {
		return r.pushBothRemember(ctx, repo, "forgejo", ref, gh)
	}
	ghAncFj, err := repo.IsAncestor(ctx, gh, fj)
	if err != nil {
		return err
	}
	fjAncGh, err := repo.IsAncestor(ctx, fj, gh)
	if err != nil {
		return err
	}
	if fjAncGh {
		return r.pushBothRemember(ctx, repo, "forgejo", ref, gh)
	}
	if ghAncFj {
		return r.pushBothRemember(ctx, repo, "github", ref, fj)
	}
	msg := fmt.Sprintf("reposync: merge github=%s forgejo=%s ref=%s", short(gh), short(fj), name)
	sha, conflict, err := repo.Merge(ctx, gh, fj, msg, true)
	if err != nil {
		return err
	}
	if !conflict {
		if err := r.pushSHA(ctx, repo, "github", ref, sha); err != nil {
			return err
		}
		if err := r.pushSHA(ctx, repo, "forgejo", ref, sha); err != nil {
			return err
		}
		return nil
	}
	conflictRef := model.ConflictRef(name)
	existing, err := repo.TrackingSHA(ctx, "github", "heads", strings.TrimPrefix(conflictRef, "refs/heads/"))
	if err != nil {
		return err
	}
	if existing != "" {
		parents, err := repo.Parents(ctx, existing)
		if err == nil && sameParents(parents, gh, fj) {
			r.Log.Info("reuse conflict branch", "ref", conflictRef)
			return r.ensureConflictOnBoth(ctx, repo, conflictRef, existing)
		}
	}
	r.Log.Info("content conflict; wrote markers", "ref", conflictRef, "github", short(gh), "forgejo", short(fj))
	if err := r.pushSHA(ctx, repo, "github", conflictRef, sha); err != nil {
		return err
	}
	return r.pushSHA(ctx, repo, "forgejo", conflictRef, sha)
}

func (r *Runner) fastForwardOnly(ctx context.Context, repo *gitops.Repo, gh, fj, ref string) error {
	if gh == "" {
		return r.pushSHA(ctx, repo, "github", ref, fj)
	}
	if fj == "" {
		return r.pushSHA(ctx, repo, "forgejo", ref, gh)
	}
	ghAncFj, err := repo.IsAncestor(ctx, gh, fj)
	if err != nil {
		return err
	}
	fjAncGh, err := repo.IsAncestor(ctx, fj, gh)
	if err != nil {
		return err
	}
	if fjAncGh {
		return r.pushSHA(ctx, repo, "forgejo", ref, gh)
	}
	if ghAncFj {
		return r.pushSHA(ctx, repo, "github", ref, fj)
	}
	r.Log.Info("skip diverged tag or conflict-ref", "ref", ref)
	return nil
}

func (r *Runner) pushBothRemember(ctx context.Context, repo *gitops.Repo, lagging, ref, sha string) error {
	return r.pushSHA(ctx, repo, lagging, ref, sha)
}

func (r *Runner) ensureConflictOnBoth(ctx context.Context, repo *gitops.Repo, ref, sha string) error {
	if err := r.pushSHA(ctx, repo, "github", ref, sha); err != nil {
		return err
	}
	return r.pushSHA(ctx, repo, "forgejo", ref, sha)
}

func (r *Runner) pushSHA(ctx context.Context, repo *gitops.Repo, remote, ref, sha string) error {
	if sha == "" {
		return nil
	}
	if err := repo.Push(ctx, remote, ref, sha); err != nil {
		return fmt.Errorf("push %s %s: %w", remote, ref, err)
	}
	return r.DB.RememberSHA(sha)
}

func sameParents(parents []string, a, b string) bool {
	if len(parents) != 2 {
		return false
	}
	return (parents[0] == a && parents[1] == b) || (parents[0] == b && parents[1] == a)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
