package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

const markdownAgentLimit = 64 << 10

var markdownAgentName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

type MarkdownDiagnostic struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Code   string `json:"code"`
	Repair string `json:"repair"`
}

func (d *MarkdownDiagnostic) Error() string {
	return fmt.Sprintf("%s:%d:%d [%s]: %s", safeTerminal(d.Path), d.Line, d.Column, d.Code, d.Repair)
}
func mdError(path string, line int, code, repair string) error {
	return &MarkdownDiagnostic{path, max(1, line), 1, code, repair}
}

// MarkdownAgent owns a frozen native definition and its separately authored tests.
// Hashes identify source bytes, not model determinism or deployment authority.
type MarkdownAgent struct {
	Unmapped                                                                                       []string
	ToolsOmitted                                                                                   bool
	ToolsPath, ToolsRevision                                                                       string
	ToolSources, TestSources                                                                       map[string][]byte
	CasePaths                                                                                      map[string]string
	CLITools                                                                                       []CLIToolSpec
	Grant                                                                                          *MarkdownToolGrant
	BindingDigest                                                                                  string
	Path, TestsPath, Name, Description, Model, Instructions, Revision, TestsRevision, PairRevision string
	Tools                                                                                          []string
	Examples                                                                                       []agentruntime.EvaluationCase
	Mocks                                                                                          map[string][]MarkdownMock
	Lines                                                                                          map[string]int
	Source, TestSource                                                                             []byte
}
type MarkdownMockCall struct {
	Arguments map[string]any `yaml:"arguments" json:"arguments"`
	Result    any            `yaml:"result" json:"result"`
}
type MarkdownMock struct {
	Name        string             `yaml:"name" json:"name"`
	Description string             `yaml:"description" json:"description"`
	Parameters  map[string]any     `yaml:"parameters" json:"parameters"`
	Calls       []MarkdownMockCall `yaml:"calls" json:"calls"`
	schema      *jsonschema.Schema
}

func mdHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func mdJSONHash(v any) string  { b, _ := json.Marshal(v); b, _ = jcs.Transform(b); return mdHash(b) }

func mdRead(path string, limit int) ([]byte, error) {
	f, err := mdOpenRead(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errors.New("requires a bounded regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	if err != nil || len(b) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return b, nil
}

func MarkdownTestsPath(path string) string {
	return markdownStem(path) + ".tests.md"
}
func ReadMarkdownAgent(path string) (*MarkdownAgent, error) { return LoadMarkdownAgent(path, "") }
func LoadMarkdownAgent(path, tests string) (*MarkdownAgent, error) {
	var resolveErr error
	path, resolveErr = ResolveMarkdownSource(path)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if !strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".tests.md") || strings.HasSuffix(path, ".agent.test.md") {
		return nil, mdError(path, 1, "extension", "select an agent Markdown definition, not a test file")
	}
	path, err := canonicalMDSource(path)
	if err != nil {
		return nil, err
	}
	raw, err := mdRead(path, markdownAgentLimit)
	if err != nil {
		return nil, mdError(path, 1, "read", "select a regular, non-symlink UTF-8 file of at most 64 KiB")
	}
	a, err := parseMarkdownAgent(path, raw)
	if err != nil {
		return nil, err
	}
	if err := loadMarkdownTools(a); err != nil {
		return nil, err
	}
	explicit := tests != ""
	if tests != "" {
		absolute, _ := filepath.Abs(tests)
		explicit = absolute != MarkdownTestsPath(path)
	}
	if !explicit {
		tests = MarkdownTestsPath(path)
	}
	if tests != "" {
		tests, err = filepath.Abs(tests)
		if err != nil {
			return nil, err
		}
		a.TestsPath = tests
		b, e := mdRead(tests, 128<<10)
		if e != nil && (!errors.Is(e, os.ErrNotExist) || explicit) {
			return nil, mdError(tests, 1, "read", "select a regular test file of at most 128 KiB")
		}
		if e == nil {
			if err = loadMarkdownTestFiles(a, b); err != nil {
				return nil, err
			}
		}
	}
	a.PairRevision = mdJSONHash(struct{ Schema, Agent, Tools, Tests string }{"kmx.agent-pair/v2", a.Revision, a.ToolsRevision, a.TestsRevision})
	if err = a.CheckCurrent(); err != nil {
		return nil, err
	}
	return a, nil
}

// Canonicalize existing parent directories while retaining the final component:
// mdRead must still refuse an agent-looking symlink to another file.
func canonicalMDSource(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
func (a *MarkdownAgent) CheckCurrent() error {
	raw, err := mdRead(a.Path, markdownAgentLimit)
	if err != nil || mdHash(raw) != a.Revision {
		return errors.New("agent definition changed; reload before running")
	}
	if a.ToolsPath != "" {
		if err := checkMarkdownSources(a.ToolsPath, a.ToolSources, 128<<10); err != nil {
			return err
		}
	}
	if a.TestSources != nil {
		return checkMarkdownSources(a.TestsPath, a.TestSources, 128<<10)
	}
	if a.TestsPath != "" {
		b, e := mdRead(a.TestsPath, 128<<10)
		if a.TestsRevision == "" && errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil || mdHash(b) != a.TestsRevision {
			return errors.New("paired tests changed; reload before running")
		}
	}
	return nil
}

func mdNodeCheck(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("aliases and anchors are unsupported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Tag != "!!str" || seen[k.Value] {
				return errors.New("duplicate or non-string mapping key")
			}
			seen[k.Value] = true
		}
	}
	if n.Tag != "" && !strings.HasPrefix(n.Tag, "!!") {
		return errors.New("custom YAML tags are unsupported")
	}
	if n.Kind == yaml.ScalarNode && secretshapes.Match(n.Value) != nil {
		return errors.New("remove credential-shaped content")
	}
	for _, child := range n.Content {
		if err := mdNodeCheck(child); err != nil {
			return err
		}
	}
	return nil
}
func mdFront(path string, raw []byte, limit int) (map[string]*yaml.Node, []string, int, error) {
	if len(raw) > limit || !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
		return nil, nil, 0, mdError(path, 1, "encoding", "use bounded UTF-8 text")
	}
	if secretshapes.Match(string(raw)) != nil {
		return nil, nil, 0, mdError(path, 1, "credential", "remove credential-shaped content; authentication stays outside the file")
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if lines[0] != "---" {
		if strings.HasSuffix(path, ".agent.test.md") {
			return nil, nil, 0, mdError(path, 1, "frontmatter", "add this header above your test cases:\n\n---\nschemaVersion: 1\n---\n\nPut cases in ```yaml agent-test blocks with id, input and assertions.")
		}
		name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".md"), ".agent")
		if !markdownAgentName.MatchString(name) {
			name = "my-agent"
		}
		return nil, nil, 0, mdError(path, 1, "frontmatter", fmt.Sprintf("this file has instructions but no agent header. Add this at the top and keep your instructions below it:\n\n---\nname: %s\ndescription: Describe what this agent does\ntools: []\n---\n\nname is optional (defaults to the filename); description and tools are required.\ntools: [] means no tools. model is optional and uses your Copilot default.\nFor a starter definition and paired tests: kmx create %s --type md", name, name))
	}
	end := 1
	for end < len(lines) && lines[end] != "---" {
		end++
	}
	if end == len(lines) {
		return nil, nil, 0, mdError(path, 1, "frontmatter", "close front matter with ---")
	}
	var n yaml.Node
	if yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &n) != nil || len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode {
		return nil, nil, 0, mdError(path, 2, "frontmatter", "use one YAML mapping")
	}
	if err := mdNodeCheck(&n); err != nil {
		return nil, nil, 0, mdError(path, 2, "frontmatter", err.Error())
	}
	m := map[string]*yaml.Node{}
	r := n.Content[0]
	for i := 0; i < len(r.Content); i += 2 {
		m[r.Content[i].Value] = r.Content[i+1]
	}
	return m, lines, end, nil
}
func mdText(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return ""
	}
	return n.Value
}
func parseMarkdownAgent(path string, raw []byte) (*MarkdownAgent, error) {
	if !bytes.HasPrefix(raw, []byte("---\n")) && !bytes.HasPrefix(raw, []byte("---\r\n")) {
		parsed, err := agentfile.Parse(path, raw)
		if err != nil {
			return nil, err
		}
		a := &MarkdownAgent{Path: path, Name: parsed.Identity.ID, Description: parsed.Identity.Purpose, Model: parsed.Model, Instructions: parsed.Instructions, Tools: parsed.Tools, Source: bytes.Clone(raw), Revision: mdHash(raw), Mocks: map[string][]MarkdownMock{}, Lines: map[string]int{}}
		for _, example := range parsed.Examples {
			if example.Mocks != nil {
				return nil, errors.New("use paired tests for recorded tool fixtures in persistent agents")
			}
			a.Examples = append(a.Examples, agentruntime.EvaluationCase{ID: example.ID, Input: example.Input, Assertions: example.Assertions})
		}
		return a, nil
	}
	m, lines, end, err := mdFront(path, raw, markdownAgentLimit)
	if err != nil {
		return nil, err
	}
	var unmapped []string
	for k := range m {
		switch k {
		case "name", "description", "model", "tools":
		case "metadata", "user-invocable", "disable-model-invocation", "infer":
		default:
			unmapped = append(unmapped, safeTerminal(k))
		}
	}
	a := &MarkdownAgent{Path: path, Name: strings.TrimSuffix(filepath.Base(path), ".agent.md"), Description: mdText(m["description"]), Model: mdText(m["model"]), Source: bytes.Clone(raw), Revision: mdHash(raw), Mocks: map[string][]MarkdownMock{}, Lines: map[string]int{}}
	a.Unmapped, a.ToolsOmitted = unmapped, m["tools"] == nil
	if !strings.HasSuffix(path, ".agent.md") {
		a.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if n := m["name"]; n != nil {
		a.Name = mdText(n)
	}
	if !markdownAgentName.MatchString(a.Name) {
		return nil, mdError(path, 2, "name", "use a safe 1-63 character name")
	}
	if strings.TrimSpace(a.Description) == "" {
		a.Description = a.Name + " agent"
	}
	if n := m["model"]; n != nil && (a.Model == "" || !localModelName.MatchString(a.Model)) {
		return nil, mdError(path, n.Line+1, "model", "use a native string model ID or omit model; nested provider/id is retired")
	}
	tools := m["tools"]
	if tools == nil {
		tools = &yaml.Node{Kind: yaml.SequenceNode}
	}
	if tools.Kind == yaml.ScalarNode && tools.Tag == "!!str" {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, name := range strings.Split(tools.Value, ",") {
			list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(name)})
		}
		tools = list
	}
	if tools.Kind != yaml.SequenceNode {
		return nil, mdError(path, 2, "tools", "declare tools: [] for tool-free agents, or a list of mock-bound tool names")
	}
	a.Tools = []string{}
	seen := map[string]bool{}
	for _, n := range tools.Content {
		v := mdText(n)
		if v == "" || len(v) > 128 || !cliText(v) || seen[v] {
			return nil, mdError(path, n.Line+1, "tools", "use unique literal tool names")
		}
		a.Tools = append(a.Tools, v)
		seen[v] = true
	}
	if strings.Contains(string(raw), "```yaml kmx-tool") {
		return nil, mdError(path, end+2, "tools", "put command definitions in the .tools.yaml companion")
	}
	a.Instructions = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	if a.Instructions == "" || utf8.RuneCountInString(a.Instructions) > 30000 {
		return nil, mdError(path, end+2, "instructions", "provide 1-30000 characters of instructions")
	}
	// Give old spike users an explicit repair rather than leaking tests into prompts.
	if parsed, e := agentfile.Parse(path, raw); e == nil && len(parsed.Examples) > 0 {
		a.Instructions = parsed.Instructions
		for _, example := range parsed.Examples {
			if example.Mocks != nil {
				return nil, errors.New("use paired tests for recorded tool fixtures in persistent agents")
			}
			a.Examples = append(a.Examples, agentruntime.EvaluationCase{ID: example.ID, Input: example.Input, Assertions: example.Assertions})
		}
	}
	return a, nil
}

type mdTestCase struct {
	ID             string                             `yaml:"id"`
	Input          string                             `yaml:"input"`
	Assertions     []agentruntime.EvaluationAssertion `yaml:"assertions"`
	ExpectContains []string                           `yaml:"expectContains"`
	Mocks          struct {
		Tools []MarkdownMock `yaml:"tools"`
	} `yaml:"mocks"`
}

func parseMarkdownTests(a *MarkdownAgent, raw []byte) error {
	m, lines, end, err := mdFront(a.TestsPath, raw, 128<<10)
	if err != nil {
		return err
	}
	n := m["schemaVersion"]
	if len(m) != 1 || n == nil || n.Tag != "!!int" || n.Value != "1" {
		return mdError(a.TestsPath, 2, "version", "use only schemaVersion: 1 in test front matter")
	}
	seen := map[string]bool{}
	for i := end + 1; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "```") && !strings.HasPrefix(l, "~~~") {
			continue
		}
		if l != "```yaml agent-test" {
			return mdError(a.TestsPath, i+1, "fence", "use ```yaml agent-test for test blocks")
		}
		start := i + 1
		i++
		for i < len(lines) && strings.TrimSpace(lines[i]) != "```" {
			i++
		}
		if i == len(lines) {
			return mdError(a.TestsPath, start, "fence", "close the test block with ```")
		}
		b := []byte(strings.Join(lines[start:i], "\n"))
		var node yaml.Node
		if yaml.Unmarshal(b, &node) != nil || mdNodeCheck(&node) != nil {
			return mdError(a.TestsPath, start+1, "case", "fix malformed, duplicate or aliased YAML fields")
		}
		d := yaml.NewDecoder(bytes.NewReader(b))
		d.KnownFields(true)
		var c mdTestCase
		if d.Decode(&c) != nil {
			return mdError(a.TestsPath, start+1, "case", "use id, input, assertions/expectContains and optional mocks.tools; check field types")
		}
		var extra any
		if !errors.Is(d.Decode(&extra), io.EOF) {
			return mdError(a.TestsPath, start+1, "case", "use one YAML document per case")
		}
		ordinary := agentruntime.EvaluationCase{ID: c.ID, Input: c.Input, Assertions: c.Assertions, ExpectContains: c.ExpectContains}
		if err := agentruntime.ValidateEvaluationCase(ordinary); err != nil {
			return mdError(a.TestsPath, start+1, "assertion", safeTerminal(err.Error()))
		}
		ids := map[string]bool{}
		for _, check := range agentruntime.EvaluationCaseAssertions(ordinary) {
			if ids[check.ID] {
				return mdError(a.TestsPath, start+1, "assertion", "use unique assertion IDs")
			}
			ids[check.ID] = true
			switch check.Type {
			case "contains", "notContains", "regex", "toolCalled", "toolNotCalled":
			default:
				return mdError(a.TestsPath, start+1, "unsupported-check", "supported checks: contains, notContains, regex, toolCalled, toolNotCalled")
			}
		}
		if seen[c.ID] || len(a.Examples) >= 32 {
			return mdError(a.TestsPath, start+1, "case-id", "use unique case IDs and at most 32 cases")
		}
		seen[c.ID] = true
		caseTools := append([]string{}, a.Tools...)
		for _, mock := range c.Mocks.Tools {
			if !containsString(caseTools, mock.Name) && githubToolTemplate(mock.Name) != "" {
				caseTools = append(caseTools, mock.Name)
			}
		}
		mockTools := caseTools
		mockTools = nil
		for _, m := range c.Mocks.Tools {
			if !containsString(caseTools, m.Name) {
				return mdError(a.TestsPath, start+1, "mock", "mock tool must be declared")
			}
			mockTools = append(mockTools, m.Name)
		}
		if err := validateMarkdownMocks(mockTools, c.Mocks.Tools); err != nil {
			return mdError(a.TestsPath, start+1, "mock", err.Error())
		}
		for _, check := range c.Assertions {
			if check.Tool != "" && !containsString(caseTools, check.Tool) {
				return mdError(a.TestsPath, start+1, "tool", "assertion tool must be declared by the agent")
			}
		}
		a.Examples = append(a.Examples, ordinary)
		a.Mocks[c.ID] = c.Mocks.Tools
		a.Lines[c.ID] = start + 1
	}
	a.TestSource = bytes.Clone(raw)
	a.TestsRevision = mdHash(raw)
	return nil
}
func containsString(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
func validateMarkdownMocks(tools []string, mocks []MarkdownMock) error {
	seen := map[string]bool{}
	for i := range mocks {
		m := &mocks[i]
		if !containsString(tools, m.Name) || seen[m.Name] || strings.TrimSpace(m.Description) == "" || len(m.Calls) > 16 {
			return errors.New("mock needs a unique declared tool, description and at most 16 expected calls")
		}
		seen[m.Name] = true
		// Local fixtures never resolve schemas through the network or filesystem.
		var refs func(any) bool
		refs = func(v any) bool {
			switch x := v.(type) {
			case map[string]any:
				for k, v := range x {
					if k == "$ref" || k == "$dynamicRef" {
						return true
					}
					if refs(v) {
						return true
					}
				}
			case []any:
				for _, v := range x {
					if refs(v) {
						return true
					}
				}
			}
			return false
		}
		if m.Parameters["type"] != "object" || refs(m.Parameters) {
			return errors.New("mock parameters require an object JSON schema without references")
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("urn:kmx:mock", m.Parameters); err != nil {
			return errors.New("invalid mock parameter schema")
		}
		schema, err := compiler.Compile("urn:kmx:mock")
		if err != nil {
			return errors.New("invalid mock parameter schema")
		}
		m.schema = schema
		for _, call := range m.Calls {
			if call.Arguments == nil || call.Result == nil || schema.Validate(call.Arguments) != nil {
				return errors.New("mock calls require schema-valid arguments and a non-null result")
			}
			if _, err := json.Marshal(call); err != nil {
				return errors.New("mock arguments and results must be JSON-compatible values")
			}
		}
	}
	if len(seen) != len(tools) {
		return errors.New("every declared tool needs a mock for each case; real tool fallback is disabled")
	}
	return nil
}

// MarkdownOrphanTests reports companion files that cannot pair in this project.
func MarkdownOrphanTests(root string) ([]string, error) {
	dir := root
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(101)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 100 {
		return nil, errors.New("test discovery exceeds 100 files")
	}
	var orphan []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".tests.md") {
			agent := filepath.Join(dir, strings.TrimSuffix(entry.Name(), ".tests.md")+".agent.md")
			if _, err := os.Lstat(agent); errors.Is(err, os.ErrNotExist) {
				orphan = append(orphan, filepath.Join(dir, entry.Name()))
			}
		}
	}
	return orphan, nil
}

func DiscoverMarkdownAgents(root string) ([]string, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("agent discovery requires a directory")
	}
	var paths []string
	visited := 0
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("cannot scan %s", safeTerminal(path))
		}
		visited++
		if visited > 10000 {
			return errors.New("scan exceeds 10000 entries; select a smaller directory")
		}
		if e.IsDir() && path != root {
			name := e.Name()
			if (strings.HasPrefix(name, ".") && name != ".github") || name == "node_modules" || name == "vendor" || name == "bin" {
				return filepath.SkipDir
			}
			if filepath.Base(filepath.Dir(path)) == ".github" && name != "agents" {
				return filepath.SkipDir
			}
		}
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".agent.md") {
			paths = append(paths, path)
			if len(paths) > 100 {
				return errors.New("scan exceeds 100 agents")
			}
		}
		return nil
	})
	return paths, err
}
