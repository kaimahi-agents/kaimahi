package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

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
	return nil
}
