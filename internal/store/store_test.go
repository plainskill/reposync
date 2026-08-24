package store

import (
	"path/filepath"
	"testing"
)

func TestEnqueueCoalesce(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	already, err := db.Enqueue("alice/demo")
	if err != nil || already {
		t.Fatalf("first %v %v", already, err)
	}
	already, err = db.Enqueue("alice/demo")
	if err != nil || !already {
		t.Fatalf("second %v %v", already, err)
	}
	j, err := db.NextQueued()
	if err != nil || j == nil || j.PairKey != "alice/demo" {
		t.Fatalf("job %+v %v", j, err)
	}
	j2, err := db.NextQueued()
	if err != nil || j2 != nil {
		t.Fatalf("expected empty, got %+v %v", j2, err)
	}
}

func TestPeerGone(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	id, err := db.UpsertPair(Pair{GitHubID: 1, ForgejoID: 2, GitHubOwner: "a", GitHubName: "n", ForgejoOwner: "a", ForgejoName: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkPeerGone(id, "github"); err != nil {
		t.Fatal(err)
	}
	p, err := db.PairByGitHub("a", "n")
	if err != nil || p == nil || !p.PeerGone {
		t.Fatalf("%+v %v", p, err)
	}
}
