package model

import (
	"testing"
	"time"
)

func TestWinner(t *testing.T) {
	old := time.Unix(100, 0)
	nw := time.Unix(200, 0)
	a := Meta{Description: "a", UpdatedAt: old}
	b := Meta{Description: "b", UpdatedAt: nw}
	w, side := Winner(a, b)
	if side != "forgejo" || w.Description != "b" {
		t.Fatalf("%s %#v", side, w)
	}
	w, side = Winner(b, a)
	if side != "github" || w.Description != "b" {
		t.Fatalf("%s %#v", side, w)
	}
}

func TestConflictRef(t *testing.T) {
	if ConflictRef("main") != "refs/heads/reposync/conflict/main" {
		t.Fatal(ConflictRef("main"))
	}
	if !IsConflictRef("refs/heads/reposync/conflict/main") {
		t.Fatal("expected conflict ref")
	}
}

func TestReconcileMetaUnarchive(t *testing.T) {
	snap := Meta{Description: "x", Archived: true}
	gh := Meta{Description: "x", Archived: false, UpdatedAt: time.Unix(3, 0)}
	fj := Meta{Description: "x", Archived: true, UpdatedAt: time.Unix(1, 0)}
	apply, writeGH, writeFJ := ReconcileMeta(snap, true, gh, fj)
	if writeGH || !writeFJ || apply.Archived {
		t.Fatalf("writeGH=%v writeFJ=%v archived=%v", writeGH, writeFJ, apply.Archived)
	}
}

func TestReconcileMetaIndependentFields(t *testing.T) {
	snap := Meta{Description: "old", Homepage: "http://a", Archived: false, Private: false, DefaultBranch: "main", Topics: []string{"x"}}
	gh := Meta{Description: "new", Homepage: "http://a", Archived: false, Private: false, DefaultBranch: "main", Topics: []string{"x"}, UpdatedAt: time.Unix(2, 0)}
	fj := Meta{Description: "old", Homepage: "http://a", Archived: true, Private: true, DefaultBranch: "dev", Topics: []string{"x", "y"}, UpdatedAt: time.Unix(1, 0)}
	apply, writeGH, writeFJ := ReconcileMeta(snap, true, gh, fj)
	if !writeGH || !writeFJ {
		t.Fatalf("expected both writes gh=%v fj=%v", writeGH, writeFJ)
	}
	if apply.Description != "new" || !apply.Archived || !apply.Private || apply.DefaultBranch != "dev" {
		t.Fatalf("%#v", apply)
	}
	if !topicsEqual(apply.Topics, []string{"x", "y"}) {
		t.Fatalf("topics %v", apply.Topics)
	}
}
