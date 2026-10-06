package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const example = "testdata/research-team"

// copyExample copies the example bundle into a temporary directory that a
// test may freely modify.
func copyExample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := filepath.WalkDir(example, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(example, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func edit(t *testing.T, dir, rel, old, new string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s does not contain %q", rel, old)
	}
	write(t, dir, rel, strings.Replace(string(data), old, new, 1))
}

func TestLoadExample(t *testing.T) {
	b, err := Load(example)
	if err != nil {
		t.Fatal(err)
	}
	if m := b.Manifest.Metadata; m.Name != "research-team" || m.Version != "0.1.0" || m.Description == "" {
		t.Fatalf("manifest = %+v", m)
	}
	if len(b.Providers) != 1 || len(b.Tools) != 2 || len(b.Agents) != 2 {
		t.Fatalf("loaded %d Providers, %d Tools, %d Agents", len(b.Providers), len(b.Tools), len(b.Agents))
	}
	src, _ := os.ReadFile(filepath.Join(example, "agents/researcher.yaml"))
	if got := b.Agent("researcher").Source(); string(got) != string(src) {
		t.Fatal("Agent source is not the exact file bytes")
	}
}

func TestBundleYAMLIsNotPartOfAgentIdentity(t *testing.T) {
	dir := copyExample(t)
	before, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	edit(t, dir, "Bundle.yaml", "0.1.0", "0.2.0")
	after, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"researcher", "summarizer"} {
		a, _ := before.AgentIdentity(name)
		b, _ := after.AgentIdentity(name)
		if a != b {
			t.Errorf("%s identity changed with the bundle version", name)
		}
	}
}

func TestLoadRefuses(t *testing.T) {
	agentDoc := "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: Agent\nmetadata:\n  name: extra\nspec:\n  instructions: x\n  provider: default-chat\n"
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{"missing Bundle.yaml", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, "Bundle.yaml")) }, "Bundle.yaml"},
		{"Bundle.yaml kind", func(t *testing.T, dir string) { edit(t, dir, "Bundle.yaml", "kind: Bundle", "kind: Chart") }, `kind must be "Bundle"`},
		{"Bundle.yaml unknown field", func(t *testing.T, dir string) {
			edit(t, dir, "Bundle.yaml", "metadata:", "values: {}\nmetadata:")
		}, "field values not found"},
		{"non-semver version", func(t *testing.T, dir string) { edit(t, dir, "Bundle.yaml", "0.1.0", "v1") }, "semantic version"},
		{"invalid bundle name", func(t *testing.T, dir string) {
			edit(t, dir, "Bundle.yaml", "name: research-team", "name: Research Team")
		}, "metadata.name"},
		{"kind in wrong directory", func(t *testing.T, dir string) { write(t, dir, "tools/extra.yaml", agentDoc) }, `tools/extra.yaml: kind must be "Tool"`},
		{"file name mismatch", func(t *testing.T, dir string) { write(t, dir, "agents/other.yaml", agentDoc) }, `must match the file name "other"`},
		{"non-YAML file", func(t *testing.T, dir string) { write(t, dir, "tools/README.md", "notes") }, "only regular .yaml"},
		{"nested directory", func(t *testing.T, dir string) { write(t, dir, "tools/extra/x.yaml", agentDoc) }, "only regular .yaml"},
		{"symlink", func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(dir, "agents/summarizer.yaml"), filepath.Join(dir, "agents/linked.yaml")); err != nil {
				t.Skip(err)
			}
		}, "only regular .yaml"},
		{"no Agents", func(t *testing.T, dir string) { os.RemoveAll(filepath.Join(dir, "agents")) }, "no Agents"},
		{"invalid resource", func(t *testing.T, dir string) {
			edit(t, dir, "tools/fetch-page.yaml", "method: POST", "method: TRACE")
		}, "tools/fetch-page.yaml: spec.http.method"},
		{"broken reference", func(t *testing.T, dir string) {
			edit(t, dir, "agents/summarizer.yaml", "provider: default-chat", "provider: other-chat")
		}, `Agent "summarizer" uses unknown Provider "other-chat"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyExample(t)
			tc.setup(t, dir)
			_, err := Load(dir)
			if err == nil {
				t.Fatal("Load succeeded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadReportsEveryInvalidFile(t *testing.T) {
	dir := copyExample(t)
	edit(t, dir, "tools/fetch-page.yaml", "method: POST", "method: TRACE")
	edit(t, dir, "providers/default-chat.yaml", "type: openai", "type: bedrock")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load succeeded")
	}
	for _, want := range []string{"providers/default-chat.yaml", "tools/fetch-page.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
