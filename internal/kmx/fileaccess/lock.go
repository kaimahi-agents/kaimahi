package fileaccess

import "context"

// TryLock takes a nonblocking process-held exclusive lock on file. The lock is
// released when the private file handle closes or the process exits. Existing
// callers retain the unsafe error category for contention.
func TryLock(ctx context.Context, file *PrivateFile) error {
	acquired, err := tryLock(ctx, file)
	if err == nil && !acquired {
		err = unsafeCause("private file is already locked")
	}
	return wrapAccess("lock private file", err)
}

// TryLockOutcome distinguishes actual local contention (false, nil) from an
// acquired lock (true, nil) and all invalid, cancelled or unreadable outcomes.
// Closing file releases a successful lock; contention grants no retry authority.
func TryLockOutcome(ctx context.Context, file *PrivateFile) (bool, error) {
	acquired, err := tryLock(ctx, file)
	return acquired, wrapAccess("lock private file", err)
}
