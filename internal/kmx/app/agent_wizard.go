package app

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"golang.org/x/term"
)

var errCreateCancelled = errors.New("agent creation cancelled")

func (a *App) CreateAgentInteractive(opt CreateOptions) error {
	errFile, visible := a.Err.(*os.File)
	if a.Stdin == nil || !visible || !term.IsTerminal(int(a.Stdin.Fd())) || !term.IsTerminal(int(errFile.Fd())) {
		return fmt.Errorf("kmx agent create needs a name in non-interactive input: kmx agent create <name> [flags]")
	}
	completed, err := collectCreateOptions(bufio.NewScanner(a.Stdin), a.Err, opt)
	if errors.Is(err, errCreateCancelled) {
		a.notef("Agent creation cancelled. Nothing was written or applied.")
		return nil
	}
	if err != nil {
		return err
	}
	return a.CreateAgent(completed)
}

func collectCreateOptions(scanner lineScanner, out io.Writer, opt CreateOptions) (CreateOptions, error) {
	var err error
	if opt.Description == "" {
		opt.Description, err = promptValue(scanner, out, "Describe this agent", "", true)
		if err != nil {
			return opt, err
		}
	}

	defaultName := opt.Name
	if defaultName == "" {
		defaultName = slugAgentName(opt.Description)
	}
	for {
		opt.Name, err = promptValue(scanner, out, "Agent name", defaultName, true)
		if err != nil {
			return opt, err
		}
		if err := scaffold.ValidateName(opt.Name); err != nil {
			fmt.Fprintf(out, "  %v\n", err)
			defaultName = ""
			continue
		}
		break
	}
	if _, err := scaffold.ParseTools(opt.Tools); err != nil {
		return opt, err
	}
	if opt.Instructions == "" {
		opt.InstructionText = "You are " + opt.Name + ". Your purpose is: " + opt.Description + "\nAnswer briefly and say plainly when you do not know something."
	}
	if opt.Namespace == "" {
		opt.Namespace = config.DefaultNamespace
	}
	if opt.Out == "" {
		opt.Out = filepath.Join("agents", opt.Name+".yaml")
	}
	if opt.Out == "-" || opt.NoApply {
		opt.NoApply = true
		return opt, nil
	}
	if opt.DryRun {
		return opt, nil
	}
	for {
		apply, err := promptValue(scanner, out, "Create and apply to "+opt.Namespace+"? (Y/n)", "y", true)
		if err != nil {
			return opt, err
		}
		switch strings.ToLower(apply) {
		case "y", "yes":
			opt.NoApply = false
			return opt, nil
		case "n", "no":
			return opt, errCreateCancelled
		default:
			fmt.Fprintln(out, "  Answer y or n.")
		}
	}
}

func promptValue(scanner lineScanner, out io.Writer, label, fallback string, required bool) (string, error) {
	for {
		if fallback == "" {
			fmt.Fprintf(out, "%s: ", label)
		} else {
			fmt.Fprintf(out, "%s [%s]: ", label, fallback)
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		value := strings.TrimSpace(scanner.Text())
		if value == "" {
			value = fallback
		}
		if value != "" || !required {
			return value, nil
		}
		fmt.Fprintln(out, "  A value is required.")
	}
}

func slugAgentName(description string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(description) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
		if b.Len() >= 50 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func (a *App) EditAgent(name, path string) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}
	if path == "" {
		path = filepath.Join("agents", name+".yaml")
	}
	originalInfo, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no local agent source at %s; `kmx agent edit` edits source, not the live cluster\n  live edit: kubectl --context %s -n kagent edit agent %s", path, shellArg(a.Cfg.KubeContext), shellArg(name))
		}
		return err
	}
	if originalInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to replace its target or convert it into a regular file", path)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	originalAgent, err := a.validateAgentEdit(path, name)
	if err != nil {
		return fmt.Errorf("existing agent source must validate before it can be edited: %w", err)
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return fmt.Errorf("set VISUAL or EDITOR to use `kmx agent edit`")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".kmx-agent-edit-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeCandidate := true
	defer func() {
		if removeCandidate {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(original); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	cmd := exec.Command(fields[0], append(fields[1:], tmpPath)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.Stdin, a.Out, a.Err
	if err := cmd.Run(); err != nil {
		removeCandidate = false
		return fmt.Errorf("editor failed: %w\n  edited candidate retained at %s", err, tmpPath)
	}
	candidate, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	if string(candidate) == string(original) {
		a.notef("unchanged: %s", path)
		return nil
	}
	if err := scaffold.RefuseKeyShapes(string(candidate)); err != nil {
		return err
	}
	edited, err := a.validateAgentEdit(tmpPath, name)
	if err != nil {
		removeCandidate = false
		return fmt.Errorf("%w\n  edited candidate retained at %s", err, tmpPath)
	}
	if edited.Namespace != originalAgent.Namespace {
		removeCandidate = false
		return fmt.Errorf("edited Agent changed namespace from %q to %q; refusing to change source identity\n  edited candidate retained at %s", originalAgent.Namespace, edited.Namespace, tmpPath)
	}
	if err := a.preflightModelConfig(edited.ModelConfig, edited.Namespace); err != nil {
		removeCandidate = false
		return fmt.Errorf("%w\n  edited candidate retained at %s", err, tmpPath)
	}
	for _, tools := range edited.Tools {
		if err := a.preflightTools(tools, edited.Namespace); err != nil {
			removeCandidate = false
			return fmt.Errorf("%w\n  edited candidate retained at %s", err, tmpPath)
		}
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(originalInfo, currentInfo) || sha256.Sum256(current) != sha256.Sum256(original) {
		removeCandidate = false
		return fmt.Errorf("%s changed while the editor was open; refusing to overwrite it\n  edited candidate retained at %s", path, tmpPath)
	}
	if err := os.Chmod(tmpPath, originalInfo.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	removeCandidate = false
	fmt.Fprintf(a.Out, "updated %s\n", path)
	a.notef("Not applied. Review the diff, then:\n  kubectl --context %s apply -f %s", shellArg(a.Cfg.KubeContext), shellArg(path))
	return nil
}

type editedAgent struct {
	Namespace   string
	ModelConfig string
	Tools       []*scaffold.ToolWiring
}

func (a *App) validateAgentEdit(path, name string) (*editedAgent, error) {
	raw, err := a.Run.Capture("kubectl", "create", "--dry-run=client", "-f", path, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("edited agent is not valid Kubernetes YAML: %w", err)
	}
	var resource struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			Type        string `json:"type"`
			Declarative struct {
				ModelConfig string `json:"modelConfig"`
				Tools       []struct {
					MCPServer struct {
						Name      string   `json:"name"`
						ToolNames []string `json:"toolNames"`
					} `json:"mcpServer"`
				} `json:"tools"`
			} `json:"declarative"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(raw), &resource); err != nil {
		return nil, fmt.Errorf("kubectl returned invalid JSON for edited agent: %w", err)
	}
	if resource.APIVersion != "kagent.dev/v1alpha2" || resource.Kind != "Agent" || resource.Metadata.Name != name {
		return nil, fmt.Errorf("edited source must remain kagent.dev/v1alpha2 kind Agent named %q", name)
	}
	if resource.Metadata.Namespace == "" || resource.Spec.Type != "Declarative" || resource.Spec.Declarative.ModelConfig == "" {
		return nil, fmt.Errorf("edited Agent must retain its namespace, Declarative type, and modelConfig")
	}
	edited := &editedAgent{Namespace: resource.Metadata.Namespace, ModelConfig: resource.Spec.Declarative.ModelConfig}
	for _, tool := range resource.Spec.Declarative.Tools {
		if tool.MCPServer.Name == "" || len(tool.MCPServer.ToolNames) == 0 {
			return nil, fmt.Errorf("edited Agent has an MCP server without a name and explicit toolNames allowlist")
		}
		parsed, err := scaffold.ParseTools(tool.MCPServer.Name + ":" + strings.Join(tool.MCPServer.ToolNames, ","))
		if err != nil {
			return nil, err
		}
		edited.Tools = append(edited.Tools, parsed)
	}
	return edited, nil
}
