package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ErrStdio = errors.New("local tools: stdio server unavailable")
var ErrStdioCleanup = errors.New("local tools: stdio server cleanup failed")

// StdioConfig is resolved host authority, never agent input or journal data.
// Command and Dir are absolute. Env is the complete explicit environment; the
// caller resolves named references and supplies PATH rather than inheriting it.
type StdioConfig struct {
	Command string
	Args    []string
	Dir     string
	Env     map[string]string
	// CredentialEnv identifies sensitive values within Env. The trusted caller
	// must include every credential, including short values; non-secret settings
	// such as DEBUG=1 must not be mistaken for protocol echoes.
	CredentialEnv []string
	Tools         map[string]string // local declared name -> exact remote name
}

// StdioHost owns one invocation's process and SDK session. The caller must check
// Close before accepting completion; an available Host alone is not success.
type StdioHost struct {
	host    *Host
	process *stdioProcess
	session *mcp.ClientSession
}

func (s *StdioHost) Host() *Host { return s.host }

func (s *StdioHost) Close() error {
	err := s.process.Close()
	if s.session != nil && s.session.Close() != nil && err == nil {
		err = ErrStdio
	}
	return err
}

// validateStdioConfig is pure so a multi-server invocation can validate every
// frozen launch before starting the first child.
func validateStdioConfig(cfg StdioConfig) (func(...string) error, error) {
	if !filepath.IsAbs(cfg.Command) || !filepath.IsAbs(cfg.Dir) || len(cfg.Command)+len(cfg.Dir) > 8192 || len(cfg.Args) > 128 || len(cfg.Env) > 64 || len(cfg.Tools) == 0 || len(cfg.Tools) > 32 {
		return nil, ErrStdio
	}
	var secrets []string
	bytes := 0
	for name, value := range cfg.Env {
		bytes += len(name) + len(value)
		if !envID.MatchString(name) || strings.ContainsRune(value, 0) || bytes > 64<<10 {
			return nil, ErrStdio
		}
	}
	seen := map[string]bool{}
	for _, name := range cfg.CredentialEnv {
		value, present := cfg.Env[name]
		if !present || value == "" || seen[name] || len(cfg.CredentialEnv) > 64 {
			return nil, ErrStdio
		}
		seen[name] = true
		secrets = append(secrets, value)
	}
	exact := func(values ...string) error {
		for _, value := range values {
			for _, secret := range secrets {
				if strings.Contains(value, secret) {
					return errUnsafeContent
				}
			}
		}
		return nil
	}
	bytes = 0
	for _, arg := range cfg.Args {
		bytes += len(arg)
		if strings.ContainsRune(arg, 0) || bytes > 16<<10 || exact(arg) != nil || secretshapes.Match(arg) != nil {
			return nil, ErrStdio
		}
	}
	for local, remote := range cfg.Tools {
		if !ValidToolName(local) || remote == "" || len(remote) > 128 || strings.TrimSpace(remote) != remote || exact(local, remote) != nil {
			return nil, ErrStdio
		}
	}
	return exact, nil
}

// StartStdioHost starts only an already selected server. It performs no config,
// credential, executable or server discovery and offers only explicit mappings.
func StartStdioHost(ctx context.Context, cfg StdioConfig) (*StdioHost, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg.Args, cfg.Env, cfg.Tools = slices.Clone(cfg.Args), maps.Clone(cfg.Env), maps.Clone(cfg.Tools)
	cfg.CredentialEnv = slices.Clone(cfg.CredentialEnv)
	exact, err := validateStdioConfig(cfg)
	if err != nil {
		return nil, err
	}
	process, err := startStdioProcess(ctx, cfg, exact)
	if err != nil {
		return nil, ErrStdio
	}
	s := &StdioHost{process: process}
	fail := func() (*StdioHost, error) {
		cleanup := s.Close()
		cause := ErrStdio
		if err := ctx.Err(); err != nil {
			cause = err
		}
		if errors.Is(cleanup, ErrStdioCleanup) {
			cause = errors.Join(cause, ErrStdioCleanup)
		}
		return nil, cause
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(startup, func() { _ = process.Close() })
	defer stop()
	client := mcp.NewClient(&mcp.Implementation{Name: "kmx-stdio-tools", Version: "1"}, &mcp.ClientOptions{
		Logger: slog.New(slog.DiscardHandler), Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	s.session, err = client.Connect(startup, process.transport(), &mcp.ClientSessionOptions{ProtocolVersion: mcpProtocol})
	if err != nil || s.session.InitializeResult().ProtocolVersion != mcpProtocol {
		return fail()
	}
	listed, err := s.session.ListTools(startup, nil)
	if err != nil || listed == nil || listed.NextCursor != "" || len(listed.Tools) > 128 {
		return fail()
	}
	var defs []api.ToolDefinition
	for _, local := range slices.Sorted(maps.Keys(cfg.Tools)) {
		var selected *mcp.Tool
		for _, tool := range listed.Tools {
			if tool != nil && tool.Name == cfg.Tools[local] {
				if selected != nil {
					return fail()
				}
				selected = tool
			}
		}
		if selected == nil {
			return fail()
		}
		raw, err := json.Marshal(selected.InputSchema)
		var schema map[string]any
		if err != nil || json.Unmarshal(raw, &schema) != nil || schema == nil {
			return fail()
		}
		defs = append(defs, api.ToolDefinition{Name: local, Description: selected.Description, InputSchema: schema})
	}
	check := func(values ...string) error {
		if process.healthy() != nil {
			return ErrStdio
		}
		return exact(values...)
	}
	s.host, err = NewTrustedHost(defs, func(ctx context.Context, _ controller.ToolCallContext, call api.ToolCall) (out api.ToolResult, failure error) {
		defer func() {
			if failure != nil {
				process.fail()
				_ = s.Close()
			}
		}()
		if process.healthy() != nil {
			return api.ToolResult{}, ErrStdio
		}
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		stop := context.AfterFunc(bounded, func() { process.fail(); _ = process.Close() })
		defer stop()
		result, err := s.session.CallTool(bounded, &mcp.CallToolParams{Name: cfg.Tools[call.Tool], Arguments: call.Args})
		if err != nil || bounded.Err() != nil || result == nil || result.IsError || result.NeedsInput() || len(result.InputRequests) != 0 || result.RequestState != "" || len(result.Content) > 32 {
			return api.ToolResult{}, ErrStdio
		}
		var text []string
		for _, content := range result.Content {
			part, ok := content.(*mcp.TextContent)
			if !ok || part == nil {
				return api.ToolResult{}, ErrStdio
			}
			text = append(text, part.Text)
		}
		var output map[string]any
		if result.StructuredContent != nil {
			raw, err := json.Marshal(result.StructuredContent)
			if err != nil || json.Unmarshal(raw, &output) != nil || output == nil {
				return api.ToolResult{}, ErrStdio
			}
		} else if len(text) > 0 {
			output = map[string]any{"text": strings.Join(text, "\n")}
		} else {
			return api.ToolResult{}, ErrStdio
		}
		raw, err := json.Marshal(output)
		if err != nil || len(raw) > 32<<10 || !runtime.JSONNumbersExact(raw) || CheckJSON(output, check) != nil {
			return api.ToolResult{}, ErrStdio
		}
		return api.ToolResult{ID: call.ID, Output: output}, nil
	}, check)
	if err != nil || startup.Err() != nil || process.healthy() != nil {
		return fail()
	}
	return s, nil
}
