package single

import (
	"testing"
)

func TestLockExclusive(t *testing.T) {
	dir := t.TempDir()
	a, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := Acquire(dir); err == nil {
		t.Fatal("expected second lock to fail")
	}
}
