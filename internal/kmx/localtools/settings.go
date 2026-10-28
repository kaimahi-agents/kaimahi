package localtools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

type ToolState string

const (
	Enabled  ToolState = "Enabled"
	Disabled ToolState = "Disabled"
)

type ToolSetting struct {
	Name  string    `json:"name"`
	State ToolState `json:"state"`
}

var ErrSettings = errors.New("invalid tool settings in the agent folder's kmx.yaml")

// SettingsSnapshot binds effective states to the exact bytes read, not to an agent revision.
// Empty Revision means no declared tools; absent means an observed missing config.
type SettingsSnapshot struct {
	Settings []ToolSetting
	Revision string
}

// LoadSettings reads only beside the caller's checked physical agent filename.
// It never searches ancestors, creates a file or rewrites unrelated YAML keys.
func LoadSettings(ctx context.Context, filename string, names []string) ([]ToolSetting, error) {
	return loadSettings(ctx, filename, names, fileaccess.OpenDirectory)
}
func loadSettings(ctx context.Context, filename string, names []string, openDirectory func(context.Context, string) (*fileaccess.Directory, error)) ([]ToolSetting, error) {
	snapshot, err := readSettingsSnapshot(ctx, filename, names, openDirectory)
	return snapshot.Settings, err
}

// ReadSettingsSnapshot projects states and the opaque revision from one checked read.
func ReadSettingsSnapshot(ctx context.Context, filename string, names []string) (SettingsSnapshot, error) {
	return readSettingsSnapshot(ctx, filename, names, fileaccess.OpenDirectory)
}
func readSettingsSnapshot(ctx context.Context, filename string, names []string, openDirectory func(context.Context, string) (*fileaccess.Directory, error)) (SettingsSnapshot, error) {
	if _, err := ParseSettings(nil, names); err != nil {
		return SettingsSnapshot{}, err
	}
	if len(names) == 0 {
		return SettingsSnapshot{}, nil
	}
	raw, err := readSettingsBytes(ctx, filename, openDirectory)
	if err != nil {
		return SettingsSnapshot{}, err
	}
	settings, err := ParseSettings(raw, names)
	if err != nil {
		return SettingsSnapshot{}, err
	}
	token := "absent"
	if raw != nil {
		token = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	}
	return SettingsSnapshot{Settings: settings, Revision: token}, nil
}

// Keep filesystem-foundation wiring in this audited owner. Launch selection
// consumes the same bounded checked bytes without widening the import surface.
func readLaunchBytes(ctx context.Context, filename string) ([]byte, error) {
	return readSettingsBytes(ctx, filename, fileaccess.OpenDirectory)
}

func readSettingsBytes(ctx context.Context, filename string, openDirectory func(context.Context, string) (*fileaccess.Directory, error)) ([]byte, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, ErrSettings
	}
	refuse := func() ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, ErrSettings
	}
	folder := filepath.Dir(absolute)
	// Absence is not authority to impose private-state volume qualification on source.
	// Lstat keeps a dangling final symlink on the checked no-follow path.
	if _, statErr := os.Lstat(filepath.Join(folder, "kmx.yaml")); statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return refuse()
		}
		if info, dirErr := os.Lstat(folder); dirErr == nil && info.IsDir() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
	dir, err := openDirectory(ctx, folder)
	if err != nil {
		return refuse()
	}
	defer dir.Close()
	file, err := fileaccess.OpenRegularAt(ctx, dir, "kmx.yaml")
	if errors.Is(err, fileaccess.ErrMissing) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return refuse()
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || before.Size() > 256*1024 {
		return refuse()
	}
	raw, err := io.ReadAll(io.LimitReader(file, 256*1024+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || len(raw) > 256*1024 || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return refuse()
	}
	// A present empty file is invalid, not the absent-file sentinel.
	if raw == nil {
		raw = []byte{}
	}
	return raw, nil
}

// CopySettings validates exact definition coverage; nil is the legacy Enabled default.
func CopySettings(settings []ToolSetting, defs []api.ToolDefinition) ([]ToolSetting, error) {
	if settings == nil {
		return nil, nil
	}
	if defs == nil {
		defs = []api.ToolDefinition{Definition()}
	}
	if len(settings) != len(defs) {
		return nil, ErrSettings
	}
	seen := map[string]bool{}
	for _, s := range settings {
		if seen[s.Name] || s.State != Enabled && s.State != Disabled {
			return nil, ErrSettings
		}
		seen[s.Name] = true
	}
	for _, d := range defs {
		if !seen[d.Name] {
			return nil, ErrSettings
		}
	}
	return append([]ToolSetting{}, settings...), nil
}

func IsDisabled(settings []ToolSetting, name string) bool {
	for _, s := range settings {
		if s.Name == name {
			return s.State == Disabled
		}
	}
	return false
}

// DisabledResult is a correlated controller-local answer, never tool dispatch.
func DisabledResult(id string) api.ToolResult {
	return api.ToolResult{ID: id, Output: map[string]any{"status": "disabled", "message": "This tool is disabled for this test."}}
}
