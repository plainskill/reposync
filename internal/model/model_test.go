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
