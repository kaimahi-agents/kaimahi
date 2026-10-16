// Package workspace resolves closed, tool-free native authoring profiles.
// It performs no inference, image building, or cluster operations.
package workspace

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

//go:embed default-profile.json
var defaultProfile []byte

const maxFile = 1 << 20

// AgentSessionsVersion pins the host/chat config contract, not a floating release.
const AgentSessionsVersion = "ca85f47a2ec17ca6e16029cfa69419cfe29442b3"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// RuntimeProfile is KMX-private selection metadata, not a public AgentSuite SPI.
// The zero value is accepted only as the historical Orka authoring default.
type RuntimeProfile struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Engine  string `json:"engine"`
}

type sourceRuntime struct {
	ID      string  `yaml:"id"`
	Version *string `yaml:"version,omitempty"`
	Engine  *string `yaml:"engine,omitempty"`
}

func (r RuntimeProfile) validate() error {
	if r == (RuntimeProfile{ID: "orka"}) || r == (RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}) || r == (RuntimeProfile{ID: "agentsessions", Version: AgentSessionsVersion, Engine: "chat"}) {
		return nil
	}
	return errors.New("runtime must be orka without version/engine, exact kagent v0.10.3 go, or pinned agentsessions chat")
}

func (r *sourceRuntime) profile() (RuntimeProfile, error) {
	if r == nil {
		return RuntimeProfile{ID: "orka"}, nil
	}
	profile := RuntimeProfile{ID: r.ID}
	if r.ID == "orka" && (r.Version != nil || r.Engine != nil) {
		return profile, errors.New("Orka runtime forbids version and engine fields")
	}
	if r.Version != nil {
		profile.Version = *r.Version
	}
	if r.Engine != nil {
		profile.Engine = *r.Engine
	}
	return profile, profile.validate()
}

type CreateOptions struct {
	Name, Description, Model, InstructionsFile string
	Runtime                                    RuntimeProfile
}
type sourceWorkspace struct {
	SchemaVersion int             `yaml:"schemaVersion"`
	Name          string          `yaml:"name"`
	Member        string          `yaml:"member,omitempty"`
	BuildProfile  string          `yaml:"buildProfile"`
	Members       []string        `yaml:"members,omitempty"`
	Profiles      []sourceProfile `yaml:"profiles,omitempty"`
}

type sourceProfile struct {
	RuntimeProfile string `yaml:"runtimeProfile"`
	BuildProfile   string `yaml:"buildProfile"`
	Platform       string `yaml:"platform"`
}
type sourceMember struct {
	ID           string `yaml:"id"`
	Description  string `yaml:"description,omitempty"`
	Instructions string `yaml:"instructions"`
	Model        struct {
		Protocol string `yaml:"protocol"`
		Model    string `yaml:"model"`
	} `yaml:"model"`
	Runtime     *sourceRuntime `yaml:"runtime,omitempty"`
	Policy      yaml.Node      `yaml:"policy,omitempty"`
	policyBytes []byte
}

func safeText(value string) error {
	if !utf8.ValidString(value) || secretshapes.Match(value) != nil {
		return errors.New("refusing invalid Unicode or credential-shaped content")
	}
	return nil
}
func validateMember(m sourceMember, instructions []byte) error {
	if _, err := m.Runtime.profile(); err != nil {
		return err
	}
	if err := safeText(m.ID + "\n" + m.Description + "\n" + m.Model.Model + "\n" + string(instructions)); err != nil {
		return err
	}
	if err := scaffold.ValidateName(m.ID); err != nil {
		return err
	}
	if m.Model.Protocol != "openai-compatible" || strings.TrimSpace(m.Model.Model) == "" || strings.TrimSpace(m.Model.Model) != m.Model.Model || strings.IndexFunc(m.Model.Model, unicode.IsControl) >= 0 {
		return errors.New("an explicit exact model and openai-compatible protocol are required")
	}
	if strings.TrimSpace(string(instructions)) == "" {
		return errors.New("instructions must not be blank")
	}
	if err := scaffold.ValidateBlockText(string(instructions)); err != nil {
		return fmt.Errorf("instructions: %w", err)
	}
	if err := scaffold.ValidateSingleLineText(m.Description); err != nil {
		return fmt.Errorf("description: %w", err)
	}
	return nil
}

// safeAbsolute refuses symlink components, including existing ancestors of a new path.
func safeAbsolute(value string) (string, error) {
	if err := safeText(value); err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	for p := abs; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlink path is not supported")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return abs, nil
}
func confined(r *os.Root, name string) error {
	if name == "." {
		return nil
	}
	if !filepath.IsLocal(name) || strings.Contains(name, "\\") || path.Clean(name) != name {
		return errors.New("path must be clean and workspace-relative")
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := r.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("workspace symlinks are not supported")
		}
	}
	return nil
}
func readBounded(r *os.Root, name string) ([]byte, error) {
	if err := confined(r, name); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	// Refuse static FIFOs/devices before opening; opening them can block.
	info, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFile {
		return nil, errors.New("source must be a regular file of at most 1 MiB")
	}
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFile {
		return nil, errors.New("source must be a regular file of at most 1 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile || int64(len(b)) != info.Size() {
		return nil, errors.New("source size changed or exceeds limit")
	}
	return b, nil
}

// ValidateCreateOptions checks authored inputs without creating any files.
// Interactive callers use it before offering workspace initialization.
func ValidateCreateOptions(opt CreateOptions) error {
	_, _, err := createInput(opt)
	return err
}

func createInput(opt CreateOptions) (sourceMember, []byte, error) {
	profile := opt.Runtime
	if profile == (RuntimeProfile{}) {
		profile.ID = "orka"
	}
	if err := profile.validate(); err != nil {
		return sourceMember{}, nil, err
	}
	declared := &sourceRuntime{ID: profile.ID}
	if profile.ID != "orka" {
		declared.Version, declared.Engine = &profile.Version, &profile.Engine
	}
	m := sourceMember{ID: opt.Name, Description: opt.Description, Instructions: "instructions.md", Runtime: declared}
	if err := safeText(opt.Name); err != nil {
		return m, nil, err
	}
	if err := scaffold.ValidateName(opt.Name); err != nil {
		return m, nil, err
	}
	if strings.TrimSpace(opt.Model) == "" || strings.TrimSpace(opt.InstructionsFile) == "" {
		return m, nil, errors.New("workspace creation requires --model and --instructions <file>")
	}
	m.Model.Protocol, m.Model.Model = "openai-compatible", opt.Model
	input, err := safeAbsolute(opt.InstructionsFile)
	if err != nil {
		return m, nil, err
	}
	inputRoot, err := os.OpenRoot(filepath.Dir(input))
	if err != nil {
		return m, nil, err
	}
	instructions, err := readBounded(inputRoot, filepath.Base(input))
	inputRoot.Close()
	if err != nil {
		return m, nil, err
	}
	if err := validateMember(m, instructions); err != nil {
		return m, nil, err
	}
	return m, instructions, nil
}

// Create stages complete files and publishes the descriptor last. Exclusive links
// refuse overwrites; rollback removes only directories created by this call.
func Create(ctx context.Context, root string, opt CreateOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m, instructions, err := createInput(opt)
	if err != nil {
		return err
	}
	root, err = safeAbsolute(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, name := range []string{"workspace.yaml", "agent.yaml", "agentsuite.json", "agents", "eval", "build-profiles"} {
		if _, err := r.Lstat(name); !os.IsNotExist(err) {
			return fmt.Errorf("refusing existing workspace/member/bundle path %s", name)
		}
	}
	stage, err := os.MkdirTemp(root, ".kmx-create-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	w := sourceWorkspace{SchemaVersion: 1, Name: opt.Name, Member: "agents/" + opt.Name + "/member.yaml", BuildProfile: "build-profiles/default.json"}
	wb, err := yaml.Marshal(w)
	if err != nil {
		return err
	}
	mb, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	files := map[string][]byte{"workspace.yaml": wb, w.Member: mb, "agents/" + opt.Name + "/instructions.md": instructions, w.BuildProfile: defaultProfile, "eval/example.yaml": []byte("id: example\ninput: What is 2+2?\nexpectContains: [\"4\"]\n")}
	if err := writeFiles(stage, files); err != nil {
		return err
	}
	var owned []string
	published := false
	defer func() {
		if !published {
			for _, name := range owned {
				_ = r.RemoveAll(name)
			}
		}
	}()
	for _, name := range []string{"agents", "build-profiles", "eval"} {
		if err := r.Mkdir(name, 0o700); err != nil {
			return err
		}
		owned = append(owned, name)
	}
	if err := r.Mkdir("agents/"+opt.Name, 0o700); err != nil {
		return err
	}
	for name := range files {
		if name == "workspace.yaml" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.Link(filepath.Base(stage)+"/"+name, name); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Link(filepath.Base(stage)+"/workspace.yaml", "workspace.yaml"); err != nil {
		return err
	}
	published = true
	return nil
}
func writeFiles(root string, files map[string][]byte) error {
	for name, b := range files {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func strictYAML(b []byte, out any) error { return strictSourceYAML(b, out, false) }

func strictSourceYAML(b []byte, out any, profileRows bool) error {
	if err := safeText(string(b)); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("source must contain exactly one YAML document")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("source must be a YAML mapping")
	}
	var visit func(*yaml.Node, int, bool) error
	visit = func(n *yaml.Node, depth int, rows bool) error {
		if depth > 32 || n.Kind == yaml.AliasNode || n.Anchor != "" || n.Tag == "!!null" || n.Tag == "!!binary" {
			return errors.New("unsupported source YAML alias, anchor, null, binary or excessive nesting")
		}
		if err := safeText(n.Value); err != nil {
			return err
		}
		if n.Kind == yaml.SequenceNode {
			for _, item := range n.Content {
				if !(rows && item.Kind == yaml.MappingNode) && (item.Kind != yaml.ScalarNode || item.Tag != "!!str") {
					return errors.New("source lists must contain only strings")
				}
			}
		}
		seen := map[string]bool{}
		for i, c := range n.Content {
			if n.Kind == yaml.MappingNode && i%2 == 0 {
				if c.Kind != yaml.ScalarNode || c.Tag != "!!str" || c.Value == "<<" || seen[c.Value] {
					return errors.New("unsupported or duplicate YAML key")
				}
				seen[c.Value] = true
				value := n.Content[i+1]
				if c.Value == "schemaVersion" {
					if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
						return errors.New("schemaVersion must be an integer scalar")
					}
				} else if value.Kind == yaml.ScalarNode && value.Tag != "!!str" {
					return errors.New("source fields must be strings, without scalar coercion")
				}
			}
			allowRows := profileRows && depth == 0 && n.Kind == yaml.MappingNode && i%2 == 1 && n.Content[i-1].Value == "profiles"
			if err := visit(c, depth+1, allowRows); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(doc.Content[0], 0, false); err != nil {
		return err
	}
	dec = yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(out)
}
