package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadExpandsEnvAndDefaults(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "gtok")
	t.Setenv("GITHUB_HOOK_SECRET", "ghsec")
	t.Setenv("FORGEJO_TOKEN", "ftok")
	t.Setenv("FJ_HOOK_SECRET", "fjsec")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
state_root: ` + filepath.Join(dir, "state") + `
sqlite: ` + filepath.Join(dir, "reposync.db") + `
github:
  token: ${GITHUB_TOKEN}
  webhook_secret: ${GITHUB_HOOK_SECRET}
forgejo:
  api: https://gt.example
  git: ssh://git@127.0.0.1:2222
  token: ${FORGEJO_TOKEN}
  webhook_secret: ${FJ_HOOK_SECRET}
owners:
  - github: alice
    forgejo: alice
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:7745" {
		t.Fatalf("listen: %s", cfg.Listen)
	}
	if cfg.ReconcileEvery != 5*time.Minute {
		t.Fatalf("reconcile: %s", cfg.ReconcileEvery)
	}
	if cfg.GitHub.Token != "gtok" || cfg.Forgejo.Token != "ftok" {
		t.Fatal("tokens not expanded")
	}
	if cfg.GitTimeout != 60*time.Second {
		t.Fatalf("git timeout: %s", cfg.GitTimeout)
	}
	u := cfg.ForgejoGitURL("alice", "demo")
	if u != "ssh://git@127.0.0.1:2222/alice/demo.git" {
		t.Fatalf("git url %s", u)
	}
}

func TestIsBot(t *testing.T) {
	c := &Config{Bot: Bot{Name: "reposync", GitHub: "reposync[bot]", Forgejo: "reposync"}}
	if !c.IsBot("reposync") || !c.IsBot("RepoSync[bot]") {
		t.Fatal("expected bot match")
	}
	if c.IsBot("alice") || c.IsBot("") {
		t.Fatal("unexpected bot match")
	}
}

func TestRejectsEmptySecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	_ = os.WriteFile(path, []byte(`
state_root: /tmp
sqlite: /tmp/x.db
github:
  token: t
  webhook_secret: ""
forgejo:
  api: https://x
  git: ssh://git@x
  token: t
  webhook_secret: s
owners:
  - github: a
    forgejo: a
`), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error")
	}
}
