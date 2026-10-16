package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseLiftStoragePreflightExistingLockReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary filesystem permission enforcement")
	}
	t.Setenv("KMX_HOME", t.TempDir())
	if err := preflightReleaseLiftStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(os.Getenv("KMX_HOME"), "release-lifts")
	if err := os.Chmod(base, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(base, 0700); err != nil {
			t.Error(err)
		}
	})
	if err := preflightReleaseLiftStorage(t.Context()); !errors.Is(err, errReleaseLiftRecord) {
		t.Fatalf("unwritable storage accepted before spend: %v", err)
	}
}

func TestReleaseLiftStorageWritableProbeLeavesNoAttempt(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	if err := preflightReleaseLiftStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(os.Getenv("KMX_HOME"), "release-lifts"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "lock" || entries[0].IsDir() {
		t.Fatalf("storage probe left history: %v %v", entries, err)
	}
}

func TestReleaseApplyFixtureDoesNotWriteAmbientState(t *testing.T) {
	ambient := t.TempDir()
	t.Setenv("KMX_HOME", ambient)
	a, _, opt, _ := releaseApplyFixture(t, "both")
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(ambient)
	if err != nil || len(entries) != 0 {
		t.Fatalf("shared fixture wrote caller's state: %v %v", entries, err)
	}
}
