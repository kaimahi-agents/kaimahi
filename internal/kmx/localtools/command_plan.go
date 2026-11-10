package localtools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CommandPlan is a frozen, credential-free commandTools selection. It is safe
// to transfer to the resident tool broker, but it is still host execution
// authority and must never enter model or journal content.
type CommandPlan struct {
	ConfigRevision string          `json:"configRevision"`
	Revision       string          `json:"revision"`
	Commands       []CommandConfig `json:"commands"`
}

// FreezeCommandPlan reads the agent folder's exact kmx.yaml once and resolves
// only enabled fixed commandTools. Resident plans deliberately refuse env
// references: the separate broker receives no model credential or ambient
// credential environment.
func FreezeCommandPlan(ctx context.Context, filename string, names []string) (CommandPlan, error) {
	if len(names) == 0 {
		return CommandPlan{}, nil
	}
	raw, err := readLaunchBytes(ctx, filename)
	if err != nil || raw == nil {
		return CommandPlan{}, ErrServerSelection
	}
	settings, err := ParseSettings(raw, names)
	if err != nil || len(settings) != len(names) {
		return CommandPlan{}, ErrServerSelection
	}
	for _, setting := range settings {
		if setting.State != Enabled {
			return CommandPlan{}, ErrServerSelection
		}
	}
	definitions, err := parseCommandConfig(raw, names)
	if err != nil || len(definitions) != len(names) {
		return CommandPlan{}, ErrServerSelection
	}
	folder, err := filepath.Abs(filepath.Dir(filename))
	if err != nil {
		return CommandPlan{}, ErrServerSelection
	}
	folder, err = filepath.EvalSymlinks(folder)
	if err != nil {
		return CommandPlan{}, ErrServerSelection
	}
	wanted := slices.Clone(names)
	slices.Sort(wanted)
	seen := make([]string, 0, len(definitions))
	plan := CommandPlan{ConfigRevision: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}
	identity := sha256.New()
	_, _ = identity.Write(raw)
	for _, definition := range definitions {
		if len(definition.env) != 0 {
			return CommandPlan{}, ErrServerSelection
		}
		dir, resolveErr := filepath.EvalSymlinks(filepath.Clean(filepath.Join(folder, definition.directory)))
		relative, relErr := filepath.Rel(folder, dir)
		if resolveErr != nil || relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return CommandPlan{}, ErrServerSelection
		}
		cfg := CommandConfig{Name: definition.name, Command: definition.command, Args: slices.Clone(definition.args), Dir: dir, Env: map[string]string{}}
		commandInfo, commandErr := os.Stat(cfg.Command)
		dirInfo, dirErr := os.Stat(cfg.Dir)
		if commandErr != nil || !commandInfo.Mode().IsRegular() || commandInfo.Mode().Perm()&0111 == 0 || dirErr != nil || !dirInfo.IsDir() {
			return CommandPlan{}, ErrServerSelection
		}
		cfg.commandInfo, cfg.dirInfo = commandInfo, dirInfo
		cfg.ExecutableID, _ = commandFileIdentity(commandInfo)
		cfg.DirectoryID, _ = commandFileIdentity(dirInfo)
		if cfg.ExecutableID == "" || cfg.DirectoryID == "" {
			return CommandPlan{}, ErrServerSelection
		}
		cfg.ExecutableDigest, err = commandExecutableDigest(cfg.Command)
		if err != nil {
			return CommandPlan{}, ErrServerSelection
		}
		if _, validateErr := validateCommandConfig(cfg); validateErr != nil {
			return CommandPlan{}, ErrServerSelection
		}
		seen = append(seen, cfg.Name)
		plan.Commands = append(plan.Commands, cfg)
		_, _ = identity.Write([]byte{0})
		_, _ = identity.Write([]byte(cfg.Name))
		_, _ = identity.Write([]byte{0})
		_, _ = identity.Write([]byte(cfg.ExecutableDigest))
		_, _ = identity.Write([]byte{0})
		_, _ = identity.Write([]byte(cfg.ExecutableID))
		_, _ = identity.Write([]byte{0})
		_, _ = identity.Write([]byte(cfg.DirectoryID))
	}
	slices.Sort(seen)
	if !slices.Equal(seen, wanted) {
		return CommandPlan{}, ErrServerSelection
	}
	plan.Revision = fmt.Sprintf("sha256:%x", identity.Sum(nil))
	return plan, nil
}

// OpenCommandPlan revalidates executable and directory identities in the
// receiving process before exposing the fixed command host.
func OpenCommandPlan(plan CommandPlan) (*Host, error) {
	validRevision := func(revision string) bool {
		digest := strings.TrimPrefix(revision, "sha256:")
		valid := strings.HasPrefix(revision, "sha256:") && len(digest) == 64
		for _, value := range digest {
			valid = valid && (value >= '0' && value <= '9' || value >= 'a' && value <= 'f')
		}
		return valid
	}
	if !validRevision(plan.ConfigRevision) || !validRevision(plan.Revision) || len(plan.Commands) == 0 || len(plan.Commands) > 32 {
		return nil, ErrServerSelection
	}
	for _, cfg := range plan.Commands {
		if len(cfg.Env) != 0 || len(cfg.CredentialEnv) != 0 || cfg.ExecutableDigest == "" || cfg.ExecutableID == "" || cfg.DirectoryID == "" {
			return nil, ErrServerSelection
		}
	}
	host, err := NewCommandHost(plan.Commands)
	if err != nil {
		return nil, errors.Join(ErrServerSelection, err)
	}
	return host, nil
}
