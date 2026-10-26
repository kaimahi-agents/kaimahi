package localtools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

// countingOpener stands in for the qualified private-state directory opener,
// which on Windows rejects every volume except local NTFS. A source folder
// with no kmx.yaml must never reach it.
func countingOpener(calls *int) func(context.Context, string) (*fileaccess.Directory, error) {
	return func(ctx context.Context, path string) (*fileaccess.Directory, error) {
		*calls++
		return fileaccess.OpenDirectory(ctx, path)
	}
}

func TestMissingSettingsFileNeverUsesQualifiedDirectoryOpener(t *testing.T) {
	root := settingsRoot(t)
	calls := 0
	got, err := loadSettings(t.Context(), filepath.Join(root, "agent.agent.md"), []string{"ping"}, countingOpener(&calls))
	if err != nil || !reflect.DeepEqual(got, []ToolSetting{{Name: "ping", State: Enabled}}) {
		t.Fatalf("missing file: %+v %v", got, err)
	}
	if calls != 0 {
		t.Fatalf("missing kmx.yaml reached the qualified opener %d times", calls)
	}
}

func TestMissingSettingsDefaultsOnUnqualifiedSourceVolume(t *testing.T) {
	root := settingsRoot(t)
	calls := 0
	unqualified := func(context.Context, string) (*fileaccess.Directory, error) {
		calls++
		return nil, fileaccess.ErrUnsupported // The Windows ReFS/non-NTFS outcome.
	}
	file := filepath.Join(root, "agent.agent.md")
	got, err := loadSettings(t.Context(), file, []string{"ping"}, unqualified)
	if err != nil || len(got) != 1 || got[0].State != Enabled || calls != 0 {
		t.Fatalf("absent settings imposed a volume requirement: %+v %v calls=%d", got, err, calls)
	}
	if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("tools: {ping: Disabled}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(t.Context(), file, []string{"ping"}, unqualified); !errors.Is(err, ErrSettings) || calls != 1 {
		t.Fatalf("present settings bypassed qualification: %v calls=%d", err, calls)
	}
}

func TestExistingSettingsFileKeepsQualifiedDirectoryOpener(t *testing.T) {
	root := settingsRoot(t)
	if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("tools: {ping: Disabled}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	got, err := loadSettings(t.Context(), filepath.Join(root, "agent.agent.md"), []string{"ping"}, countingOpener(&calls))
	if err != nil || calls != 1 || !reflect.DeepEqual(got, []ToolSetting{{Name: "ping", State: Disabled}}) {
		t.Fatalf("existing file: %+v %v calls=%d", got, err, calls)
	}
}

func TestMissingSettingsFileHonorsCancellationAndParentChecks(t *testing.T) {
	root := settingsRoot(t)
	calls := 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadSettings(ctx, filepath.Join(root, "agent.agent.md"), []string{"ping"}, countingOpener(&calls)); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancelled missing-file read: %v calls=%d", err, calls)
	}
	// A missing source folder is not an absent optional file; it still fails closed.
	if _, err := loadSettings(t.Context(), filepath.Join(root, "gone", "agent.agent.md"), []string{"ping"}, countingOpener(&calls)); !errors.Is(err, ErrSettings) {
		t.Fatalf("missing folder accepted: %v", err)
	}
}

func TestDanglingSettingsSymlinkIsNotAbsent(t *testing.T) {
	root := settingsRoot(t)
	if err := os.Symlink(filepath.Join(root, "nowhere.yaml"), filepath.Join(root, "kmx.yaml")); err != nil {
		t.Skip("symlinks unavailable")
	}
	calls := 0
	if _, err := loadSettings(t.Context(), filepath.Join(root, "agent.agent.md"), []string{"ping"}, countingOpener(&calls)); !errors.Is(err, ErrSettings) {
		t.Fatalf("dangling link accepted: %v", err)
	}
}
