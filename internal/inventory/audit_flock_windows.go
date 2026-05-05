//go:build windows

// audit_flock_windows.go is the Windows shim for FileLogger's append +
// rotate critical section. Windows lacks flock; the equivalent is
// LockFileEx with LOCKFILE_EXCLUSIVE_LOCK over a sufficiently large byte
// range, which we approximate with the maximum 64-bit range so the lock
// covers the whole file regardless of size.
//
// LockFileEx semantics differ from flock in one important way: the lock is
// per-handle, not per-open-file-description, so we must call UnlockFileEx
// before closing the handle to keep the protocol clean. Closing the
// handle does release the lock, but doing the explicit unlock first
// avoids any platform-specific edge cases around delayed lock release.

package inventory

import (
	"os"

	"golang.org/x/sys/windows"
)

const (
	// Lock the entire file — high 32 bits = MaxUint32, low = MaxUint32.
	// LockFileEx interprets these as a 64-bit byte range; this covers the
	// largest representable file size, which is more than enough.
	lockBytesLow  uint32 = 0xFFFFFFFF
	lockBytesHigh uint32 = 0xFFFFFFFF
)

// lockFile acquires an exclusive lock on f. Blocks until the lock is
// available; returns the underlying syscall error on failure.
func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, // reserved, must be 0
		lockBytesLow,
		lockBytesHigh,
		ol,
	)
}

// unlockFile releases the lock held on f. Errors are best-effort — the
// handle is about to close, which also releases the lock.
func unlockFile(f *os.File) {
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0, // reserved
		lockBytesLow,
		lockBytesHigh,
		ol,
	)
}
