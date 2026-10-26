package localtools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
	"oras.land/oras-go/v2/registry"
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

// LoadSettings reads only beside the caller's checked physical agent filename.
// It never searches ancestors, creates a file or rewrites unrelated YAML keys.
func LoadSettings(ctx context.Context, filename string, names []string) ([]ToolSetting, error) {
	return loadSettings(ctx, filename, names, fileaccess.OpenDirectory)
}

// loadSettings takes the directory opener only so a test can prove the boundary.
func loadSettings(ctx context.Context, filename string, names []string, openDirectory func(context.Context, string) (*fileaccess.Directory, error)) ([]ToolSetting, error) {
	settings := make([]ToolSetting, len(names))
	seen := map[string]bool{}
	for i, name := range names {
		if !ValidToolName(name) || seen[name] || len(names) > 32 {
			return nil, ErrSettings
		}
		settings[i] = ToolSetting{Name: name, State: Enabled}
		seen[name] = true
	}
	if len(names) == 0 {
		return nil, nil
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, ErrSettings
	}
	// Cancellation outranks an invalid-config verdict so callers keep it.
	refuse := func() ([]ToolSetting, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, ErrSettings
	}
	folder := filepath.Dir(absolute)
	// An absent optional file in an existing source folder must not trigger the
	// private-state filesystem qualification (Windows accepts only local NTFS).
	// Lstat sees a dangling final symlink as present, so it still reaches the
	// checked no-follow open below and is refused.
	if _, statErr := os.Lstat(filepath.Join(folder, "kmx.yaml")); statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return refuse()
		}
		if info, dirErr := os.Lstat(folder); dirErr == nil && info.IsDir() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return settings, nil
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
		return settings, nil
	}
	if err != nil {
		return refuse()
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || before.Size() > 256*1024 {
		return nil, ErrSettings
	}
	raw, err := io.ReadAll(io.LimitReader(file, 256*1024+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || len(raw) > 256*1024 || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) || secretshapes.Match(string(raw)) != nil {
		return nil, ErrSettings
	}
	var doc, extra yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || !settingsNode(doc.Content[0], 0) || decoder.Decode(&extra) != io.EOF {
		return nil, ErrSettings
	}
	root := doc.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		switch key {
		case "schemaVersion":
			var version int
			if value.Tag != "!!int" || value.Decode(&version) != nil || version != 1 {
				return nil, ErrSettings
			}
		case "publishRepository":
			ref, parseErr := registry.ParseReference(value.Value)
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || parseErr != nil || ref.Reference != "" {
				return nil, ErrSettings
			}
		case "tools":
			if value.Kind != yaml.MappingNode {
				return nil, ErrSettings
			}
			overrides := map[string]ToolState{}
			for j := 0; j < len(value.Content); j += 2 {
				name, state := value.Content[j].Value, value.Content[j+1]
				if !ValidToolName(name) || state.Kind != yaml.ScalarNode || state.Tag != "!!str" || ToolState(state.Value) != Enabled && ToolState(state.Value) != Disabled {
					return nil, ErrSettings
				}
				overrides[name] = ToolState(state.Value)
			}
			for j := range settings {
				if state, ok := overrides[settings[j].Name]; ok {
					settings[j].State = state
				}
			}
		}
	}
	return settings, nil
}

func settingsNode(n *yaml.Node, depth int) bool {
	if n == nil || depth > 32 || n.Kind == yaml.AliasNode || n.Anchor != "" {
		return false
	}
	if n.Kind == yaml.MappingNode {
		if n.Tag != "!!map" || len(n.Content)%2 != 0 {
			return false
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
				return false
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if !settingsNode(child, depth+1) {
			return false
		}
	}
	return true
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
