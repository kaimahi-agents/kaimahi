package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

func encodeOrkaCreationBindings(opt CreateOptions) ([]byte, error) {
	return agentruntime.EncodeOrkaBindings(orkaBindingsFromCreate(opt))
}

func bundlePathForCreate(opt CreateOptions) string {
	if opt.BundlePath != "" {
		return opt.BundlePath
	}
	if opt.Out == "-" {
		return ""
	}
	return filepath.Join("agents", opt.Name)
}

// refuseOrkaBundleArtifactOverlap keeps the rendered manifest out of the
// two-file portable bundle; otherwise a later retry would refuse that extra
// entry or mistake a manifest for the revision.
func refuseOrkaBundleArtifactOverlap(opt CreateOptions, bundlePath string) error {
	if bundlePath == "" || opt.Out == "-" {
		return nil
	}
	bundle, err := resolveOrkaPath(bundlePath)
	if err != nil {
		return err
	}
	artifact, err := resolveOrkaPath(orkaArtifactPath(opt))
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{bundle, artifact}, {artifact, bundle}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return err
		}
		if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("rendered artifact cannot be inside the bundle or contain it")
		}
	}
	return nil
}

// resolveOrkaPath follows existing ancestors while retaining the as-yet
// unwritten suffix, so aliases cannot hide an artifact inside a bundle.
func resolveOrkaPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for parent := absolute; ; parent = filepath.Dir(parent) {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			suffix, err := filepath.Rel(parent, absolute)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// A dangling symlink is not a missing directory we may create.
		if _, statErr := os.Lstat(parent); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("cannot resolve bundle or artifact path: %w", err)
		}
		if filepath.Dir(parent) == parent {
			return "", err
		}
	}
}

// preflightOrkaBundle checks whether a directory can be reused without
// creating it (or its parents). Persistence checks again to catch changes
// between preflight and write.
func preflightOrkaBundle(path string, agent, bindings []byte) error {
	if err := scaffold.RefuseKeyShapes(path); err != nil {
		return fmt.Errorf("refusing credential-shaped bundle path")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("bundle path %s is not a directory", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect bundle directory: %w", err)
	}
	files := map[string][]byte{"agent.yaml": agent, "bindings.yaml": bindings}
	for _, entry := range entries {
		// receipts/ is written by lift and evaluate; eval/ holds the cases an
		// operator authors. Neither is part of the revision, so their
		// contents never make a rerun differ, but each must be a directory.
		if entry.Name() == "receipts" || entry.Name() == agentruntime.EvaluationCaseDir {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect %s directory: %w", entry.Name(), err)
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("bundle %s must be a directory, not a link", entry.Name())
			}
			continue
		}
		if entry.Name() == "lift-policy.yaml" {
			if _, err := loadBundleLiftPolicy(path); err != nil {
				return err
			}
			continue
		}
		content, expected := files[entry.Name()]
		if !expected {
			if err := scaffold.RefuseKeyShapes(entry.Name()); err != nil {
				return fmt.Errorf("bundle directory contains a credential-shaped unexpected entry")
			}
			return fmt.Errorf("bundle directory contains unexpected entry %s", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect bundle %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle %s must be a regular file", entry.Name())
		}
		existing, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return fmt.Errorf("read bundle %s: %w", entry.Name(), err)
		}
		if !bytes.Equal(existing, content) {
			return fmt.Errorf("bundle %s differs; refusing to overwrite it", entry.Name())
		}
	}
	for _, name := range []string{"agent.yaml", "bindings.yaml"} {
		found := false
		for _, entry := range entries {
			if entry.Name() == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("bundle is incomplete: missing %s; review or remove it before retrying", name)
		}
	}
	return nil
}

// writeOrkaBundle never modifies an existing entry. Complete, identical pairs
// are reusable after deployment fails; partial directories need manual review
// rather than guessing whether an interrupted write is the intended revision.
func writeOrkaBundle(path string, agent, bindings []byte) error {
	if err := preflightOrkaBundle(path, agent, bindings); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create bundle parent: %w", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("create bundle directory: %w", err)
		}
		return preflightOrkaBundle(path, agent, bindings)
	}
	files := map[string][]byte{"agent.yaml": agent, "bindings.yaml": bindings}
	for _, name := range []string{"agent.yaml", "bindings.yaml"} {
		if err := scaffold.WriteNew(filepath.Join(path, name), string(files[name])); err != nil {
			return fmt.Errorf("write bundle %s (partial bundle may remain): %w", name, err)
		}
	}
	// Only a bundle this call created gets the example case; a reused bundle
	// keeps whatever cases its operator has.
	if err := os.Mkdir(filepath.Join(path, agentruntime.EvaluationCaseDir), 0o755); err != nil {
		return fmt.Errorf("create bundle %s directory: %w", agentruntime.EvaluationCaseDir, err)
	}
	example := filepath.Join(path, agentruntime.EvaluationCaseDir, "example.yaml")
	if err := scaffold.WriteNew(example, exampleEvaluationCase); err != nil {
		return fmt.Errorf("write example evaluation case: %w", err)
	}
	return nil
}

// exampleEvaluationCase is the one case a new bundle starts with. It is
// deliberately trivial to satisfy, so a first `kmx agent evaluate` shows the
// loop working before anyone writes a real case.
const exampleEvaluationCase = `# One evaluation case for kmx agent evaluate. Each eval/*.yaml file is one
# case: kmx creates one Orka Task with this input against the deployed
# revision, and the case passes when the Task succeeds and its answer
# contains every expectContains string (exact, case-sensitive). Unknown
# fields are refused. Cases test a revision; they are not part of its digest.
id: example
input: Reply with exactly the word READY and nothing else.
expectContains:
  - READY
`
