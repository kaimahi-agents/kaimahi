package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func TestWriteOrkaBundleAllowsIdenticalRetryAndRefusesEditsByFilename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents", "demo")
	if err := writeOrkaBundle(path, []byte("revision\n"), []byte("bindings\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeOrkaBundle(path, []byte("revision\n"), []byte("bindings\n")); err != nil {
		t.Fatalf("identical retry refused: %v", err)
	}
	for _, tc := range []struct {
		name               string
		revision, bindings []byte
	}{
		{"agent.yaml", []byte("edited\n"), []byte("bindings\n")},
		{"bindings.yaml", []byte("revision\n"), []byte("edited\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := writeOrkaBundle(path, tc.revision, tc.bindings)
			if err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("did not refuse changed %s: %v", tc.name, err)
			}
		})
	}
}

func TestWriteOrkaBundleRejectsIncompleteAndUnexpectedEntries(t *testing.T) {
	for _, tc := range []struct{ name, file, want string }{{"partial", "agent.yaml", "bindings.yaml"}, {"unexpected", "notes.txt", "notes.txt"}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "demo")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, tc.file), []byte("revision"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeOrkaBundle(path, []byte("revision"), []byte("bindings")); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("did not identify incomplete/extra entry: %v", err)
			}
		})
	}
}

func TestWriteOrkaBundleDoesNotEchoCredentialShapedUnexpectedFilenames(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "demo")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, shape.Example), []byte("unexpected"), 0600); err != nil {
				t.Fatal(err)
			}
			err := writeOrkaBundle(path, []byte("revision"), []byte("bindings"))
			if err == nil || strings.Contains(err.Error(), shape.Example) {
				t.Fatalf("unexpected entry was accepted or echoed: %v", err)
			}
		})
	}
}

func TestOfflineCreateRejectsIncompatibleBundleBeforeArtifactEmission(t *testing.T) {
	for _, output := range []string{"stdout", "file"} {
		t.Run(output, func(t *testing.T) {
			a, opt, out, _, _ := orkaCreateFixture(t, "")
			opt.NoApply = true
			if output == "stdout" {
				opt.Out = "-"
			}
			if err := writeOrkaBundle(opt.BundlePath, []byte("different revision"), mustBindingsSource(t, opt)); err != nil {
				t.Fatal(err)
			}
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), "agent.yaml differs") {
				t.Fatalf("incompatible bundle was not refused: %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("stdout artifact emitted before bundle refusal")
			}
			if output == "file" {
				if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
					t.Fatalf("file artifact emitted before bundle refusal: %v", err)
				}
			}
		})
	}
}

func TestOnlineCreateRejectsBundleAndArtifactBeforePersistence(t *testing.T) {
	for _, tc := range []struct{ name, expected string }{
		{"incompatible bundle", "agent.yaml differs"},
		{"existing artifact", "already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, "")
			if tc.name == "incompatible bundle" {
				if err := writeOrkaBundle(opt.BundlePath, []byte("different revision"), mustBindingsSource(t, opt)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(opt.Out, []byte("operator artifact"), 0600); err != nil {
				t.Fatal(err)
			}
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("preflight did not refuse %s: %v", tc.name, err)
			}
			if tc.name == "existing artifact" {
				if _, err := os.Stat(opt.BundlePath); !os.IsNotExist(err) {
					t.Fatalf("wrote bundle despite artifact collision: %v", err)
				}
				if content, err := os.ReadFile(opt.Out); err != nil || string(content) != "operator artifact" {
					t.Fatalf("changed existing artifact: %q, %v", content, err)
				}
			}
			if calls := orkaCalls(t, dir); len(calls) != 0 {
				t.Fatalf("reached kubectl before local refusal: %+v", calls)
			}
		})
	}
}

func TestOnlineCreateDoesNotPersistIfKubectlPreflightFails(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "")
	fakeTool(t, dir, "kubectl", "exit 1")
	if err := a.CreateAgent(opt); err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("unusable kubectl was not refused: %v", err)
	}
	if _, err := os.Stat(opt.BundlePath); !os.IsNotExist(err) {
		t.Fatalf("persisted bundle before kubectl preflight: %v", err)
	}
}

func TestOfflineCreatePersistsBundleAndKeepsArtifactExclusive(t *testing.T) {
	a, opt, _, _, _ := orkaCreateFixture(t, "")
	opt.NoApply = true
	opt.BundlePath = filepath.Join(t.TempDir(), "agents", opt.Name)
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opt.BundlePath, "agent.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opt.BundlePath, "bindings.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateAgent(opt); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("artifact lost exclusive-write semantics: %v", err)
	}
}

func TestCreateDefaultBundlePathIsAgentsName(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	app := lifecycleTestApp(t)
	opt := goldenNoTaskCreate("")
	opt.Name = "default-path"
	opt.Out = ""
	if err := app.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("agents", "default-path", "agent.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("agents", "default-path.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestStdoutCreatesNoImplicitBundleButExplicitPathPersists(t *testing.T) {
	a, opt, out, _, _ := orkaCreateFixture(t, "")
	t.Chdir(t.TempDir())
	opt.Out = "-"
	opt.BundlePath = ""
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "kind: Provider") {
		t.Fatal("stdout lost rendered artifact")
	}
	if _, err := os.Stat(filepath.Join("agents", opt.Name)); !os.IsNotExist(err) {
		t.Fatalf("stdout unexpectedly wrote the default bundle: %v", err)
	}
	path := filepath.Join(t.TempDir(), "agents", "demo")
	opt.BundlePath = path
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "agent.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineCreatePersistsSourceAndCreationBindings(t *testing.T) {
	a, opt, _, _, _ := orkaCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(opt.BundlePath, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, mustPortableSource(t, opt)) {
		t.Fatal("stored revision differs from render input")
	}
	stored, err := os.ReadFile(filepath.Join(opt.BundlePath, "bindings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := agentruntime.ParseOrkaBindings(stored)
	if err != nil {
		t.Fatal(err)
	}
	if bindings.Namespace != opt.Namespace || bindings.Provider.SecretRef.Name != opt.Secret {
		t.Fatal("stored creation target differs from create flags")
	}
}

func TestOnlineFailureKeepsBundleForIdenticalRetry(t *testing.T) {
	a, opt, _, _, _ := orkaCreateFixture(t, "missing-secret")
	opt.BundlePath = filepath.Join(t.TempDir(), "agents", "sample")
	if err := a.CreateAgent(opt); err == nil {
		t.Fatal("expected deployment failure")
	}
	if _, err := os.Stat(filepath.Join(opt.BundlePath, "agent.yaml")); err != nil {
		t.Fatalf("failed deployment lost revision: %v", err)
	}
	if err := writeOrkaBundle(opt.BundlePath, mustPortableSource(t, opt), mustBindingsSource(t, opt)); err != nil {
		t.Fatalf("failed-deploy bundle not reusable: %v", err)
	}
	// Re-enter the actual create boundary. The target still lacks its
	// Secret, so it should fail at deployment again, not at bundle collision.
	if err := a.CreateAgent(opt); err == nil || strings.Contains(err.Error(), "bundle agent.yaml") {
		t.Fatalf("identical retry did not reach deployment: %v", err)
	}
}

func TestDryRunPersistsBundleWhenArtifactWritten(t *testing.T) {
	a, opt, _, _, _ := orkaCreateFixture(t, "")
	opt.DryRun = true
	opt.BundlePath = filepath.Join(t.TempDir(), "agents", "sample")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opt.BundlePath, "agent.yaml")); err != nil {
		t.Fatalf("dry-run lost revision: %v", err)
	}
}

func mustPortableSource(t *testing.T, opt CreateOptions) []byte {
	t.Helper()
	result, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func mustBindingsSource(t *testing.T, opt CreateOptions) []byte {
	t.Helper()
	result, err := encodeOrkaCreationBindings(opt)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCreateBundleFromDiskMatchesInMemoryRender(t *testing.T) {
	a, opt, _, _, _ := orkaCreateFixture(t, "")
	opt.NoApply = true
	opt.BundlePath = filepath.Join(t.TempDir(), "agents", opt.Name)
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	inMemory, err := RenderOrkaBundleFile(opt.BundlePath, orkaBindingsFromCreate(opt))
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(opt.BundlePath, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if inMemory.PortableDigest() != agentruntime.PortableBundleDigest(source) {
		t.Fatal("disk bytes altered portable digest")
	}
	artifact, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range inMemory.Documents() {
		if !bytes.Contains(artifact, doc) {
			t.Fatal("disk render differs from artifact")
		}
	}
}
