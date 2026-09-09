package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

type sliceScanner struct {
	values []string
	index  int
}

func TestOperationCommandsRoundTripShellArguments(t *testing.T) {
	a := &App{Cfg: &config.Config{KubeContext: "kind-team's $(false); test"}}
	args := []string{"agent", "chat", "my-agent", "a 'quote' and $(false); $HOME", ""}
	command := a.operationCommand(args...)
	// Replace only the executable with a shell function, so the printed
	// command is parsed exactly as a pasted next action would be.
	out, err := exec.Command("sh", "-c", "kmx() { printf '%s\\000' \"$@\"; }; "+command).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(append([]string{"--context", a.Cfg.KubeContext}, args...), "\x00") + "\x00"
	if string(out) != want {
		t.Fatalf("command %s produced %q, want %q", command, out, want)
	}
}

func TestLocalOperationCommandsRetainClusterAndEngine(t *testing.T) {
	a := &App{Cfg: &config.Config{KubeContext: "kind-custom", KindCluster: "custom", ContainerEngine: "podman"}}
	for _, verb := range []string{"up", "plane", "down"} {
		want := "KIND_CLUSTER=custom CONTAINER_ENGINE=podman kmx --context kind-custom " + verb
		if got := a.operationCommand(verb); got != want {
			t.Fatalf("%s action lost its target: %s", verb, got)
		}
	}
}

func TestCreateNoApplyQuotesContextAndManifestPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent's $(false).yaml")
	var out, errOut bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-team's test"}, Out: &out, Err: &errOut}
	if err := a.CreateAgent(CreateOptions{Name: "demo", ModelConfig: "hello-world-model", Out: path, NoApply: true}); err != nil {
		t.Fatal(err)
	}
	want := "kubectl --context " + shellArg(a.Cfg.KubeContext) + " apply -f " + shellArg(path)
	if !strings.Contains(errOut.String(), want) {
		t.Fatalf("unsafe apply hint: %s", errOut.String())
	}
}

func (s *sliceScanner) Scan() bool {
	if s.index >= len(s.values) {
		return false
	}
	s.index++
	return true
}
func (s *sliceScanner) Text() string { return s.values[s.index-1] }
func (s *sliceScanner) Err() error   { return nil }

func TestCreateWizardCollectsSafeDefaultsAndAppliesByDefault(t *testing.T) {
	scanner := &sliceScanner{values: []string{"Reports unhealthy workloads", "", ""}}
	var out bytes.Buffer
	opt, err := collectCreateOptions(scanner, &out, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if opt.Description != "Reports unhealthy workloads" || opt.Name != "reports-unhealthy-workloads" || opt.Namespace != "kagent" {
		t.Fatalf("unexpected wizard options: %+v", opt)
	}
	if opt.Out != filepath.Join("agents", opt.Name+".yaml") || opt.NoApply {
		t.Fatalf("wizard should apply by default: %+v", opt)
	}
	if !strings.Contains(opt.InstructionText, "Reports unhealthy workloads") {
		t.Fatalf("description did not configure instructions: %q", opt.InstructionText)
	}
	if !strings.HasPrefix(out.String(), "Describe this agent: ") {
		t.Fatalf("first prompt is not the requested description prompt: %q", out.String())
	}
}

func TestCreateWizardExplicitNoApplySkipsConfirmation(t *testing.T) {
	scanner := &sliceScanner{values: []string{"Cluster reporter", "cluster-reporter"}}
	opt, err := collectCreateOptions(scanner, &bytes.Buffer{}, CreateOptions{NoApply: true})
	if err != nil {
		t.Fatal(err)
	}
	if !opt.NoApply {
		t.Fatal("explicit --no-apply was lost")
	}
}

func TestCreateWizardDeclineCancels(t *testing.T) {
	scanner := &sliceScanner{values: []string{"Cluster reporter", "cluster-reporter", "no"}}
	_, err := collectCreateOptions(scanner, &bytes.Buffer{}, CreateOptions{})
	if !errors.Is(err, errCreateCancelled) {
		t.Fatalf("decline did not cancel creation: %v", err)
	}
}

func TestCreateWizardRepromptsInvalidConfirmation(t *testing.T) {
	scanner := &sliceScanner{values: []string{"Cluster reporter", "cluster-reporter", "maybe", "yes"}}
	var out bytes.Buffer
	opt, err := collectCreateOptions(scanner, &out, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if opt.NoApply || !strings.Contains(out.String(), "Answer y or n") {
		t.Fatalf("invalid confirmation was not reprompted: %+v %q", opt, out.String())
	}
}

func TestSlugAgentName(t *testing.T) {
	if got := slugAgentName("  CrashLoop mechanic: prod!  "); got != "crashloop-mechanic-prod" {
		t.Fatalf("slug=%q", got)
	}
}

func TestEditAgentLeavesOriginalOnInvalidCandidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.yaml")
	original := "apiVersion: kagent.dev/v1alpha2\nkind: Agent\nmetadata:\n  name: demo\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(dir, "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'not: [valid' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	t.Setenv("VISUAL", "")
	fakeTool(t, dir, "kubectl", "exit 1")
	t.Setenv("PATH", dir)
	// Validation stops at kubectl; the invalid source must never replace the original.
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := a.EditAgent("demo", path); err == nil {
		t.Fatal("invalid edit was accepted")
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("invalid edit replaced original:\n%s", got)
	}
}

func TestEditAgentRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	link := filepath.Join(dir, "demo.yaml")
	if err := os.WriteFile(target, []byte("kind: Agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}}
	if err := a.EditAgent("demo", link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink edit was not refused: %v", err)
	}
}

func TestCreateRejectsDryRunWithoutApply(t *testing.T) {
	a := &App{}
	err := a.CreateAgent(CreateOptions{Name: "demo", NoApply: true, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("contradictory flags were accepted: %v", err)
	}
}

func TestCreateNoApplyGroupsArtifactCapabilitiesAndNextStep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.yaml")
	var out, errOut bytes.Buffer
	times := []time.Time{time.Unix(0, 0), time.Unix(0, 0), time.Unix(0, 250_000_000), time.Unix(0, 250_000_000)}
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: &out, Stderr: &errOut}, Out: &out, Err: &errOut,
		now: func() time.Time { value := times[0]; times = times[1:]; return value },
	}
	err := a.CreateAgent(CreateOptions{Name: "demo", Description: "Demo agent", ModelConfig: "hello-world-model", Out: path, NoApply: true})
	if err != nil {
		t.Fatal(err)
	}
	text := errOut.String()
	for _, want := range []string{
		"PHASE  [1/1] Generate agent manifest",
		"CAPABILITIES\n  Tools: none",
		"DONE   [1/1] Generate agent manifest (250ms)",
		"COMPLETE  Agent manifest written; not applied (250ms total)",
		"NEXT  Review it, then:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("create transcript lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Validate and apply") || strings.Contains(text, "Wait for agent Ready") {
		t.Fatalf("no-apply transcript included cluster phases:\n%s", text)
	}
}

// A flag a BYO manifest silently drops is worse than no flag — and --tools was
// worse still, because the capabilities report printed the allowlist back for a
// document that had none.
func TestCreateRefusesFlagsABYOManifestWouldDrop(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  CreateOptions
		want string
	}{
		{"tools", CreateOptions{Name: "demo", Image: "acme/a:1", Tools: "srv:t1"}, "--tools"},
		{"instructions", CreateOptions{Name: "demo", Image: "acme/a:1", Instructions: "x.md"}, "--instructions"},
		{"instruction text", CreateOptions{Name: "demo", Image: "acme/a:1", InstructionText: "be brief"}, "--instructions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{}
			err := a.CreateAgent(tc.opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("a flag a BYO agent cannot carry was accepted: %v", err)
			}
		})
	}
	// --model still reaches a real decision — whether the governed seams are
	// injected — so it is not swept up in the refusal.
	if err := refuseFlagsBYODrops(CreateOptions{Image: "acme/a:1", ModelConfig: "governed-ollama"}); err != nil {
		t.Errorf("--model was refused, but it decides whether governance is injected: %v", err)
	}
}

// `--isolation` is refused without `--image` for every spelling, including the
// one that resolves to no placement. Accepting `none` there was the flag doing
// nothing quietly, which is the thing the refusal exists to prevent.
func TestCreateRefusesIsolationWithoutAnImage(t *testing.T) {
	for _, profile := range []string{"none", "virtual-node", "kata"} {
		t.Run(profile, func(t *testing.T) {
			if err := (&App{}).CreateAgent(CreateOptions{Name: "demo", Isolation: profile, NoApply: true}); err == nil ||
				!strings.Contains(err.Error(), "needs --image") {
				t.Fatalf("--isolation %s was accepted without --image: %v", profile, err)
			}
		})
	}
}
