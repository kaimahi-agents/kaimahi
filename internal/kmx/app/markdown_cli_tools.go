package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

const cliToolAdapter = "kmx.cli-binding/v1"

type CLIToolArg struct {
	Literal  string `json:"literal,omitempty"`
	Argument string `json:"argument,omitempty"`
}

func (a CLIToolArg) MarshalYAML() (any, error) {
	if a.Argument != "" {
		return map[string]string{"argument": a.Argument}, nil
	}
	return a.Literal, nil
}

func (a *CLIToolArg) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
		a.Literal = n.Value
		return nil
	}
	if n.Kind == yaml.MappingNode && len(n.Content) == 2 && n.Content[0].Value == "argument" && n.Content[1].Tag == "!!str" {
		a.Argument = n.Content[1].Value
		if a.Argument != "" {
			return nil
		}
	}
	return errors.New("args must be strings or {argument: NAME}")
}

type CLIToolParameter struct {
	Type        string   `yaml:"type" json:"type"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Pattern     string   `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Enum        []string `yaml:"enum,omitempty" json:"enum,omitempty"`
	MaxLength   int      `yaml:"max-length,omitempty" json:"maxLength,omitempty"`
	Minimum     *int     `yaml:"minimum,omitempty" json:"minimum,omitempty"`
	Maximum     *int     `yaml:"maximum,omitempty" json:"maximum,omitempty"`
}
type CLIToolProbe struct {
	Args          []string `yaml:"args" json:"args"`
	ExitCode      int      `yaml:"exit-code" json:"exitCode"`
	StdoutPattern string   `yaml:"stdout-pattern" json:"stdoutPattern"`
	Timeout       string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}
type CLIToolSpec struct {
	Name        string                      `yaml:"name" json:"name"`
	Description string                      `yaml:"description" json:"description"`
	Command     string                      `yaml:"command" json:"command"`
	Args        []CLIToolArg                `yaml:"args" json:"args"`
	Parameters  map[string]CLIToolParameter `yaml:"parameters" json:"parameters"`
	Execution   struct {
		Timeout        string `yaml:"timeout" json:"timeout"`
		MaxOutputBytes int    `yaml:"max-output-bytes" json:"maxOutputBytes"`
		Approval       string `yaml:"approval" json:"approval"`
	} `yaml:"execution" json:"execution"`
	Validation *CLIToolProbe `yaml:"validation,omitempty" json:"validation,omitempty"`
}
type ApprovedCLITool struct {
	Spec             CLIToolSpec `json:"spec"`
	Executable       string      `json:"executable"`
	ExecutableDigest string      `json:"executableDigest"`
}

func cliDuration(value string, defaultValue, maxValue time.Duration) (time.Duration, error) {
	if value == "" {
		return defaultValue, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 || d > maxValue {
		return 0, errors.New("invalid bounded command timeout")
	}
	return d, nil
}
func cliText(s string) bool {
	return !strings.ContainsAny(s, "\x00\r\n") && safeTerminal(s) == s && secretshapes.Match(s) == nil
}
func validateCLISpec(s CLIToolSpec) error {
	if !markdownAgentName.MatchString(s.Name) || strings.TrimSpace(s.Description) == "" || !cliText(s.Description) || len(s.Description) > 1024 {
		return errors.New("tool requires a safe name and bounded description")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`).MatchString(s.Command) {
		return errors.New("command must be a PATH executable name, not a path or shell expression")
	}
	if len(s.Args) == 0 || len(s.Args) > 64 || s.Parameters == nil || len(s.Parameters) > 16 {
		return errors.New("declare 1-64 args and a parameters mapping (maximum 16)")
	}
	if _, err := cliDuration(s.Execution.Timeout, 15*time.Second, 2*time.Minute); err != nil {
		return err
	}
	if s.Execution.MaxOutputBytes < 1 || s.Execution.MaxOutputBytes > 64<<10 {
		return errors.New("max-output-bytes must be 1-65536")
	}
	if s.Execution.Approval != "granted-scope" {
		return errors.New("this slice supports approval: granted-scope only; approve the exact binding explicitly before running")
	}
	used := map[string]bool{}
	for _, arg := range s.Args {
		if arg.Argument != "" {
			if arg.Literal != "" {
				return errors.New("argument cannot also contain literal text")
			}
			if _, ok := s.Parameters[arg.Argument]; !ok {
				return errors.New("argument references unknown parameter")
			}
			used[arg.Argument] = true
		} else if len(arg.Literal) > 4096 || !cliText(arg.Literal) {
			return errors.New("invalid fixed argument")
		}
	}
	// A model-selected first token could change the subcommand rather than an
	// operand. Require a reviewed literal first argument for this initial wrapper.
	if s.Args[0].Argument != "" {
		return errors.New("the first command argument must be a fixed subcommand or flag")
	}
	for name, p := range s.Parameters {
		if !markdownAgentName.MatchString(name) || !used[name] || !cliText(p.Description) {
			return errors.New("each parameter needs a safe name and an argument reference")
		}
		switch p.Type {
		case "string":
			if p.Minimum != nil || p.Maximum != nil || p.MaxLength < 0 || p.MaxLength > 4096 || len(p.Enum) > 64 {
				return errors.New("invalid string parameter constraints")
			}
			if p.Pattern == "" && len(p.Enum) == 0 {
				return errors.New("string parameters require an anchored pattern or explicit enum")
			}
			if p.Pattern != "" {
				if !strings.HasPrefix(p.Pattern, "^") || !strings.HasSuffix(p.Pattern, "$") || len(p.Pattern) > 512 {
					return errors.New("parameter patterns must be anchored ^...$ and at most 512 bytes")
				}
				if _, err := regexp.Compile(p.Pattern); err != nil {
					return errors.New("invalid parameter pattern")
				}
			}
			for _, v := range p.Enum {
				if !cliText(v) || v == "" || strings.HasPrefix(v, "-") {
					return errors.New("enum values must be literal non-option strings")
				}
			}
		case "integer":
			if p.Pattern != "" || len(p.Enum) > 0 || p.MaxLength != 0 || p.Minimum == nil || p.Maximum == nil || *p.Minimum < 0 || *p.Maximum < *p.Minimum {
				return errors.New("integer parameters require nonnegative minimum and maximum")
			}
		default:
			return errors.New("CLI parameters support string or integer only")
		}
	}
	if s.Validation != nil {
		v := s.Validation
		if len(v.Args) < 1 || len(v.Args) > 16 || v.ExitCode < 0 || v.ExitCode > 255 || v.StdoutPattern == "" || len(v.StdoutPattern) > 512 {
			return errors.New("validation requires fixed args, exit-code 0-255 and stdout-pattern")
		}
		for _, arg := range v.Args {
			if !cliText(arg) || len(arg) > 4096 {
				return errors.New("validation args must be fixed literal strings")
			}
		}
		if _, err := regexp.Compile(v.StdoutPattern); err != nil {
			return errors.New("invalid validation output pattern")
		}
		if _, err := cliDuration(v.Timeout, 5*time.Second, 15*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// Only a final dedicated section is executable metadata. Ordinary code fences
// in the instructions are retained; a binding fence outside the section refuses.
func extractCLITools(path string, lines []string, start int) (string, []CLIToolSpec, error) {
	section := len(lines)
	fence := ""
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if fence != "" {
			if line == fence {
				fence = ""
			}
			continue
		}
		if line == "## Tool bindings" {
			section = i
			break
		}
		if line == "```yaml kmx-tool" {
			return "", nil, mdError(path, i+1, "tool-section", "put kmx-tool blocks in a final ## Tool bindings section")
		}
		if strings.HasPrefix(line, "```") {
			fence = "```"
		} else if strings.HasPrefix(line, "~~~") {
			fence = "~~~"
		}
	}
	instructions := strings.TrimSpace(strings.Join(lines[start:section], "\n"))
	specs := []CLIToolSpec{}
	seen := map[string]bool{}
	for i := section + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if strings.TrimSpace(lines[i]) != "```yaml kmx-tool" {
			return "", nil, mdError(path, i+1, "tool-section", "Tool bindings accepts only yaml kmx-tool fences")
		}
		begin := i + 1
		i++
		for i < len(lines) && strings.TrimSpace(lines[i]) != "```" {
			i++
		}
		if i == len(lines) {
			return "", nil, mdError(path, begin, "tool-fence", "close tool fence with ```")
		}
		b := []byte(strings.Join(lines[begin:i], "\n"))
		var node yaml.Node
		if yaml.Unmarshal(b, &node) != nil || mdNodeCheck(&node) != nil {
			return "", nil, mdError(path, begin+1, "tool", "invalid YAML, duplicates or aliases in tool binding")
		}
		d := yaml.NewDecoder(bytes.NewReader(b))
		d.KnownFields(true)
		var s CLIToolSpec
		var extra any
		if d.Decode(&s) != nil || !errors.Is(d.Decode(&extra), io.EOF) {
			return "", nil, mdError(path, begin+1, "tool", "use one closed CLI binding document; check field names and types")
		}
		if err := validateCLISpec(s); err != nil {
			return "", nil, mdError(path, begin+1, "tool", err.Error())
		}
		if seen[s.Name] || len(specs) >= 8 {
			return "", nil, mdError(path, begin+1, "tool", "use unique tool names and at most eight bindings")
		}
		seen[s.Name] = true
		specs = append(specs, s)
	}
	if section < len(lines) && len(specs) == 0 {
		return "", nil, mdError(path, section+1, "tool", "add at least one kmx-tool block")
	}
	return instructions, specs, nil
}
func resolveCLICommand(name string) (string, string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", "", fmt.Errorf("%s not found on PATH; install it separately", name)
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", "", err
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return "", "", err
	}
	digest, err := executableDigest(p)
	return p, digest, err
}
func validateCLIGrant(g MarkdownToolGrant) error {
	if g.SchemaVersion != 1 || !filepath.IsAbs(g.Source) || !agentVersionDigest.MatchString(g.AgentDigest) || len(g.CLI) < 1 || len(g.CLI) > 8 || len(g.Tools) != len(g.CLI) || g.Repository != "" || g.Executable != "" || g.ExecutableDigest != "" {
		return errors.New("invalid CLI tool grant")
	}
	seen := map[string]bool{}
	for _, tool := range g.CLI {
		if err := validateCLISpec(tool.Spec); err != nil {
			return err
		}
		if !filepath.IsAbs(tool.Executable) || !agentVersionDigest.MatchString(tool.ExecutableDigest) || seen[tool.Spec.Name] || !slices.Contains(g.Tools, tool.Spec.Name) {
			return errors.New("invalid CLI executable binding")
		}
		seen[tool.Spec.Name] = true
	}
	return nil
}
func checkCLIIdentity(tool ApprovedCLITool) error {
	path, digest, err := resolveCLICommand(tool.Spec.Command)
	if err != nil {
		return err
	}
	if path != tool.Executable || digest != tool.ExecutableDigest {
		return fmt.Errorf("%s PATH resolution or executable changed; approve the tool again", tool.Spec.Name)
	}
	return nil
}

type CLIToolApproval struct {
	Grant        MarkdownToolGrant `json:"grant"`
	BaseGrant    string            `json:"baseGrant"`
	ProbeResults []string          `json:"probeResults,omitempty"`
}

func PrepareCLIToolApproval(path string) (CLIToolApproval, error) {
	var plan CLIToolApproval
	a, err := LoadMarkdownAgent(path, "")
	if err != nil {
		return plan, err
	}
	if len(a.CLITools) == 0 {
		return plan, errors.New("agent has no ## Tool bindings; add a yaml kmx-tool block")
	}
	_, base, err := readCurrentToolGrant(a.Path)
	if err != nil {
		return plan, err
	}
	g := MarkdownToolGrant{SchemaVersion: 1, Source: a.Path, AgentDigest: a.Revision, Adapter: cliToolAdapter, Tools: append([]string{}, a.Tools...)}
	for _, s := range a.CLITools {
		exe, digest, err := resolveCLICommand(s.Command)
		if err != nil {
			return plan, err
		}
		g.CLI = append(g.CLI, ApprovedCLITool{Spec: s, Executable: exe, ExecutableDigest: digest})
	}
	plan.Grant, plan.BaseGrant = g, base
	return plan, validateCLIGrant(g)
}
func RenderCLIApproval(out io.Writer, p CLIToolApproval) error {
	fmt.Fprintf(out, "Local executable tool approval\nDefinition: %s\nSource: %s\n", p.Grant.AgentDigest, safeTerminal(p.Grant.Source))
	for _, tool := range p.Grant.CLI {
		spec, _ := yaml.Marshal(tool.Spec)
		fmt.Fprintf(out, "\n%s\nResolved: %s\nExecutable hash: %s\n", spec, safeTerminal(tool.Executable), tool.ExecutableDigest)
	}
	_, err := fmt.Fprintln(out, "Execution runs as your local user with inherited credentials, in an isolated temporary directory. These commands are not sandboxed. Regex constrains arguments, not the binary's authority. Approval enables this exact scope unattended. Validation probes are executable commands too; they run only after confirmation. A version match is not login or service-health proof.")
	return err
}

// Process cancellation covers the Unix process group, including script children.
func runBoundCLI(ctx context.Context, path string, args []string, timeout time.Duration, limit int) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "kmx-cli-tool-")
	if err != nil {
		return nil, -1, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = cliToolEnv()
	cmd.Stdin = nil
	cmd.WaitDelay = time.Second
	configureCLIProcess(cmd)
	out := &cliToolOutput{remaining: limit}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	err = cmd.Run()
	if out.overflow {
		return nil, -1, errors.New("tool output limit exceeded")
	}
	if ctx.Err() != nil {
		return nil, -1, errors.New("tool command timed out or was cancelled")
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out.buffer.Bytes(), exit.ExitCode(), nil
		}
		return nil, -1, errors.New("tool command failed or output limit exceeded")
	}
	return out.buffer.Bytes(), 0, nil
}

type cliToolOutput struct {
	buffer    bytes.Buffer
	remaining int
	overflow  bool
}

func (b *cliToolOutput) Write(data []byte) (int, error) {
	if len(data) > b.remaining {
		b.overflow = true
		return 0, errors.New("tool output limit exceeded")
	}
	b.remaining -= len(data)
	return b.buffer.Write(data)
}
func cliToolEnv() []string {
	env := []string{}
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		switch key {
		case "BASH_ENV", "ENV", "PAGER", "GH_PAGER", "GH_DEBUG", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES":
			continue
		}
		env = append(env, e)
	}
	return append(env, "NO_COLOR=1", "AZURE_CORE_NO_COLOR=true", "AZURE_CORE_ONLY_SHOW_ERRORS=true")
}
func runCLIProbe(ctx context.Context, tool ApprovedCLITool) error {
	if err := checkCLIIdentity(tool); err != nil {
		return err
	}
	v := tool.Spec.Validation
	if v == nil {
		return nil
	}
	timeout, _ := cliDuration(v.Timeout, 5*time.Second, 15*time.Second)
	out, code, err := runBoundCLI(ctx, tool.Executable, v.Args, timeout, 16384)
	if err != nil {
		return fmt.Errorf("%s validation: %w", tool.Spec.Name, err)
	}
	if code != v.ExitCode || !regexp.MustCompile(v.StdoutPattern).Match(out) {
		return fmt.Errorf("%s validation failed: expected exit %d and stdout-pattern match (output withheld)", tool.Spec.Name, v.ExitCode)
	}
	return nil
}
func ApplyCLIToolApproval(ctx context.Context, p *CLIToolApproval) error {
	if err := validateCLIGrant(p.Grant); err != nil {
		return err
	}
	root, current, err := mdToolPaths(p.Grant.Source)
	if err != nil {
		return err
	}
	return mdStateUpdate(current, func() ([]byte, error) {
		a, err := LoadMarkdownAgent(p.Grant.Source, "")
		if err != nil {
			return nil, err
		}
		if a.Revision != p.Grant.AgentDigest {
			return nil, errors.New("source changed after review; no probe or grant applied")
		}
		_, base, err := readCurrentToolGrant(a.Path)
		if err != nil {
			return nil, err
		}
		if base != p.BaseGrant {
			return nil, errors.New("binding changed during review")
		}
		if len(a.CLITools) != len(p.Grant.CLI) {
			return nil, errors.New("grant does not match declared bindings")
		}
		for i, tool := range p.Grant.CLI {
			if mdJSONHash(tool.Spec) != mdJSONHash(a.CLITools[i]) {
				return nil, errors.New("grant changed after review")
			}
			if err = runCLIProbe(ctx, tool); err != nil {
				return nil, err
			}
			if tool.Spec.Validation != nil {
				p.ProbeResults = append(p.ProbeResults, tool.Spec.Name+": passed")
			} else {
				p.ProbeResults = append(p.ProbeResults, tool.Spec.Name+": no probe declared")
			}
		}
		if err = a.CheckCurrent(); err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(p.Grant)
		if err != nil {
			return nil, err
		}
		digest := mdJSONHash(p.Grant)
		if err = mdStateUpdate(filepath.Join(root, "snapshots", strings.TrimPrefix(digest, "sha256:")+".json"), func() ([]byte, error) { return raw, nil }); err != nil {
			return nil, err
		}
		return raw, nil
	})
}

func cliInvocationArgs(s CLIToolSpec, value any) ([]string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("invalid tool arguments")
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil || len(values) != len(s.Parameters) {
		return nil, errors.New("provide exactly the declared tool parameters")
	}
	rendered := map[string]string{}
	for name, p := range s.Parameters {
		b, ok := values[name]
		if !ok {
			return nil, errors.New("missing required tool parameter")
		}
		switch p.Type {
		case "string":
			var v string
			if json.Unmarshal(b, &v) != nil || v == "" || strings.HasPrefix(v, "-") || !cliText(v) {
				return nil, errors.New("argument_not_allowed: expected a literal non-option string")
			}
			max := p.MaxLength
			if max == 0 {
				max = 256
			}
			if len(v) > max {
				return nil, errors.New("argument_not_allowed: string exceeds length limit")
			}
			if len(p.Enum) > 0 && !slices.Contains(p.Enum, v) {
				return nil, errors.New("argument_not_allowed: value outside enum")
			}
			if p.Pattern != "" && !regexp.MustCompile(p.Pattern).MatchString(v) {
				return nil, errors.New("argument_not_allowed: value does not match approved pattern")
			}
			rendered[name] = v
		case "integer":
			var v int
			if json.Unmarshal(b, &v) != nil || v < *p.Minimum || v > *p.Maximum {
				return nil, errors.New("argument_not_allowed: integer outside approved bounds")
			}
			rendered[name] = strconv.Itoa(v)
		}
	}
	args := make([]string, 0, len(s.Args))
	for _, arg := range s.Args {
		if arg.Argument != "" {
			args = append(args, rendered[arg.Argument])
		} else {
			args = append(args, arg.Literal)
		}
	}
	return args, nil
}
func executeCLITool(ctx context.Context, tool ApprovedCLITool, input any) (string, error) {
	if err := validateCLISpec(tool.Spec); err != nil {
		return "", err
	}
	args, err := cliInvocationArgs(tool.Spec, input)
	if err != nil {
		return "", err
	}
	if err = checkCLIIdentity(tool); err != nil {
		return "", err
	}
	duration, _ := cliDuration(tool.Spec.Execution.Timeout, 15*time.Second, 2*time.Minute)
	out, code, err := runBoundCLI(ctx, tool.Executable, args, duration, tool.Spec.Execution.MaxOutputBytes)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("tool exited with code %d (diagnostics withheld)", code)
	}
	if secretshapes.Match(string(out)) != nil {
		return "", errors.New("tool output withheld: credential-shaped content")
	}
	return safeTerminal(string(out)), nil
}
func cliToolParameters(s CLIToolSpec) map[string]any {
	properties := map[string]any{}
	required := []string{}
	for name, p := range s.Parameters {
		value := map[string]any{"type": p.Type, "description": p.Description}
		if p.Type == "string" {
			max := p.MaxLength
			if max == 0 {
				max = 256
			}
			value["maxLength"] = max
			value["minLength"] = 1
			if p.Pattern != "" {
				value["pattern"] = p.Pattern
			}
			if len(p.Enum) > 0 {
				value["enum"] = p.Enum
			}
		} else {
			value["minimum"], value["maximum"] = *p.Minimum, *p.Maximum
		}
		properties[name] = value
		required = append(required, name)
	}
	slices.Sort(required)
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func cliReadTools(g MarkdownToolGrant, budget *atomic.Int32) []copilot.Tool {
	tools := []copilot.Tool{}
	for _, approved := range g.CLI {
		tool := approved
		tools = append(tools, copilot.Tool{Name: tool.Spec.Name, Description: tool.Spec.Description, Parameters: cliToolParameters(tool.Spec), SkipPermission: true, Handler: func(i copilot.ToolInvocation) (copilot.ToolResult, error) {
			if budget.Add(-1) < 0 {
				return copilot.ToolResult{TextResultForLLM: "Tool budget exceeded (8 calls per turn)", ResultType: "failure"}, nil
			}
			ctx := i.TraceContext
			if ctx == nil {
				ctx = context.Background()
			}
			text, err := executeCLITool(ctx, tool, i.Arguments)
			if err != nil {
				return copilot.ToolResult{TextResultForLLM: err.Error(), ResultType: "failure"}, nil
			}
			return copilot.ToolResult{TextResultForLLM: text, ResultType: "success"}, nil
		}})
	}
	return tools
}
