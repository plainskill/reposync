package single

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock is an exclusive flock on state_root/reposync.lock so two
// processes cannot share a worktree or sqlite journal.
type Lock struct {
	f *os.File
}

func Acquire(stateRoot string) (*Lock, error) {
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateRoot, "reposync.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another reposync already holds %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}
