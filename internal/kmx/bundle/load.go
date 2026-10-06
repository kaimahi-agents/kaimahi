// Package bundle loads a composable kmx bundle from a directory: a
// Bundle.yaml manifest that identifies the bundle, and krm Provider, Tool and
// Agent resources in conventional providers/, tools/ and agents/
// directories. A bundle is validated, rendered and deployed as a whole.
package bundle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/krm"
)

const (
	ManifestFile = "Bundle.yaml"
	KindBundle   = "Bundle"
)

// SemVer 2.0.0.
var semverRE = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// Manifest is Bundle.yaml: the bundle's identity, not its contents.
type Manifest struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   ManifestMetadata `yaml:"metadata"`
}

type ManifestMetadata struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description,omitempty"`
}

// Bundle is a loaded bundle: its manifest and its validated resource graph.
type Bundle struct {
	Manifest Manifest
	*krm.Graph
}

// Load reads dir/Bundle.yaml and every resource under providers/, tools/ and
// agents/, then validates the whole graph. Each resource file holds one
// document of that directory's kind and is named <metadata.name>.yaml. Files
// outside those directories are not part of the bundle and are ignored.
func Load(dir string) (*Bundle, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("bundle directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("bundle %s is not a directory", dir)
	}
	b := &Bundle{}
	data, err := readRegular(filepath.Join(dir, ManifestFile))
	if err == nil {
		err = krm.DecodeStrict(data, KindBundle, &b.Manifest)
	}
	if err == nil {
		err = b.Manifest.validate()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}

	var (
		providers []*krm.Provider
		tools     []*krm.Tool
		agents    []*krm.Agent
		errs      []error
	)
	load := func(dir, rel string, decode func([]byte) (string, error)) {
		data, err := readRegular(filepath.Join(dir, rel))
		var name string
		if err == nil {
			name, err = decode(data)
		}
		if want := strings.TrimSuffix(filepath.Base(rel), ".yaml"); err == nil && name != want {
			err = fmt.Errorf("metadata.name %q must match the file name %q", name, want)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.ToSlash(rel), err))
		}
	}
	for _, rd := range []struct {
		dir    string
		decode func([]byte) (string, error)
	}{
		{"providers", func(data []byte) (string, error) {
			p, err := krm.DecodeProvider(data)
			if err != nil {
				return "", err
			}
			providers = append(providers, p)
			return p.Metadata.Name, nil
		}},
		{"tools", func(data []byte) (string, error) {
			t, err := krm.DecodeTool(data)
			if err != nil {
				return "", err
			}
			tools = append(tools, t)
			return t.Metadata.Name, nil
		}},
		{"agents", func(data []byte) (string, error) {
			a, err := krm.DecodeAgent(data)
			if err != nil {
				return "", err
			}
			agents = append(agents, a)
			return a.Metadata.Name, nil
		}},
	} {
		files, err := resourceFiles(dir, rd.dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, rel := range files {
			load(dir, rel, rd.decode)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, errors.New("bundle defines no Agents")
	}
	if b.Graph, err = krm.NewGraph(providers, tools, agents); err != nil {
		return nil, err
	}
	return b, nil
}

func (m *Manifest) validate() error {
	var errs []error
	if err := krm.ValidateName(m.Metadata.Name); err != nil {
		errs = append(errs, fmt.Errorf("metadata.name: %w", err))
	}
	if !semverRE.MatchString(m.Metadata.Version) {
		errs = append(errs, fmt.Errorf("metadata.version must be a semantic version such as 0.1.0 (found %q)", m.Metadata.Version))
	}
	if krm.HasControl(m.Metadata.Description) {
		errs = append(errs, errors.New("metadata.description must not contain control characters"))
	}
	return errors.Join(errs...)
}

// resourceFiles lists one conventional directory in lexical order. A missing
// directory is empty; anything other than a regular .yaml file is refused
// rather than silently skipped.
func resourceFiles(root, dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s/: %w", dir, err)
	}
	var files []string
	for _, entry := range entries {
		rel := filepath.Join(dir, entry.Name())
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".yaml" {
			return nil, fmt.Errorf("%s: only regular .yaml resource files belong in %s/", filepath.ToSlash(rel), dir)
		}
		files = append(files, rel)
	}
	return files, nil
}

// readRegular refuses symlinks and other non-regular files so a bundle
// cannot pull content from outside its own directory.
func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("must be a regular file, not a symlink or directory")
	}
	return os.ReadFile(path)
}
