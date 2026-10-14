package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
	"oras.land/oras-go/v2/registry"
)

// Destinations contains operational selectors only; it is not suite source.
// Resolve once and pass copied concrete values to later operations.
type Destinations struct {
	Version           int                       `yaml:"version"`
	EvalTarget        string                    `yaml:"evalTarget"`
	PublishRepository string                    `yaml:"publishRepository"`
	Targets           map[string]Destination    `yaml:"targets"`
	Registries        map[string]RegistryPolicy `yaml:"registries"`
}
type Destination struct {
	Context          string `yaml:"context" json:"context"`
	Namespace        string `yaml:"namespace" json:"namespace,omitempty"`
	Runtime          string `yaml:"runtime" json:"runtime,omitempty"`
	Inference        string `yaml:"inference" json:"inference,omitempty"`
	Bindings         string `yaml:"bindings" json:"bindings,omitempty"`
	EvaluationTarget string `yaml:"evaluationTarget" json:"evaluationTarget,omitempty"`
}
type RegistryPolicy struct {
	PlainHTTP bool `yaml:"plainHTTP"`
}

var ErrDestinationsInvalid = errors.New("kmx.yaml: invalid operational config")
var ErrDestinationUnknown = errors.New("kmx.yaml: named destination not found")

const maxDestinationsBytes = 64 << 10
const maxDestinationsEntries = 128

var destinationName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// LoadDestinations distinguishes optional absence (nil, nil) from invalid state.
// The descriptor walk and bounded regular-file read never follow links or block
// on a substituted FIFO. Diagnostics do not expose YAML or filesystem content.
func LoadDestinations(root string) (*Destinations, error) {
	abs, err := filepath.Abs(root)
	if err != nil || !destinationText(abs) {
		return nil, ErrDestinationsInvalid
	}
	dir, err := openDestinationsRoot(abs)
	if err != nil {
		return nil, ErrDestinationsInvalid
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), "kmx.yaml", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrDestinationsInvalid
	}
	file := os.NewFile(uintptr(fd), "kmx.yaml")
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxDestinationsBytes {
		return nil, ErrDestinationsInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDestinationsBytes+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || len(raw) > maxDestinationsBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) || secretshapes.Match(string(raw)) != nil {
		return nil, ErrDestinationsInvalid
	}
	var node yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&node) != nil || len(node.Content) != 1 || !validDestinationNode(node.Content[0], 0, "") {
		return nil, ErrDestinationsInvalid
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return nil, ErrDestinationsInvalid
	}
	var doc Destinations
	decoder = yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if decoder.Decode(&doc) != nil || doc.Version != 1 || len(doc.Targets) > maxDestinationsEntries || len(doc.Registries) > maxDestinationsEntries {
		return nil, ErrDestinationsInvalid
	}
	if doc.EvalTarget != "" {
		if _, ok := doc.Targets[doc.EvalTarget]; !ok {
			return nil, ErrDestinationsInvalid
		}
	}
	if doc.PublishRepository != "" {
		ref, err := registry.ParseReference(doc.PublishRepository)
		if err != nil || ref.Reference != "" || !validDestinationHost(ref.Registry) {
			return nil, ErrDestinationsInvalid
		}
	}
	for host := range doc.Registries {
		if !validDestinationHost(host) {
			return nil, ErrDestinationsInvalid
		}
	}
	for name, target := range doc.Targets {
		if !destinationName.MatchString(name) || target.Context == "" || target.Runtime != "" && target.Runtime != "orka" && target.Runtime != "kagent" && target.Runtime != "agentsessions" || target.Inference != "" && target.Bindings != "" || target.Namespace != "" && scaffold.ValidateNamespace(target.Namespace) != nil {
			return nil, ErrDestinationsInvalid
		}
		if target.Inference != "" && (!strings.HasPrefix(target.Inference, "provider:") || scaffold.ValidateObjectName(strings.TrimPrefix(target.Inference, "provider:")) != nil) {
			return nil, ErrDestinationsInvalid
		}
		if target.Runtime == "kagent" && target.Inference != "" || target.Runtime == "orka" && target.Bindings != "" {
			return nil, ErrDestinationsInvalid
		}
		if target.EvaluationTarget != "" {
			if _, ok := doc.Targets[target.EvaluationTarget]; !ok {
				return nil, ErrDestinationsInvalid
			}
		}
		if target.Bindings != "" && !filepath.IsAbs(target.Bindings) {
			target.Bindings = filepath.Join(abs, target.Bindings)
		}
		if target.Bindings != "" && !destinationText(target.Bindings) {
			return nil, ErrDestinationsInvalid
		}
		doc.Targets[name] = target
	}
	return &doc, nil
}

func openDestinationsRoot(abs string) (*os.File, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if part == "" {
			continue
		}
		fd, err = unix.Openat(int(dir.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = os.NewFile(uintptr(fd), part)
	}
	return dir, nil
}

// Validate scalar tags before decoding: yaml's ordinary string decoding would
// otherwise coerce booleans/numbers and silently accept nulls and merge aliases.
func validDestinationNode(n *yaml.Node, depth int, key string) bool {
	if depth > 8 || n.Alias != nil || n.Anchor != "" {
		return false
	}
	switch n.Kind {
	case yaml.MappingNode:
		if n.Tag != "!!map" || len(n.Content) > 2*maxDestinationsEntries {
			return false
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || !destinationText(k.Value) || k.Value == "<<" || seen[k.Value] {
				return false
			}
			seen[k.Value] = true
			if !validDestinationNode(n.Content[i+1], depth+1, k.Value) {
				return false
			}
		}
		return true
	case yaml.ScalarNode:
		if key == "version" {
			return n.Tag == "!!int" && n.Value == "1"
		}
		if key == "plainHTTP" {
			return n.Tag == "!!bool" && (n.Value == "true" || n.Value == "false")
		}
		return n.Tag == "!!str" && destinationText(n.Value)
	default:
		return false
	}
}
func destinationText(s string) bool {
	return s != "" && len(s) <= 1024 && utf8.ValidString(s) && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0 && secretshapes.Match(s) == nil
}
func validDestinationHost(host string) bool {
	if !destinationText(host) || strings.ContainsAny(host, "/@?#\\") || strings.Contains(host, "://") {
		return false
	}
	ref, err := registry.ParseReference(host + "/kmx")
	return err == nil && ref.Registry == host && ref.Reference == ""
}
func (d *Destinations) Target(name string) (Destination, error) {
	if d != nil {
		if target, ok := d.Targets[name]; ok {
			return target, nil
		}
	}
	return Destination{}, ErrDestinationUnknown
}
func (d *Destinations) RegistryHTTP(reference string) bool {
	if d == nil {
		return false
	}
	ref, err := registry.ParseReference(reference)
	return err == nil && d.Registries[ref.Registry].PlainHTTP
}
