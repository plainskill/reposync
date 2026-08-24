package gitops

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
	return string(out)
}

func initSrc(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.email", "t@t")
	git(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "f")
	git(t, dir, "commit", "-m", "base")
}

func TestFastForwardAndConflictMarkers(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	initSrc(t, src)
	gh := filepath.Join(root, "gh.git")
	fj := filepath.Join(root, "fj.git")
	git(t, src, "clone", "--bare", src, gh)
	git(t, src, "clone", "--bare", src, fj)

	workGH := filepath.Join(root, "wgh")
	git(t, root, "clone", gh, workGH)
	git(t, workGH, "config", "user.email", "t@t")
	git(t, workGH, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(workGH, "f"), []byte("github\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, workGH, "commit", "-am", "gh")
	git(t, workGH, "push", "origin", "main")

	hub := filepath.Join(root, "hub.git")
	wt := filepath.Join(root, "wt")
	r := New(hub, wt, "reposync", "reposync@localhost")
	r.Timeout = 30 * time.Second
	ctx := context.Background()
	if err := r.Ensure(ctx, map[string]string{"github": gh, "forgejo": fj}); err != nil {
		t.Fatal(err)
	}
	if err := r.FetchHeadsAndTags(ctx, "github"); err != nil {
		t.Fatal(err)
	}
	if err := r.FetchHeadsAndTags(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	ghSHA, _ := r.TrackingSHA(ctx, "github", "heads", "main")
	fjSHA, _ := r.TrackingSHA(ctx, "forgejo", "heads", "main")
	anc, err := r.IsAncestor(ctx, fjSHA, ghSHA)
	if err != nil || !anc {
		t.Fatalf("expected ff ancestor fj->gh: %v %v", anc, err)
	}

	workFJ := filepath.Join(root, "wfj")
	git(t, root, "clone", fj, workFJ)
	git(t, workFJ, "config", "user.email", "t@t")
	git(t, workFJ, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(workFJ, "f"), []byte("forgejo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, workFJ, "commit", "-am", "fj")
	git(t, workFJ, "push", "origin", "main")

	if err := r.FetchHeadsAndTags(ctx, "github"); err != nil {
		t.Fatal(err)
	}
	if err := r.FetchHeadsAndTags(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	ghSHA, _ = r.TrackingSHA(ctx, "github", "heads", "main")
	fjSHA, _ = r.TrackingSHA(ctx, "forgejo", "heads", "main")
	sha, conflict, err := r.Merge(ctx, ghSHA, fjSHA, "reposync conflict", true)
	if err != nil {
		t.Fatal(err)
	}
	if !conflict {
		t.Fatal("expected conflict")
	}
	if sha == "" {
		t.Fatal("expected sha")
	}
	if err := r.Push(ctx, "github", "refs/heads/reposync/conflict/main", sha); err != nil {
		t.Fatal(err)
	}
	show := git(t, gh, "show", sha+":f")
	if !bytes.Contains([]byte(show), []byte("<<<<<<<")) || !bytes.Contains([]byte(show), []byte(">>>>>>>")) {
		t.Fatalf("missing markers:\n%s", show)
	}
	parents, err := r.Parents(ctx, sha)
	if err != nil || len(parents) != 2 {
		t.Fatalf("parents %v %v", parents, err)
	}
}

func TestCleanMerge(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	initSrc(t, src)
	gh := filepath.Join(root, "gh.git")
	fj := filepath.Join(root, "fj.git")
	git(t, src, "clone", "--bare", src, gh)
	git(t, src, "clone", "--bare", src, fj)

	workGH := filepath.Join(root, "wgh")
	git(t, root, "clone", gh, workGH)
	git(t, workGH, "config", "user.email", "t@t")
	git(t, workGH, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(workGH, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, workGH, "add", "a")
	git(t, workGH, "commit", "-m", "a")
	git(t, workGH, "push", "origin", "main")

	workFJ := filepath.Join(root, "wfj")
	git(t, root, "clone", fj, workFJ)
	git(t, workFJ, "config", "user.email", "t@t")
	git(t, workFJ, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(workFJ, "b"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, workFJ, "add", "b")
	git(t, workFJ, "commit", "-m", "b")
	git(t, workFJ, "push", "origin", "main")

	hub := filepath.Join(root, "hub.git")
	r := New(hub, filepath.Join(root, "wt"), "reposync", "reposync@localhost")
	ctx := context.Background()
	if err := r.Ensure(ctx, map[string]string{"github": gh, "forgejo": fj}); err != nil {
		t.Fatal(err)
	}
	_ = r.FetchHeadsAndTags(ctx, "github")
	_ = r.FetchHeadsAndTags(ctx, "forgejo")
	ghSHA, _ := r.TrackingSHA(ctx, "github", "heads", "main")
	fjSHA, _ := r.TrackingSHA(ctx, "forgejo", "heads", "main")
	sha, conflict, err := r.Merge(ctx, ghSHA, fjSHA, "merge", true)
	if err != nil || conflict || sha == "" {
		t.Fatalf("merge: sha=%s conflict=%v err=%v", sha, conflict, err)
	}
}
