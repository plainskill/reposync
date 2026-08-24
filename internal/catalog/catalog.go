package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"reposync/internal/config"
	"reposync/internal/model"
	"reposync/internal/store"
)

type Forge interface {
	List(ctx context.Context, owner string) ([]model.Listed, error)
	Get(ctx context.Context, owner, name string) (*model.Listed, error)
	Create(ctx context.Context, owner, name string, meta model.Meta) (*model.Listed, error)
	Update(ctx context.Context, owner, name string, meta model.Meta) error
	Rename(ctx context.Context, owner, name, newName string) error
}

type Runner struct {
	Cfg     *config.Config
	DB      *store.DB
	GitHub  Forge
	Forgejo Forge
	Log     *slog.Logger
	GHNF    func(error) bool
	FJNF    func(error) bool
}

func (r *Runner) ReconcileAll(ctx context.Context) error {
	ghByKey := map[string]model.Listed{}
	fjByKey := map[string]model.Listed{}
	for _, o := range r.Cfg.Owners {
		listed, err := r.GitHub.List(ctx, o.GitHub)
		if err != nil {
			return fmt.Errorf("github list %s: %w", o.GitHub, err)
		}
		for _, item := range listed {
			if r.skip(item, o.GitHub+"/"+item.Name) {
				continue
			}
			ghByKey[key(o.GitHub, item.Name)] = item
		}
		listed, err = r.Forgejo.List(ctx, o.Forgejo)
		if err != nil {
			return fmt.Errorf("forgejo list %s: %w", o.Forgejo, err)
		}
		for _, item := range listed {
			if r.skip(item, o.GitHub+"/"+item.Name) {
				continue
			}
			fjByKey[key(o.Forgejo, item.Name)] = item
		}
	}
	seen := map[string]struct{}{}
	for k, gh := range ghByKey {
		fjOwner, ok := r.Cfg.ForgejoOwner(gh.Owner)
		if !ok {
			continue
		}
		fj, fjOK := fjByKey[key(fjOwner, gh.Name)]
		if err := r.pairSides(ctx, gh, fj, fjOK); err != nil {
			return err
		}
		seen[k] = struct{}{}
		if fjOK {
			seen[key(fjOwner, gh.Name)] = struct{}{}
		}
	}
	for _, fj := range fjByKey {
		ghOwner, ok := r.Cfg.GitHubOwner(fj.Owner)
		if !ok {
			continue
		}
		if _, already := ghByKey[key(ghOwner, fj.Name)]; already {
			continue
		}
		if err := r.pairSides(ctx, model.Listed{}, fj, true); err != nil {
			return err
		}
	}
	return r.detectGone(ctx, ghByKey, fjByKey)
}

func (r *Runner) ReconcileOne(ctx context.Context, ghOwner, ghName, fjOwner, fjName string) error {
	var gh, fj model.Listed
	var haveGH, haveFJ bool
	if ghOwner != "" && ghName != "" {
		got, err := r.GitHub.Get(ctx, ghOwner, ghName)
		if err != nil && !r.GHNF(err) {
			return err
		}
		if err == nil {
			gh, haveGH = *got, true
		}
	}
	if fjOwner != "" && fjName != "" {
		got, err := r.Forgejo.Get(ctx, fjOwner, fjName)
		if err != nil && !r.FJNF(err) {
			return err
		}
		if err == nil {
			fj, haveFJ = *got, true
		}
	}
	if haveGH && r.skip(gh, gh.Owner+"/"+gh.Name) {
		return nil
	}
	if haveFJ && r.skip(fj, mapName(ghOwner, fj.Name)) {
		return nil
	}
	if haveGH {
		return r.pairSides(ctx, gh, fj, haveFJ)
	}
	if haveFJ {
		return r.pairSides(ctx, model.Listed{}, fj, true)
	}
	p, err := r.lookupPair(0, 0, ghOwner, ghName, fjOwner, fjName)
	if err != nil || p == nil || p.PeerGone {
		return err
	}
	return r.DB.MarkPeerGone(p.ID, "both")
}

func (r *Runner) skip(item model.Listed, ghFull string) bool {
	if r.Cfg.Excluded(ghFull) {
		return true
	}
	if item.Fork && !r.Cfg.IncludeForks {
		return true
	}
	if item.Mirror {
		return true
	}
	return false
}

func (r *Runner) pairSides(ctx context.Context, gh model.Listed, fj model.Listed, haveFJ bool) error {
	haveGH := gh.Name != ""
	if !haveGH && haveFJ {
		ghOwner, ok := r.Cfg.GitHubOwner(fj.Owner)
		if !ok {
			return nil
		}
		p, err := r.lookupPair(0, fj.ID, ghOwner, fj.Name, fj.Owner, fj.Name)
		if err != nil {
			return err
		}
		if p != nil && p.PeerGone {
			r.Log.Info("skip create; peer_gone", "forgejo", fj.Owner+"/"+fj.Name)
			return nil
		}
		if p != nil {
			return r.archiveRemaining(ctx, p, "github", fj, model.Listed{})
		}
		meta := fj.Meta
		if fj.Empty {
			meta.DefaultBranch = ""
		}
		created, err := r.GitHub.Create(ctx, ghOwner, fj.Name, meta)
		if err != nil {
			return fmt.Errorf("create github %s/%s: %w", ghOwner, fj.Name, err)
		}
		_, err = r.DB.UpsertPair(store.Pair{
			GitHubID: created.ID, ForgejoID: fj.ID,
			GitHubOwner: created.Owner, GitHubName: created.Name,
			ForgejoOwner: fj.Owner, ForgejoName: fj.Name,
		})
		return err
	}
	fjOwner, ok := r.Cfg.ForgejoOwner(gh.Owner)
	if !ok {
		return nil
	}
	p, err := r.lookupPair(gh.ID, fj.ID, gh.Owner, gh.Name, fjOwner, gh.Name)
	if err != nil {
		return err
	}
	if p != nil && p.PeerGone {
		r.Log.Info("skip create; peer_gone", "github", gh.Owner+"/"+gh.Name)
		return nil
	}
	if !haveFJ {
		if p != nil {
			return r.archiveRemaining(ctx, p, "forgejo", model.Listed{}, gh)
		}
		meta := gh.Meta
		if gh.Empty {
			meta.DefaultBranch = ""
		}
		created, err := r.Forgejo.Create(ctx, fjOwner, gh.Name, meta)
		if err != nil {
			return fmt.Errorf("create forgejo %s/%s: %w", fjOwner, gh.Name, err)
		}
		_, err = r.DB.UpsertPair(store.Pair{
			GitHubID: gh.ID, ForgejoID: created.ID,
			GitHubOwner: gh.Owner, GitHubName: gh.Name,
			ForgejoOwner: created.Owner, ForgejoName: created.Name,
		})
		return err
	}
	if p != nil {
		if p.GitHubName != gh.Name {
			if err := r.Forgejo.Rename(ctx, p.ForgejoOwner, p.ForgejoName, gh.Name); err != nil {
				return err
			}
			fj.Name = gh.Name
		}
		if p.ForgejoName != fj.Name && p.GitHubName == gh.Name {
			if err := r.GitHub.Rename(ctx, p.GitHubOwner, p.GitHubName, fj.Name); err != nil {
				return err
			}
			gh.Name = fj.Name
		}
	}
	if err := r.syncMeta(ctx, gh, fj); err != nil {
		return err
	}
	_, err = r.DB.UpsertPair(store.Pair{
		GitHubID: gh.ID, ForgejoID: fj.ID,
		GitHubOwner: gh.Owner, GitHubName: gh.Name,
		ForgejoOwner: fj.Owner, ForgejoName: fj.Name,
	})
	return err
}

func (r *Runner) archiveRemaining(ctx context.Context, p *store.Pair, goneSide string, fj, gh model.Listed) error {
	r.Log.Info("peer gone; archive remaining", "gone", goneSide, "github", p.GitHubOwner+"/"+p.GitHubName)
	if goneSide == "github" {
		meta := fj.Meta
		meta.Archived = true
		if err := r.Forgejo.Update(ctx, p.ForgejoOwner, p.ForgejoName, meta); err != nil {
			return err
		}
		return r.DB.MarkPeerGone(p.ID, "github")
	}
	meta := gh.Meta
	meta.Archived = true
	if err := r.GitHub.Update(ctx, p.GitHubOwner, p.GitHubName, meta); err != nil {
		return err
	}
	return r.DB.MarkPeerGone(p.ID, "forgejo")
}

func (r *Runner) syncMeta(ctx context.Context, gh, fj model.Listed) error {
	if gh.Empty || fj.Empty {
		gh.Meta.DefaultBranch = ""
		fj.Meta.DefaultBranch = ""
	}
	if gh.Meta.Equal(fj.Meta) {
		return nil
	}
	win, side := model.Winner(gh.Meta, fj.Meta)
	if side == "github" && !win.Equal(fj.Meta) {
		r.Log.Info("metadata github -> forgejo", "repo", gh.Owner+"/"+gh.Name)
		return r.Forgejo.Update(ctx, fj.Owner, fj.Name, win)
	}
	if side == "forgejo" && !win.Equal(gh.Meta) {
		r.Log.Info("metadata forgejo -> github", "repo", gh.Owner+"/"+gh.Name)
		return r.GitHub.Update(ctx, gh.Owner, gh.Name, win)
	}
	return nil
}

func (r *Runner) lookupPair(ghID, fjID int64, ghOwner, ghName, fjOwner, fjName string) (*store.Pair, error) {
	if ghID != 0 {
		if p, err := r.DB.PairByGitHubID(ghID); err != nil || p != nil {
			return p, err
		}
	}
	if fjID != 0 {
		if p, err := r.DB.PairByForgejoID(fjID); err != nil || p != nil {
			return p, err
		}
	}
	if p, err := r.DB.PairByGitHub(ghOwner, ghName); err != nil || p != nil {
		return p, err
	}
	return r.DB.PairByForgejo(fjOwner, fjName)
}

func (r *Runner) detectGone(ctx context.Context, gh map[string]model.Listed, fj map[string]model.Listed) error {
	pairs, err := r.DB.ListPairs()
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if p.PeerGone {
			continue
		}
		_, haveGH := gh[key(p.GitHubOwner, p.GitHubName)]
		_, haveFJ := fj[key(p.ForgejoOwner, p.ForgejoName)]
		if haveGH && haveFJ {
			continue
		}
		if !haveGH && haveFJ {
			r.Log.Info("github gone; archive forgejo", "repo", p.ForgejoOwner+"/"+p.ForgejoName)
			listed := fj[key(p.ForgejoOwner, p.ForgejoName)]
			meta := listed.Meta
			meta.Archived = true
			if err := r.Forgejo.Update(ctx, p.ForgejoOwner, p.ForgejoName, meta); err != nil {
				return err
			}
			if err := r.DB.MarkPeerGone(p.ID, "github"); err != nil {
				return err
			}
			continue
		}
		if haveGH && !haveFJ {
			r.Log.Info("forgejo gone; archive github", "repo", p.GitHubOwner+"/"+p.GitHubName)
			listed := gh[key(p.GitHubOwner, p.GitHubName)]
			meta := listed.Meta
			meta.Archived = true
			if err := r.GitHub.Update(ctx, p.GitHubOwner, p.GitHubName, meta); err != nil {
				return err
			}
			if err := r.DB.MarkPeerGone(p.ID, "forgejo"); err != nil {
				return err
			}
		}
	}
	return nil
}

func key(owner, name string) string {
	return strings.ToLower(owner) + "/" + strings.ToLower(name)
}

func mapName(owner, name string) string {
	return owner + "/" + name
}
