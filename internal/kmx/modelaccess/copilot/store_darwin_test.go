package copilot

import (
	"errors"
	"os"
	"testing"
)

// Native ephemeral macOS CI only: no writes, no reads of other applications'
// items, and never enabled by default on a developer's signed-in keychain.
func TestDarwinReleaseKeychainMissingOwnedItem(t *testing.T) {
	if os.Getenv("CI") != "true" || os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("read-only characterization requires ephemeral GitHub Actions macOS")
	}
	token, err := New(nil).store.Get()
	if token != "" || !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("missing owned native keychain item: want ErrAuthRequired, got %v", err)
	}
}
