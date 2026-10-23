//go:build !windows

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStateAccessSeparatesLookupFromCreation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "state")
	t.Setenv("KMX_HOME", home)
	if _, err := ExistingStateDir(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent ExistingStateDir = %v", err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only lookup created state: %v", err)
	}
	if got, err := PrepareStateDir(); err != nil || got != home {
		t.Fatalf("PrepareStateDir = %q, %v", got, err)
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("prepared state = %v, %v", info, err)
	}
	if got, err := ExistingStateDir(); err != nil || got != home {
		t.Fatalf("existing state = %q, %v", got, err)
	}
}

func TestStateAccessRefusesUnsafeExistingRootWithoutRepair(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "state")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	for _, resolve := range []func() (string, error){ExistingStateDir, PrepareStateDir} {
		if _, err := resolve(); err == nil {
			t.Fatal("unsafe state root accepted")
		}
	}
	info, err := os.Stat(home)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("unsafe state root was repaired: %v, %v", info, err)
	}
}

func TestStateAccessHonorsCancellationBeforeEffects(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "state")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareStateDir(ctx, home); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled state creation = %v", err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled state creation had effects: %v", err)
	}
}
