package gitrec

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reposync/internal/config"
	"reposync/internal/store"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
	return string(out)
}

func TestSyncPairWritesConflictBranch(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, src, "init", "-b", "main")
	git(t, src, "config", "user.email", "t@t")
	git(t, src, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, src, "add", "f")
	git(t, src, "commit", "-m", "base")

	gh := filepath.Join(root, "github", "alice", "demo.git")
	fj := filepath.Join(root, "forgejo", "alice", "demo.git")
	if err := os.MkdirAll(filepath.Dir(gh), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fj), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, src, "clone", "--bare", src, gh)
	git(t, src, "clone", "--bare", src, fj)

	wgh := filepath.Join(root, "wgh")
	git(t, root, "clone", gh, wgh)
	git(t, wgh, "config", "user.email", "t@t")
	git(t, wgh, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(wgh, "f"), []byte("github\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wgh, "commit", "-am", "gh")
	git(t, wgh, "push")

	wfj := filepath.Join(root, "wfj")
	git(t, root, "clone", fj, wfj)
	git(t, wfj, "config", "user.email", "t@t")
	git(t, wfj, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(wfj, "f"), []byte("forgejo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wfj, "commit", "-am", "fj")
	git(t, wfj, "push")

	db, err := store.Open(filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{
		StateRoot:  filepath.Join(root, "state"),
		GitTimeout: 30 * time.Second,
		Bot:        config.Bot{Name: "reposync", Email: "reposync@localhost"},
		GitHub:     config.GitHub{Git: "file://" + filepath.Join(root, "github")},
		Forgejo:    config.Forgejo{Git: "file://" + filepath.Join(root, "forgejo")},
	}
	r := &Runner{Cfg: cfg, DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p := store.Pair{GitHubOwner: "alice", GitHubName: "demo", ForgejoOwner: "alice", ForgejoName: "demo"}
	if err := r.SyncPair(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	out := git(t, gh, "show-ref")
	if !strings.Contains(out, "refs/heads/reposync/conflict/main") {
		t.Fatalf("missing conflict ref:\n%s", out)
	}
	sha := strings.Fields(git(t, gh, "rev-parse", "refs/heads/reposync/conflict/main"))[0]
	blob := git(t, gh, "show", sha+":f")
	if !strings.Contains(blob, "<<<<<<<") {
		t.Fatalf("missing markers:\n%s", blob)
	}
	main := git(t, gh, "rev-parse", "refs/heads/main")
	if strings.TrimSpace(main) == strings.TrimSpace(sha) {
		t.Fatal("should not have moved main")
	}
}
