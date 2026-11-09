package localtools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

var ErrCommand = errors.New("local tools: command unavailable")
var ErrCommandCleanup = errors.New("local tools: command cleanup failed")

// JSON may expand each retained byte to six bytes (for example, '<' to
// '\u003c'). Leave 2 KiB of the trusted host's 32 KiB limit for the envelope.
const commandOutputLimit = 5 * 1024

// CommandConfig is frozen host authority. Arguments are fixed and the complete
// environment is explicit; no model value can alter the launched command.
type CommandConfig struct {
	Name             string            `json:"name"`
	Command          string            `json:"command"`
	Dir              string            `json:"directory"`
	Args             []string          `json:"args"`
	Env              map[string]string `json:"environment"`
	CredentialEnv    []string          `json:"credentialEnvironment,omitempty"`
	ExecutableDigest string            `json:"executableDigest,omitempty"`
	ExecutableID     string            `json:"executableIdentity,omitempty"`
	DirectoryID      string            `json:"directoryIdentity,omitempty"`
	commandInfo      os.FileInfo
	dirInfo          os.FileInfo
}

const commandExecutableLimit = 256 << 20

func commandExecutableDigest(path string) (string, error) {
	return commandExecutableDigestContext(context.Background(), path)
}

func commandExecutableDigestContext(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", ErrCommand
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > commandExecutableLimit {
		return "", ErrCommand
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			written += int64(n)
			if written > commandExecutableLimit {
				return "", ErrCommand
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(readErr, os.ErrClosed) {
			return "", ErrCommand
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", ErrCommand
		}
	}
	after, statErr := file.Stat()
	if err != nil || statErr != nil || written != before.Size() || !os.SameFile(before, after) || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return "", ErrCommand
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

func commandDefinitionFor(name string) api.ToolDefinition {
	return api.ToolDefinition{Name: name, Description: "Run the host-configured command with fixed arguments.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
}

func validateCommandConfig(cfg CommandConfig) (func(...string) error, error) {
	if !ValidToolName(cfg.Name) || !filepath.IsAbs(cfg.Command) || !filepath.IsAbs(cfg.Dir) || len(cfg.Command)+len(cfg.Dir) > 8192 || len(cfg.Args) > 128 || len(cfg.Env) > 64 {
		return nil, ErrCommand
	}
	if cfg.ExecutableDigest != "" {
		digest := strings.TrimPrefix(cfg.ExecutableDigest, "sha256:")
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return nil, ErrCommand
		}
	}
	if (cfg.ExecutableID == "") != (cfg.DirectoryID == "") {
		return nil, ErrCommand
	}
	bytes := 0
	var secrets []string
	for name, value := range cfg.Env {
		bytes += len(name) + len(value)
		if !envID.MatchString(name) || strings.ContainsRune(value, 0) || bytes > 64<<10 {
			return nil, ErrCommand
		}
	}
	seen := map[string]bool{}
	for _, name := range cfg.CredentialEnv {
		value, ok := cfg.Env[name]
		if !ok || value == "" || seen[name] || len(cfg.CredentialEnv) > 64 {
			return nil, ErrCommand
		}
		seen[name] = true
		secrets = append(secrets, value)
	}
	check := func(values ...string) error {
		for _, value := range values {
			for _, secret := range secrets {
				if strings.Contains(value, secret) {
					return errUnsafeContent
				}
			}
			if secretshapes.Match(value) != nil {
				return errUnsafeContent
			}
		}
		return nil
	}
	bytes = 0
	for _, arg := range cfg.Args {
		bytes += len(arg)
		if strings.ContainsRune(arg, 0) || bytes > 16<<10 || check(arg) != nil {
			return nil, ErrCommand
		}
	}
	if check(cfg.Name, cfg.Command, cfg.Dir) != nil {
		return nil, ErrCommand
	}
	return check, nil
}

// NewCommandHost creates a one-shot-per-call trusted host. Process effects begin
// only when Execute is called; the caller owns the outer invocation context.
func NewCommandHost(configs []CommandConfig) (*Host, error) {
	if len(configs) == 0 || len(configs) > 32 {
		return nil, ErrCommand
	}
	definitions := make([]api.ToolDefinition, 0, len(configs))
	byName := map[string]CommandConfig{}
	checks := map[string]func(...string) error{}
	for _, input := range configs {
		cfg := input
		cfg.Args, cfg.Env, cfg.CredentialEnv = slices.Clone(cfg.Args), maps.Clone(cfg.Env), slices.Clone(cfg.CredentialEnv)
		if byName[cfg.Name].Name != "" {
			return nil, ErrCommand
		}
		check, err := validateCommandConfig(cfg)
		if err != nil {
			return nil, err
		}
		commandInfo, commandErr := os.Stat(cfg.Command)
		dirInfo, dirErr := os.Stat(cfg.Dir)
		if commandErr != nil || !commandInfo.Mode().IsRegular() || commandInfo.Mode().Perm()&0111 == 0 || dirErr != nil || !dirInfo.IsDir() {
			return nil, ErrCommand
		}
		if cfg.commandInfo != nil && !os.SameFile(cfg.commandInfo, commandInfo) || cfg.dirInfo != nil && !os.SameFile(cfg.dirInfo, dirInfo) {
			return nil, ErrCommand
		}
		if cfg.ExecutableID != "" {
			commandID, commandOK := commandFileIdentity(commandInfo)
			dirID, dirOK := commandFileIdentity(dirInfo)
			if !commandOK || !dirOK || commandID != cfg.ExecutableID || dirID != cfg.DirectoryID {
				return nil, ErrCommand
			}
		}
		if cfg.ExecutableDigest != "" {
			digest, digestErr := commandExecutableDigest(cfg.Command)
			if digestErr != nil || digest != cfg.ExecutableDigest {
				return nil, ErrCommand
			}
		}
		cfg.commandInfo, cfg.dirInfo = commandInfo, dirInfo
		byName[cfg.Name], checks[cfg.Name] = cfg, check
		definitions = append(definitions, commandDefinitionFor(cfg.Name))
	}
	checkAll := func(values ...string) error {
		for _, check := range checks {
			if err := check(values...); err != nil {
				return err
			}
		}
		return nil
	}
	return NewTrustedHost(definitions, func(ctx context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
		cfg, ok := byName[call.Tool]
		if !ok {
			return api.ToolResult{}, ErrCommand
		}
		result, err := runCommand(ctx, cfg, checks[call.Tool])
		if err != nil {
			return api.ToolResult{}, err
		}
		return api.ToolResult{ID: call.ID, Output: result}, nil
	}, checkAll)
}

func normalizeCommandOutput(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, value)
}

func commandResult(exit int, stdout, stderr []byte, check func(...string) error) (map[string]any, error) {
	if !utf8.Valid(stdout) || !utf8.Valid(stderr) {
		return nil, ErrCommand
	}
	rawOut, rawErr := string(stdout), string(stderr)
	cleanOut, cleanErr := normalizeCommandOutput(rawOut), normalizeCommandOutput(rawErr)
	if check(rawOut, rawErr, rawOut+rawErr, cleanOut, cleanErr) != nil {
		return nil, ErrCommand
	}
	result := map[string]any{"exitCode": exit, "succeeded": exit == 0}
	if cleanOut != "" {
		result["stdout"] = cleanOut
	}
	if cleanErr != "" {
		result["stderr"] = cleanErr
	}
	if CheckJSON(result, check) != nil {
		return nil, ErrCommand
	}
	return result, nil
}
