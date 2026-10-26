package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var settingsToken = regexp.MustCompile(`^(absent|sha256:[a-f0-9]{64})$`)

type ToolStateInput struct {
	SchemaVersion            int                  `json:"schemaVersion"`
	Path                     string               `json:"path"`
	ExpectedRevision         string               `json:"expectedRevision"`
	ExpectedSettingsRevision string               `json:"expectedSettingsRevision"`
	Name                     string               `json:"name"`
	State                    localtools.ToolState `json:"state"`
}
type ToolStateResult struct {
	SchemaVersion        int                      `json:"schemaVersion"`
	Operation            string                   `json:"operation"`
	State                string                   `json:"state"`
	File                 *File                    `json:"file,omitempty"`
	Changed              *bool                    `json:"changed,omitempty"`
	MayHaveWritten       bool                     `json:"mayHaveWritten"`
	ToolSettingsRevision string                   `json:"toolSettingsRevision,omitempty"`
	ToolSettings         []localtools.ToolSetting `json:"toolSettings,omitempty"`
	ReasonCode           string                   `json:"reasonCode,omitempty"`
	Reason               string                   `json:"reason,omitempty"`
}

// ToolStateOperation is original-server authoring only, never a preview binding.
func ToolStateOperation(files *Files) Operation {
	return Operation{Description: "Set one declared tool Enabled or Disabled in its agent folder's kmx.yaml at caller-read agent and settings revisions. Preserve other keys; no inference or native tool execution. A failed publication can be unknown; never retry automatically.", Schema: &jsonschema.Schema{Type: "object", Required: []string{"path", "expectedRevision", "expectedSettingsRevision", "name", "state"}, Properties: map[string]*jsonschema.Schema{
		"path": {Type: "string", MinLength: new(1), MaxLength: new(4096)}, "expectedRevision": {Type: "string", Pattern: `^sha256:[a-f0-9]{64}$`}, "expectedSettingsRevision": {Type: "string", Pattern: settingsToken.String()}, "name": {Type: "string", MinLength: new(1), MaxLength: new(64)}, "state": {Type: "string", Enum: []any{"Enabled", "Disabled"}},
	}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}, Handler: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var in ToolStateInput
		if json.Unmarshal(req.Params.Arguments, &in) != nil {
			return nil, refusal("invalid_options")
		}
		out := files.SetToolState(ctx, in)
		b, err := json.Marshal(out)
		if err != nil {
			return nil, refusal("operation_failed")
		}
		return &sdk.CallToolResult{IsError: out.State != "complete", StructuredContent: out, Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, nil
	}}
}

func publishSettings(parent *os.Root, staged, target string, existing bool) error {
	if existing {
		return parent.Rename(staged, target)
	}
	return renameExclusive(parent, staged, target)
}

// SetToolState performs one cooperative CAS; no source/YAML operand grants a path.
func (f *Files) SetToolState(ctx context.Context, in ToolStateInput) ToolStateResult {
	return f.setToolState(ctx, in, publishSettings)
}
func (f *Files) setToolState(ctx context.Context, in ToolStateInput, publish func(*os.Root, string, string, bool) error) ToolStateResult {
	r := ToolStateResult{SchemaVersion: 1, Operation: "set_tool_state", State: "refused", Changed: new(false)}
	fail := func(code string) ToolStateResult {
		if !r.MayHaveWritten {
			if err := cancelled(ctx); err != nil {
				code = err.(*Error).Code
			}
		}
		r.ReasonCode = code
		r.Reason = (&Error{Code: code}).Error()
		return r
	}
	if f == nil {
		return fail("invalid_root")
	}
	if in.SchemaVersion != 1 || len(in.Path) > 4096 || !settingsToken.MatchString(in.ExpectedRevision) || in.ExpectedRevision == "absent" || !settingsToken.MatchString(in.ExpectedSettingsRevision) || !localtools.ValidToolName(in.Name) || in.State != localtools.Enabled && in.State != localtools.Disabled {
		return fail("invalid_options")
	}
	if err := cancelled(ctx); err != nil {
		return fail("cancelled")
	}
	p, err := f.rootRelative(in.Path)
	if err != nil || !f.allowedPath(p) {
		return fail("unsafe_path")
	}
	parent, err := f.parent(p, false)
	if err != nil {
		return fail("unsafe_path")
	}
	defer parent.Close()
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return fail("unsafe_path")
	}
	physical := p
	if f.root != nil {
		physical = filepath.Join(f.root.Name(), filepath.FromSlash(p))
	}
	folder := filepath.Dir(physical)
	currentParent := func() bool {
		pinned, e := parent.Stat(".")
		if e != nil || !os.SameFile(parentInfo, pinned) {
			return false
		}
		named, e := os.Lstat(folder)
		if e != nil || !named.IsDir() || !os.SameFile(named, pinned) {
			return false
		}
		if f.root != nil {
			root, e := f.root.Stat(".")
			if e != nil {
				return false
			}
			named, e := os.Lstat(f.root.Name())
			if e != nil || !named.IsDir() || !os.SameFile(root, named) {
				return false
			}
		}
		return true
	}
	if !currentParent() {
		return fail("stale_revision")
	}
	source, b, sourceInfo, err := readTarget(parent, filepath.Base(p))
	if err != nil {
		return fail("invalid_file")
	}
	defer source.Close()
	if revision(b) != in.ExpectedRevision {
		return fail("stale_revision")
	}
	agent, err := agentfile.Parse(p, b)
	if err != nil || f.validate(p, b) != nil {
		return fail("invalid_file")
	}
	if !slices.Contains(agent.Tools, in.Name) {
		return fail("undeclared_tool")
	}
	r.File = &File{Path: in.Path, Revision: in.ExpectedRevision}
	qualify := func() bool {
		dir, e := fileaccess.OpenDirectory(ctx, folder)
		if e != nil {
			return false
		}
		defer dir.Close()
		info, e := dir.Stat()
		return e == nil && os.SameFile(info, parentInfo) && currentParent()
	}
	var cfg *os.File
	var raw []byte
	var cfgInfo os.FileInfo
	_, err = parent.Lstat("kmx.yaml")
	exists := !errors.Is(err, os.ErrNotExist)
	if err != nil && exists {
		return fail("settings_unavailable")
	}
	if exists {
		if !qualify() {
			return fail("settings_unavailable")
		}
		cfg, raw, cfgInfo, err = readTarget(parent, "kmx.yaml")
		if err != nil {
			return fail("settings_unavailable")
		}
		defer cfg.Close()
		after, e := cfg.Stat()
		if e != nil || after.Mode() != cfgInfo.Mode() || after.Size() != cfgInfo.Size() || !after.ModTime().Equal(cfgInfo.ModTime()) || int64(len(raw)) != cfgInfo.Size() {
			return fail("stale_settings_revision")
		}
	}
	token := "absent"
	if exists {
		token = revision(raw)
	}
	if token != in.ExpectedSettingsRevision {
		return fail("stale_settings_revision")
	}
	updated, states, changed, err := localtools.ChangeToolState(raw, agent.Tools, in.Name, in.State)
	if err != nil {
		return fail("invalid_tool_settings")
	}
	check := func() string {
		if cancelled(ctx) != nil {
			return "cancelled"
		}
		if !currentParent() {
			return "stale_revision"
		}
		now, bytes, info, e := readTarget(parent, filepath.Base(p))
		if e != nil {
			return "stale_revision"
		}
		now.Close()
		if !os.SameFile(sourceInfo, info) || info.Mode() != sourceInfo.Mode() || revision(bytes) != in.ExpectedRevision {
			return "stale_revision"
		}
		if !exists {
			if _, e := parent.Lstat("kmx.yaml"); !errors.Is(e, os.ErrNotExist) {
				return "stale_settings_revision"
			}
			return ""
		}
		current, bytes, info, e := readTarget(parent, "kmx.yaml")
		if e != nil {
			return "stale_settings_revision"
		}
		current.Close()
		if !os.SameFile(cfgInfo, info) || info.Mode() != cfgInfo.Mode() || revision(bytes) != token {
			return "stale_settings_revision"
		}
		return ""
	}
	if !changed {
		if code := check(); code != "" {
			return fail(code)
		}
		r.State = "complete"
		r.ToolSettings = states
		r.ToolSettingsRevision = token
		return r
	}
	if !exists && !qualify() {
		return fail("settings_unavailable")
	}
	r.State = "failed"
	mode := os.FileMode(0644)
	if exists {
		mode = cfgInfo.Mode()
	}
	staged, err := stage(parent, updated, mode, exists)
	if err != nil {
		return fail("io_failed")
	}
	defer parent.Remove(staged)
	if exists {
		unlock, e := lockTarget(cfg)
		if e != nil {
			return fail(lockFailure(e).(*Error).Code)
		}
		defer unlock()
	}
	if code := check(); code != "" {
		r.State = "refused"
		return fail(code)
	}
	// The Linux exclusive-copy fallback can become visible before returning an error.
	// An error is never proof of no effect; do not retry or roll back ambiguously.
	r.MayHaveWritten = true
	if err := publish(parent, staged, "kmx.yaml", exists); err != nil {
		if !exists && errors.Is(err, os.ErrExist) {
			r.MayHaveWritten = false
			r.State = "refused"
			return fail("stale_settings_revision")
		}
		r.State = "unknown"
		r.Changed = nil
		return fail("settings_write_unknown")
	}
	r.State = "complete"
	r.Changed = new(true)
	r.ToolSettings = states
	r.ToolSettingsRevision = revision(updated)
	return r
}
