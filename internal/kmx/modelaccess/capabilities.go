package modelaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Capabilities records advertised or operator-declared limits. Zero limits and
// nil booleans are unknown, not implicit support. No model-name heuristics apply.
type Capabilities struct {
	ContextTokens int   `json:"contextTokens,omitempty"`
	OutputTokens  int   `json:"outputTokens,omitempty"`
	Reasoning     *bool `json:"reasoning,omitempty"`
	ToolCalling   *bool `json:"toolCalling,omitempty"`
	Streaming     *bool `json:"streaming,omitempty"`
}

type Requirements struct {
	ContextTokens int  `json:"contextTokens,omitempty" yaml:"contextTokens,omitempty"`
	OutputTokens  int  `json:"outputTokens,omitempty" yaml:"outputTokens,omitempty"`
	Reasoning     bool `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
}

func (r Requirements) Validate() error {
	if r.ContextTokens < 0 || r.OutputTokens < 0 || r.ContextTokens > 10000000 || r.OutputTokens > 10000000 || r.ContextTokens > 0 && r.OutputTokens > r.ContextTokens {
		return errors.New("invalid model token requirements")
	}
	return nil
}

func (c Capabilities) Validate() error {
	return (Requirements{ContextTokens: c.ContextTokens, OutputTokens: c.OutputTokens}).Validate()
}

// Match distinguishes a known mismatch from missing information. Token matching
// shares AgentSuite's public inference contract; this does not create evidence.
func (r Requirements) Match(c Capabilities) string {
	if r.Validate() != nil || c.Validate() != nil {
		return "mismatch"
	}
	if r.ContextTokens > 0 && c.ContextTokens > 0 && c.ContextTokens < r.ContextTokens || r.OutputTokens > 0 && c.OutputTokens > 0 && c.OutputTokens < r.OutputTokens || r.Reasoning && c.Reasoning != nil && !*c.Reasoning {
		return "mismatch"
	}
	if r.ContextTokens > 0 && c.ContextTokens == 0 || r.OutputTokens > 0 && c.OutputTokens == 0 || r.Reasoning && c.Reasoning == nil {
		return "unknown"
	}
	if r.ContextTokens > 0 && r.OutputTokens > 0 {
		need := suitecore.InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: r.ContextTokens, OutputTokens: r.OutputTokens}
		have := suitecore.InferenceRequirements{API: need.API, ContextTokens: c.ContextTokens, OutputTokens: c.OutputTokens}
		if need.Match(have) != nil {
			return "mismatch"
		}
	}
	return "match"
}

// OutputBudget preserves explicit minima and bounds only the default by the
// frozen model limit. Unknown limits cannot satisfy an explicit requirement.
func (r Requirements) OutputBudget(c Capabilities) (int, error) {
	if r.Match(c) != "match" {
		return 0, errors.New("model capabilities do not satisfy requirements")
	}
	if r.OutputTokens > 0 {
		return r.OutputTokens, nil
	}
	budget := 1024
	if c.OutputTokens > 0 {
		budget = min(budget, c.OutputTokens)
	}
	return budget, nil
}

// Profile binds operator declarations to one exact route, never to a model name
// alone. Credentials remain environment references, not profile content.
type Profile struct {
	Route        Route        `json:"route"`
	Capabilities Capabilities `json:"capabilities"`
}

func LoadProfile(path string) (Profile, error) {
	var p Profile
	path, err := filepath.Abs(path)
	if err != nil {
		return p, errors.New("model profile unavailable")
	}
	dir, err := fileaccess.OpenDirectory(context.Background(), filepath.Dir(path))
	if err != nil {
		return p, errors.New("model profile unavailable")
	}
	defer dir.Close()
	f, err := fileaccess.OpenRegularAt(context.Background(), dir, filepath.Base(path))
	if err != nil {
		return p, errors.New("model profile unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return p, errors.New("model profile must be a regular file within 64 KiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return p, errors.New("model profile exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if !agentruntime.JSONKeysUnique(b) || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.Route.Validate() != nil || p.Capabilities.Validate() != nil {
		return Profile{}, errors.New("invalid model profile")
	}
	if p.Route.Provider != "fake" && p.Route.Provider != "copilot" {
		if err := p.Route.ValidateEndpoint(); err != nil {
			return Profile{}, err
		}
	}
	return p, nil
}
