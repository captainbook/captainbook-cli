//go:build !windows

// audit_flock_unix.go is the POSIX implementation of the file-locking
// primitives used by FileLogger. Per D36 we use flock(LOCK_EX) for the
// append-and-rotate critical section. flock locks the open file
// description, not the inode — so a rename of audit.jsonl during rotation
// does not break the lock for the holder, but a fresh open of the path
// (post-rotation) gets a separate lock on the new inode. That's exactly
// the rotation safety property we want.

package inventory

import (
	"os"
	"syscall"
)

// lockFile acquires an exclusive (writer) flock on f. Blocks until the
// lock is available; returns the underlying syscall error on failure.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the flock held on f. Errors here are best-effort —
// the file is about to be closed anyway, and close() on POSIX implicitly
// releases any held flock — but we still try the explicit unlock so the
// happy path doesn't depend on close-time behavior.
func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
