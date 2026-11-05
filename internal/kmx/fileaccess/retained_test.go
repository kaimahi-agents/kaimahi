package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTryLockOutcomePreservesLegacy(t *testing.T) {
	path, directory := privateTestDirectory(t)
	file, err := CreatePrivateFileAt(t.Context(), directory, "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	helper := startPrivateLockHelper(t, filepath.Join(path, "lock"))
	if acquired, err := TryLockOutcome(t.Context(), file); acquired || err != nil {
		t.Fatalf("contention = %v, %v", acquired, err)
	}
	if err := TryLock(t.Context(), file); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("legacy contention = %v", err)
	}
	helper.release(t)
	if acquired, err := TryLockOutcome(t.Context(), file); !acquired || err != nil {
		t.Fatalf("released lock = %v, %v", acquired, err)
	}
}

func TestTryLockOutcomeInvalidIsNotContention(t *testing.T) {
	_, directory := privateTestDirectory(t)
	closed, err := CreatePrivateFileAt(t.Context(), directory, "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	deadline, done := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer done()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		file *PrivateFile
		want error
	}{
		{"nil context", nil, nil, ErrUnsafe}, {"absent file", t.Context(), nil, ErrUnsafe}, {"closed", t.Context(), closed, ErrUnsafe},
		{"cancelled", cancelled, nil, context.Canceled}, {"deadline", deadline, nil, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if acquired, err := TryLockOutcome(tc.ctx, tc.file); acquired || !errors.Is(err, tc.want) {
				t.Fatalf("invalid lock = %v, %v", acquired, err)
			}
		})
	}
}

func TestSyncFileRequiredFailure(t *testing.T) {
	_, directory := privateTestDirectory(t)
	file, err := CreatePrivateFileAt(t.Context(), directory, "data")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := SyncFile(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	old := syncFileHandle
	injected := errors.New("private diagnostic must not escape")
	syncFileHandle = func(*os.File) error { return injected }
	t.Cleanup(func() { syncFileHandle = old })
	if err := SyncFile(t.Context(), file); !errors.Is(err, ErrUnreadable) || errors.Is(err, injected) {
		t.Fatalf("sync failure = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := SyncFile(ctx, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sync = %v", err)
	}
	for _, object := range []Object{nil, directory, (*PrivateFile)(nil)} {
		if err := SyncFile(t.Context(), object); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("non-file sync = %v", err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SyncFile(t.Context(), file); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("closed sync = %v", err)
	}
}
