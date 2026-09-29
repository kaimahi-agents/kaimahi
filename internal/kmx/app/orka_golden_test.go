package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// goldenNoTaskCreate is the one create this repository pins byte-for-byte. It
// deliberately exercises every optional field a no-Task bundle can carry —
// description, baseURL, explicit Secret key, tools, skills and both rate
// limits — so a change to any of them moves the golden instead of slipping
// through as an unpinned field. A Task is excluded on purpose: its name
// carries 16 random bytes, so a bundle with one has no fixed bytes to pin.
func goldenNoTaskCreate(out string) CreateOptions {
	return CreateOptions{
		Name: "sample", Namespace: "orka-system", Description: "Golden sample agent",
		ProviderType: "openai", Model: "gpt-4o-mini", BaseURL: "https://models.example.invalid/v1",
		Secret: "model-key", SecretKey: "api-key",
		InstructionText: "Answer briefly and say plainly when you do not know something.",
		Tools:           "search,read", Skills: "summarize",
		AgentRequestsPerMinute: "30", AgentTokensPerMinute: "60000",
		ProviderRequestsPerMinute: "120", ProviderTokensPerMinute: "240000",
		SchemaTarget: "v0.1.3", Out: out, NoApply: true,
	}
}

func TestOrkaV020NoTaskArtifactMatchesGolden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	opt := goldenNoTaskCreate(path)
	opt.SchemaTarget = "" // the default is the current v0.2.0 release
	opt.AgentRequestsPerMinute, opt.AgentTokensPerMinute = "", ""
	opt.ProviderRequestsPerMinute, opt.ProviderTokensPerMinute = "", ""
	opt.BundlePath = filepath.Join(dir, "agents", "sample")
	var out, errOut bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{Stdout: &out, Stderr: &errOut}, Out: &out, Err: &errOut}
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "orka-v020-no-task.golden.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("v0.2.0 artifact changed:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestOrkaNoTaskArtifactMatchesGolden pins the exact bytes `kmx agent create
// --no-apply` writes today, before any lifecycle routing exists to change
// them. The committed golden is the characterization: routing creation
// through the Orka LifecycleAdapter must render the same documents, in the
// same order, with the same provenance header — so a diff here is the seam
// changing output, which this port is not allowed to do.
func TestOrkaNoTaskArtifactMatchesGolden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	var out, errOut bytes.Buffer
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: &out, Stderr: &errOut}, Out: &out, Err: &errOut,
	}
	// Create writes a portable bundle beside the artifact. Without an explicit
	// path it defaults to agents/<name> under the working directory, which
	// in a test is this package's source directory.
	opt := goldenNoTaskCreate(path)
	opt.BundlePath = filepath.Join(dir, "agents", "sample")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opt.BundlePath, "agent.yaml")); err != nil {
		t.Fatalf("the bundle was not written where the test put it: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "orka-no-task.golden.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generated Orka bundle no longer matches the committed golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
