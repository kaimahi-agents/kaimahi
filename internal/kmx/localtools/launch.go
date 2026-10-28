package localtools

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

// StdioInvocation is a single Run's lazy CLI-owned selection. Construction is
// inert; the existing runner calls the resolver once per Run, across its tests.
// Neither its launch inputs nor environment values are journal data.
type StdioInvocation struct {
	ctx       context.Context
	file      string
	selection ServerSelection
	read      func(context.Context, string) ([]byte, error)
	lookup    func(string) (string, bool)
	owned     []*StdioHost
	closed    bool
	closeErr  error
}

func NewStdioInvocation(ctx context.Context, filename string, selection ServerSelection) *StdioInvocation {
	selection.Names = slices.Clone(selection.Names)
	return &StdioInvocation{ctx: ctx, file: filename, selection: selection, lookup: os.LookupEnv, read: readLaunchBytes}
}

// Resolve offers only Enabled names needing a source. The outer invocation
// context owns children; the resolver context ends when the inner Run returns.
func (s *StdioInvocation) Resolve(ctx context.Context, names []string, settings []ToolSetting) (*Host, error) {
	if s.closed {
		return nil, ErrServerSelection
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var needed []string
	for _, name := range names {
		if !IsDisabled(settings, name) {
			needed = append(needed, name)
		}
	}
	if s.selection.Clear || len(needed) == 0 {
		return nil, nil
	}
	raw, err := s.read(ctx, s.file)
	if err != nil {
		return nil, ErrServerSelection
	}
	declared := make([]string, len(settings))
	for i, v := range settings {
		declared[i] = v.Name
	}
	states, err := ParseSettings(raw, declared)
	if err != nil || !slices.Equal(states, settings) {
		return nil, ErrServerSelection
	}
	defs, err := parseLaunchConfig(raw, s.selection)
	if err != nil {
		return nil, err
	}
	folder, err := filepath.Abs(filepath.Dir(s.file))
	if err != nil {
		return nil, ErrServerSelection
	}
	type launchPlan struct {
		config StdioConfig
		refs   map[string]string
	}
	var plans []launchPlan
	claimed := map[string]bool{}
	// First freeze every selected mapping, then every used env/executable, and
	// only then start children. An unused definition never reads credentials.
	for _, def := range defs {
		cfg := StdioConfig{Command: def.command, Args: slices.Clone(def.args), Dir: folder, Env: map[string]string{}, Tools: map[string]string{}}
		for local, remote := range def.tools {
			if !slices.Contains(needed, local) {
				continue
			}
			if claimed[local] {
				return nil, ErrServerSelection
			}
			claimed[local] = true
			cfg.Tools[local] = remote
		}
		if len(cfg.Tools) > 0 {
			plans = append(plans, launchPlan{config: cfg, refs: def.env})
		}
	}
	if len(plans) == 0 {
		return nil, nil
	}
	type envValue struct {
		value   string
		present bool
	}
	frozen := map[string]envValue{}
	lookup := func(name string) (string, bool) {
		if v, ok := frozen[name]; ok {
			return v.value, v.present
		}
		value, present := s.lookup(name)
		frozen[name] = envValue{value, present}
		return value, present
	}
	path, _ := lookup("PATH")
	var credentialChecks []func(...string) error
	for i := range plans {
		cfg := &plans[i].config
		if path != "" {
			cfg.Env["PATH"] = path
		}
		for key, ref := range plans[i].refs {
			value, present := lookup(ref)
			if !present || value == "" {
				return nil, ErrServerSelection
			}
			cfg.Env[key] = value
			cfg.CredentialEnv = append(cfg.CredentialEnv, key)
		}
		cfg.Command, err = resolveStdioCommand(cfg.Command, folder, cfg.Env["PATH"])
		if err != nil {
			return nil, err
		}
		check, err := validateStdioConfig(*cfg)
		if err != nil {
			return nil, ErrServerSelection
		}
		credentialChecks = append(credentialChecks, check)
	}
	// A server must not receive another selected server's credential in argv.
	// Environments remain separate; check the frozen argument union before exec.
	for _, plan := range plans {
		argv := append([]string{plan.config.Command}, plan.config.Args...)
		for _, check := range credentialChecks {
			if check(argv...) != nil {
				return nil, ErrServerSelection
			}
		}
	}
	var definitions []api.ToolDefinition
	byName := map[string]*Host{}
	for _, plan := range plans {
		host, err := StartStdioHost(s.ctx, plan.config)
		if err != nil {
			return nil, ErrServerSelection
		}
		s.owned = append(s.owned, host)
		for _, def := range host.Host().Definitions() {
			definitions = append(definitions, def)
			byName[def.Name] = host.Host()
		}
	}
	return NewTrustedHost(definitions, func(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
		return byName[call.Tool].Execute(ctx, scope, call)
	}, func(values ...string) error {
		for _, owned := range s.owned {
			if err := owned.Host().Check(values...); err != nil {
				return err
			}
		}
		return nil
	})
}

func resolveStdioCommand(command, folder, path string) (string, error) {
	if filepath.IsAbs(command) {
		return command, nil
	}
	if strings.ContainsAny(command, "/\\") {
		return filepath.Join(folder, command), nil
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		} // Match exec's refusal of implicit cwd lookup.
		candidate := filepath.Join(dir, command)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", ErrServerSelection
}

func (s *StdioInvocation) Close() error {
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	for _, host := range s.owned {
		if err := host.Close(); err != nil {
			s.closeErr = ErrStdioCleanup
		}
	}
	return s.closeErr
}
