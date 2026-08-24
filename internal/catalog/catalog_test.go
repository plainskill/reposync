package catalog

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"reposync/internal/config"
	"reposync/internal/model"
	"reposync/internal/store"
)

type fakeForge struct {
	repos   map[string]model.Listed
	created []string
	updated []string
	renames []string
}

func newFake() *fakeForge {
	return &fakeForge{repos: map[string]model.Listed{}}
}

func (f *fakeForge) List(_ context.Context, owner string) ([]model.Listed, error) {
	var out []model.Listed
	for _, r := range f.repos {
		if r.Owner == owner {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeForge) Get(_ context.Context, owner, name string) (*model.Listed, error) {
	r, ok := f.repos[owner+"/"+name]
	if !ok {
		return nil, errNF{}
	}
	cp := r
	return &cp, nil
}

func (f *fakeForge) Create(_ context.Context, owner, name string, meta model.Meta) (*model.Listed, error) {
	item := model.Listed{ID: int64(len(f.repos) + 10), Owner: owner, Name: name, Meta: meta}
	f.repos[owner+"/"+name] = item
	f.created = append(f.created, owner+"/"+name)
	return &item, nil
}

func (f *fakeForge) Update(_ context.Context, owner, name string, meta model.Meta) error {
	r := f.repos[owner+"/"+name]
	r.Meta = meta
	f.repos[owner+"/"+name] = r
	f.updated = append(f.updated, owner+"/"+name)
	return nil
}

func (f *fakeForge) Rename(_ context.Context, owner, name, newName string) error {
	r := f.repos[owner+"/"+name]
	delete(f.repos, owner+"/"+name)
	r.Name = newName
	f.repos[owner+"/"+newName] = r
	f.renames = append(f.renames, name+"->"+newName)
	return nil
}

type errNF struct{}

func (errNF) Error() string { return "not found" }

func testRunner(t *testing.T, gh, fj *fakeForge) *Runner {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{
		Owners: []config.OwnerMap{{GitHub: "alice", Forgejo: "alice"}},
	}
	return &Runner{
		Cfg: cfg, DB: db, GitHub: gh, Forgejo: fj,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		GHNF: func(err error) bool { _, ok := err.(errNF); return ok },
		FJNF: func(err error) bool { _, ok := err.(errNF); return ok },
	}
}

func TestCreateMissingForgejo(t *testing.T) {
	gh, fj := newFake(), newFake()
	gh.repos["alice/demo"] = model.Listed{ID: 1, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "hi", UpdatedAt: time.Now()}}
	r := testRunner(t, gh, fj)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fj.created) != 1 || fj.created[0] != "alice/demo" {
		t.Fatalf("created: %v", fj.created)
	}
}

func TestLastWriteWins(t *testing.T) {
	gh, fj := newFake(), newFake()
	old := time.Now().Add(-time.Hour)
	now := time.Now()
	gh.repos["alice/demo"] = model.Listed{ID: 1, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "gh", UpdatedAt: old}}
	fj.repos["alice/demo"] = model.Listed{ID: 2, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "fj", UpdatedAt: now}}
	r := testRunner(t, gh, fj)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(gh.updated) != 1 {
		t.Fatalf("expected github update, got %v fj=%v", gh.updated, fj.updated)
	}
	if gh.repos["alice/demo"].Meta.Description != "fj" {
		t.Fatalf("desc %q", gh.repos["alice/demo"].Meta.Description)
	}
}

func TestCreateMissingGitHub(t *testing.T) {
	gh, fj := newFake(), newFake()
	fj.repos["alice/demo"] = model.Listed{ID: 2, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "from fj", UpdatedAt: time.Now()}}
	r := testRunner(t, gh, fj)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(gh.created) != 1 || gh.created[0] != "alice/demo" {
		t.Fatalf("created: %v", gh.created)
	}
	if fj.repos["alice/demo"].Meta.Archived {
		t.Fatal("should not archive forgejo when github was empty")
	}
}

func TestStalePairCreatesInsteadOfArchive(t *testing.T) {
	gh, fj := newFake(), newFake()
	fj.repos["alice/demo"] = model.Listed{ID: 2, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "keep", UpdatedAt: time.Now()}}
	r := testRunner(t, gh, fj)
	if _, err := r.DB.UpsertPair(store.Pair{
		GitHubID: 1, ForgejoID: 2,
		GitHubOwner: "alice", GitHubName: "demo",
		ForgejoOwner: "alice", ForgejoName: "demo",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fj.repos["alice/demo"].Meta.Archived {
		t.Fatal("stale unestablished pair must not archive")
	}
	if len(gh.created) != 1 {
		t.Fatalf("expected github create, got %v", gh.created)
	}
}

func TestPeerGoneArchivesNotRecreate(t *testing.T) {
	gh, fj := newFake(), newFake()
	gh.repos["alice/demo"] = model.Listed{ID: 1, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "x", UpdatedAt: time.Now()}}
	fj.repos["alice/demo"] = model.Listed{ID: 2, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "x", UpdatedAt: time.Now()}}
	r := testRunner(t, gh, fj)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	delete(gh.repos, "alice/demo")
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fj.repos["alice/demo"].Meta.Archived {
		t.Fatal("expected forgejo archived")
	}
	gh.repos["alice/demo"] = model.Listed{ID: 1, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "back", UpdatedAt: time.Now()}}
	fj.created = nil
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fj.created) != 0 {
		t.Fatalf("should not recreate: %v", fj.created)
	}
}

func TestArchiveWhenPeerArchived(t *testing.T) {
	gh, fj := newFake(), newFake()
	now := time.Now()
	gh.repos["alice/demo"] = model.Listed{ID: 1, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "x", UpdatedAt: now}}
	fj.repos["alice/demo"] = model.Listed{ID: 2, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "x", UpdatedAt: now}}
	r := testRunner(t, gh, fj)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	g := gh.repos["alice/demo"]
	g.Meta.Archived = true
	g.Meta.UpdatedAt = now.Add(time.Minute)
	gh.repos["alice/demo"] = g
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fj.repos["alice/demo"].Meta.Archived {
		t.Fatal("expected forgejo archived after github archive")
	}
	g = gh.repos["alice/demo"]
	g.Meta.Archived = false
	g.Meta.UpdatedAt = now.Add(2 * time.Minute)
	gh.repos["alice/demo"] = g
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fj.repos["alice/demo"].Meta.Archived {
		t.Fatal("unarchive on github should unarchive forgejo")
	}
}

func TestSkipOrgRepos(t *testing.T) {
	gh, fj := newFake(), newFake()
	gh.repos["alice/orgish"] = model.Listed{ID: 9, Owner: "alice", Name: "orgish", Org: true, Meta: model.Meta{UpdatedAt: time.Now()}}
	gh.repos["alice/demo"] = model.Listed{ID: 1, Owner: "alice", Name: "demo", Meta: model.Meta{Description: "hi", UpdatedAt: time.Now()}}
	r := testRunner(t, gh, fj)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fj.created) != 1 || fj.created[0] != "alice/demo" {
		t.Fatalf("created: %v", fj.created)
	}
}
