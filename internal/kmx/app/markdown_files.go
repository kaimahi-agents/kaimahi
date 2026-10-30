package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

const markdownFileBudget = 32

func markdownStem(path string) string {
	return strings.TrimSuffix(strings.TrimSuffix(path, ".md"), ".agent")
}

func MarkdownToolsPath(path string) string { return markdownStem(path) + ".tools.yaml" }

// ResolveMarkdownSource selects one root agent, never recursively guesses from
// tests or tool files. A plain agent.md is supported alongside *.agent.md.
func ResolveMarkdownSource(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return canonicalMDSource(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	entries, err := f.ReadDir(257)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if len(entries) > 256 {
		return "", errors.New("agent directory exceeds 256 entries")
	}
	var candidates []string
	var plain []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && (entry.Name() == "agent.md" || strings.HasSuffix(entry.Name(), ".agent.md")) {
			candidates = append(candidates, filepath.Join(path, entry.Name()))
		}
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md") && !strings.HasSuffix(entry.Name(), ".tests.md") && !strings.HasSuffix(entry.Name(), ".agent.test.md") {
			plain = append(plain, filepath.Join(path, entry.Name()))
		}
	}
	if len(candidates) == 0 && len(plain) == 1 {
		candidates = plain
	}
	if len(candidates) != 1 {
		return "", errors.New("select one explicit agent file; directory must contain exactly one root *.agent.md or agent.md")
	}
	return canonicalMDSource(candidates[0])
}

type markdownToolsFile struct {
	SchemaVersion int                    `yaml:"schemaVersion"`
	Files         []string               `yaml:"files,omitempty"`
	Tools         map[string]CLIToolSpec `yaml:"tools"`
}

func decodeMarkdownTools(raw []byte) (markdownToolsFile, error) {
	var result markdownToolsFile
	var node yaml.Node
	if yaml.Unmarshal(raw, &node) != nil || mdNodeCheck(&node) != nil {
		return result, errors.New("invalid tools YAML: duplicate fields, aliases and credentials refuse")
	}
	d := yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	var extra any
	if d.Decode(&result) != nil || !errors.Is(d.Decode(&extra), io.EOF) || result.SchemaVersion != 1 || result.Tools == nil {
		return result, errors.New("tools require schemaVersion: 1, tools: {} and optional files")
	}
	return result, nil
}

// References are root-relative, explicit and one-level. No links or external
// paths are followed. All referenced bytes are retained for drift and import.
func markdownReferencedFile(root, ref string) (string, error) {
	if ref == "" || filepath.IsAbs(ref) || strings.Contains(ref, "\\") || filepath.ToSlash(filepath.Clean(ref)) != ref {
		return "", errors.New("companion references must be normalized relative paths")
	}
	for _, part := range strings.Split(ref, "/") {
		if part == ".." || part == "." {
			return "", errors.New("companion reference escapes agent directory")
		}
	}
	path := filepath.Join(root, filepath.FromSlash(ref))
	for p := path; p != root; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("companion references must not traverse symlinks")
		}
	}
	return path, nil
}

func readMarkdownReferences(root string, refs []string, retained map[string][]byte) error {
	if len(refs) > markdownFileBudget {
		return errors.New("too many companion files")
	}
	total := 0
	for _, b := range retained {
		total += len(b)
	}
	for _, ref := range refs {
		path, err := markdownReferencedFile(root, ref)
		if err != nil {
			return err
		}
		if _, ok := retained[path]; ok {
			return errors.New("duplicate companion file reference")
		}
		b, err := mdRead(path, 128<<10)
		if err != nil {
			return err
		}
		total += len(b)
		if total > 1<<20 {
			return errors.New("companion files exceed 1 MiB")
		}
		retained[path] = b
	}
	return nil
}

func markdownSourcesDigest(root string, files map[string][]byte) string {
	if len(files) == 0 {
		return ""
	}
	values := map[string]string{}
	for path, b := range files {
		rel, _ := filepath.Rel(root, path)
		values[filepath.ToSlash(rel)] = mdHash(b)
	}
	return mdJSONHash(values)
}

func loadMarkdownTools(a *MarkdownAgent) error {
	a.ToolsPath = MarkdownToolsPath(a.Path)
	a.ToolSources = map[string][]byte{}
	b, err := mdRead(a.ToolsPath, 128<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(a.CLITools) > 0 {
		return errors.New("use tools.yaml only; inline and companion bindings cannot be combined")
	}
	root := filepath.Dir(a.Path)
	doc, err := decodeMarkdownTools(b)
	if err != nil {
		return err
	}
	a.ToolSources[a.ToolsPath] = b
	if err := readMarkdownReferences(root, doc.Files, a.ToolSources); err != nil {
		return err
	}
	definitions := map[string]CLIToolSpec{}
	paths := []string{a.ToolsPath}
	for _, ref := range doc.Files {
		paths = append(paths, filepath.Join(root, ref))
	}
	for i, path := range paths {
		part, err := decodeMarkdownTools(a.ToolSources[path])
		if err != nil {
			return err
		}
		if i > 0 && len(part.Files) > 0 {
			return errors.New("nested tool includes are unsupported")
		}
		for name, spec := range part.Tools {
			if _, ok := definitions[name]; ok {
				return fmt.Errorf("duplicate tool %s", name)
			}
			if spec.Name != "" && spec.Name != name {
				return errors.New("tool name differs from mapping key")
			}
			spec.Name = name
			if err := validateCLISpec(spec); err != nil {
				return fmt.Errorf("tool %s: %w", name, err)
			}
			definitions[name] = spec
		}
	}
	if len(definitions) > 8 {
		return errors.New("at most eight command bindings are supported")
	}
	for _, name := range a.Tools {
		if spec, ok := definitions[name]; ok {
			a.CLITools = append(a.CLITools, spec)
		}
	}
	a.ToolsRevision = markdownSourcesDigest(root, a.ToolSources)
	return nil
}

func loadMarkdownTestFiles(a *MarkdownAgent, raw []byte) error {
	root := filepath.Dir(a.Path)
	a.TestSources = map[string][]byte{a.TestsPath: raw}
	a.CasePaths = map[string]string{}
	fields, lines, end, err := mdFront(a.TestsPath, raw, 128<<10)
	if err != nil {
		return err
	}
	var refs []string
	if n := fields["files"]; n != nil {
		if n.Decode(&refs) != nil {
			return errors.New("test files must be a list of relative paths")
		}
		delete(fields, "files")
	}
	if len(fields) != 1 || fields["schemaVersion"] == nil || fields["schemaVersion"].Value != "1" {
		return errors.New("test index requires schemaVersion: 1 and optional files")
	}
	if err := readMarkdownReferences(root, refs, a.TestSources); err != nil {
		return err
	}
	paths := []string{a.TestsPath}
	for _, ref := range refs {
		paths = append(paths, filepath.Join(root, ref))
	}
	seen := map[string]bool{}
	for _, c := range a.Examples {
		seen[c.ID] = true
	}
	for i, path := range paths {
		b := a.TestSources[path]
		if i == 0 && len(refs) > 0 {
			b = []byte("---\nschemaVersion: 1\n---\n" + strings.Join(lines[end+1:], "\n"))
		}
		part := *a
		part.TestsPath = path
		part.Examples = nil
		part.Mocks = map[string][]MarkdownMock{}
		part.Lines = map[string]int{}
		if err := parseMarkdownTests(&part, b); err != nil {
			return err
		}
		for _, c := range part.Examples {
			if seen[c.ID] {
				return fmt.Errorf("duplicate test ID %s", c.ID)
			}
			seen[c.ID] = true
			a.Examples = append(a.Examples, c)
			a.Mocks[c.ID] = part.Mocks[c.ID]
			a.Lines[c.ID] = part.Lines[c.ID]
			a.CasePaths[c.ID] = path
		}
	}
	if len(a.Examples) > 32 {
		return errors.New("at most 32 test cases are supported")
	}
	a.TestSource = bytes.Clone(raw)
	a.TestsRevision = markdownSourcesDigest(root, a.TestSources)
	return nil
}

func checkMarkdownSources(rootFile string, files map[string][]byte, limit int) error {
	if len(files) == 0 {
		if _, err := os.Lstat(rootFile); !errors.Is(err, os.ErrNotExist) {
			return errors.New("companion appeared; reload before running")
		}
		return nil
	}
	for path, want := range files {
		b, err := mdRead(path, limit)
		if err != nil || !bytes.Equal(b, want) {
			return errors.New("companion changed; reload before running")
		}
	}
	return nil
}

func sortedMarkdownPaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func writeMarkdownCompanion(path string, data, expected []byte) error {
	before, err := mdRead(path, 128<<10)
	if expected == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("companion already exists")
		}
	} else if err != nil || !bytes.Equal(before, expected) {
		return errors.New("companion changed before save")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".kmx-companion-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if expected == nil {
		return os.Link(f.Name(), path)
	}
	before, err = mdRead(path, 128<<10)
	if err != nil || !bytes.Equal(before, expected) {
		return errors.New("companion changed before save")
	}
	return os.Rename(f.Name(), path)
}
