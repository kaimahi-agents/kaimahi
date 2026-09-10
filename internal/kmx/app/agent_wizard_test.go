package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

func TestCreateNoApplyDoesNotSuggestBulkApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent's $(false).yaml")
	var out, errOut bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-team's test"}, Out: &out, Err: &errOut}
	if err := a.CreateAgent(CreateOptions{Name: "demo", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "key", Out: path, NoApply: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), " apply -f ") || !strings.Contains(errOut.String(), "Create Provider only") {
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
	scanner := &sliceScanner{values: []string{"Reports unhealthy workloads", "", "orka-system", "openai", "local", "model-key", ""}}
	var out bytes.Buffer
	opt, err := collectCreateOptions(scanner, &out, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if opt.Description != "Reports unhealthy workloads" || opt.Name != "reports-unhealthy-workloads" || opt.Namespace != "orka-system" || opt.ProviderType != "openai" || opt.Model != "local" || opt.Secret != "model-key" {
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
	scanner := &sliceScanner{values: []string{"Cluster reporter", "cluster-reporter", "orka-system", "openai", "local", "model-key"}}
	opt, err := collectCreateOptions(scanner, &bytes.Buffer{}, CreateOptions{NoApply: true})
	if err != nil {
		t.Fatal(err)
	}
	if !opt.NoApply {
		t.Fatal("explicit --no-apply was lost")
	}
}

func TestCreateWizardDeclineCancels(t *testing.T) {
	scanner := &sliceScanner{values: []string{"Cluster reporter", "cluster-reporter", "orka-system", "openai", "local", "model-key", "no"}}
	_, err := collectCreateOptions(scanner, &bytes.Buffer{}, CreateOptions{})
	if !errors.Is(err, errCreateCancelled) {
		t.Fatalf("decline did not cancel creation: %v", err)
	}
}

func TestCreateWizardRepromptsInvalidConfirmation(t *testing.T) {
	scanner := &sliceScanner{values: []string{"Cluster reporter", "cluster-reporter", "orka-system", "openai", "local", "model-key", "maybe", "yes"}}
	var out bytes.Buffer
	opt, err := collectCreateOptions(scanner, &out, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if opt.NoApply || !strings.Contains(out.String(), "Answer y or n") {
		t.Fatalf("invalid confirmation was not reprompted: %+v %q", opt, out.String())
	}
}

func TestCreateWizardScannerDoesNotAddInstructionsToBYOAgent(t *testing.T) {
	scanner := &sliceScanner{values: []string{"Existing agent", "existing-agent"}}
	opt, err := collectCreateOptions(scanner, &bytes.Buffer{}, CreateOptions{Image: "acme/agent:1", NoApply: true})
	if err != nil {
		t.Fatal(err)
	}
	if opt.InstructionText != "" {
		t.Fatalf("BYO scanner fallback synthesized declarative instructions: %q", opt.InstructionText)
	}
}

func TestSlugAgentName(t *testing.T) {
	if got := slugAgentName("  CrashLoop mechanic: prod!  "); got != "crashloop-mechanic-prod" {
		t.Fatalf("slug=%q", got)
	}
	long := "Reports unhealthy workloads across every production namespace"
	got := slugAgentName(long)
	if len(got) > 32 || !regexp.MustCompile(`^reports-unhealthy-[a-f0-9]{6}$`).MatchString(got) {
		t.Fatalf("long derived name is not compact and stable: %q", got)
	}
	if again := slugAgentName(long); again != got {
		t.Fatalf("derived name changed between calls: %q != %q", got, again)
	}
	other := slugAgentName(long + " in Europe")
	if other == got || !strings.HasPrefix(other, "reports-unhealthy-") {
		t.Fatalf("same-prefix descriptions collided or lost readability: %q and %q", got, other)
	}
	if name := slugAgentName("Résumé incidents in production namespaces with detailed remediation"); strings.Contains(name, "é") || len(name) > 32 {
		t.Fatalf("derived name is not DNS-safe: %q", name)
	}
}

func wizardKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func updateCreateWizard(t *testing.T, m createWizardModel, msg tea.Msg) createWizardModel {
	t.Helper()
	updated, _ := m.Update(msg)
	return updated.(createWizardModel)
}

func TestCreateWizardModelCollectsMissingFieldsAndAppliesByDefault(t *testing.T) {
	m, err := newCreateWizardModel(CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.step != createDescription || m.err == nil || !strings.Contains(m.View().Content, "description is required") {
		t.Fatalf("empty description advanced: step=%d err=%v", m.step, m.err)
	}
	m.input.SetValue("Reports unhealthy workloads")
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.step != createName || m.input.Value() != "reports-unhealthy-workloads" {
		t.Fatalf("description did not derive the name carefully: step=%d name=%q", m.step, m.input.Value())
	}
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.step != createConfirm || m.selection != 0 {
		t.Fatalf("wizard did not reach apply-default confirmation: step=%d selection=%d", m.step, m.selection)
	}
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.cancelled || m.opt.NoApply || m.opt.Name != "reports-unhealthy-workloads" || m.opt.Namespace != config.DefaultNamespace {
		t.Fatalf("unexpected completed options: %+v cancelled=%v", m.opt, m.cancelled)
	}
}

func TestCreateWizardModelValidatesInlineAndPreservesFlags(t *testing.T) {
	m, err := newCreateWizardModel(CreateOptions{
		Description: "Supplied description", Namespace: "team", Out: "custom.yaml",
		NoApply: true, Tools: "server:read", Instructions: "prompt.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.step != createName {
		t.Fatalf("supplied description was prompted again: step=%d", m.step)
	}
	m.input.SetValue("Not Valid")
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.step != createName || m.err == nil || !strings.Contains(m.View().Content, m.err.Error()) {
		t.Fatalf("invalid name was not retained inline: step=%d err=%v", m.step, m.err)
	}
	m.input.SetValue("valid-name")
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.step != createDone || m.opt.Description != "Supplied description" || m.opt.Namespace != "team" || m.opt.Out != "custom.yaml" || !m.opt.NoApply || m.opt.InstructionText != "" {
		t.Fatalf("authoritative flags changed: %+v", m.opt)
	}
}

func TestCreateWizardModelCancelKeysAndVisibleSelection(t *testing.T) {
	for _, code := range []rune{tea.KeyEscape, 'c'} {
		m, err := newCreateWizardModel(CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		msg := wizardKey(code)
		if code == 'c' {
			msg = tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})
		}
		m = updateCreateWizard(t, m, msg)
		if !m.cancelled || m.step != createDone {
			t.Fatalf("%s did not cancel", msg.String())
		}
	}

	m, err := newCreateWizardModel(CreateOptions{Name: "demo", Description: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "> Apply") || !strings.Contains(view, "  Cancel") {
		t.Fatalf("confirmation is not an explicit visible selection:\n%s", view)
	}
	m = updateCreateWizard(t, m, wizardKey(tea.KeyRight))
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if !m.cancelled {
		t.Fatal("explicit Cancel selection applied")
	}
}

func TestCreateWizardFilterTreatsExternalQuitAsCancellation(t *testing.T) {
	m, err := newCreateWizardModel(CreateOptions{Name: "demo", Description: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cancelUnfinishedWizard(m, tea.QuitMsg{}).(tea.InterruptMsg); !ok {
		t.Fatal("external quit at confirmation was allowed to apply")
	}
	m.step = createDone
	if _, ok := cancelUnfinishedWizard(m, tea.QuitMsg{}).(tea.QuitMsg); !ok {
		t.Fatal("intentional completion was converted to cancellation")
	}
}

func TestCreateWizardModelRejectsInvalidToolsBeforeStarting(t *testing.T) {
	if _, err := newCreateWizardModel(CreateOptions{Tools: "server:"}); err == nil {
		t.Fatal("invalid --tools reached the wizard")
	}
}

func TestCreateWizardModelRejectsInvalidSuppliedNameBeforeStarting(t *testing.T) {
	if _, err := newCreateWizardModel(CreateOptions{Name: "Not Valid", Description: "Supplied"}); err == nil {
		t.Fatal("invalid supplied --name reached the wizard")
	}
}

func TestCreateWizardModelSupportsBYOAndLongDescriptions(t *testing.T) {
	long := strings.Repeat("description ", 30)
	m, err := newCreateWizardModel(CreateOptions{Image: "acme/agent:1"})
	if err != nil {
		t.Fatal(err)
	}
	m.input.SetValue(long)
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.opt.Description != strings.TrimSpace(long) || m.input.Value() == "" {
		t.Fatalf("long description was truncated: %d vs %d", len(m.opt.Description), len(strings.TrimSpace(long)))
	}
	m = updateCreateWizard(t, m, wizardKey(tea.KeyEnter))
	if m.opt.InstructionText != "" {
		t.Fatalf("BYO wizard synthesized declarative instructions: %q", m.opt.InstructionText)
	}
}

func TestCreateWizardConfirmationSanitizesFlagValues(t *testing.T) {
	m, err := newCreateWizardModel(CreateOptions{Name: "demo", Description: "safe\x1b[2J\nforged", Namespace: "team\nother", Out: "file\x1b]52;c;secret\a"})
	if err != nil {
		t.Fatal(err)
	}
	view := m.View().Content
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]52") || strings.Contains(view, "\nforged") || strings.Contains(view, "\nother") {
		t.Fatalf("flag value escaped confirmation hierarchy: %q", view)
	}
	plain := ansi.Strip(view)
	for _, want := range []string{"safe forged", "team other", "Output:      file"} {
		if !strings.Contains(plain, want) {
			t.Errorf("sanitized confirmation lacks %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "secret") {
		t.Fatalf("OSC clipboard payload survived confirmation: %q", plain)
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
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: &out, Stderr: &errOut}, Out: &out, Err: &errOut,
	}
	err := a.CreateAgent(CreateOptions{Name: "demo", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "key", Description: "Demo agent", Out: path, NoApply: true})
	if err != nil {
		t.Fatal(err)
	}
	text := errOut.String()
	for _, want := range []string{
		"Orka bundle not applied", "Schema:", "Create Provider only", "never write the skeleton", "does not test admission",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("create transcript lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Validate and apply") || strings.Contains(text, "Wait for agent Ready") {
		t.Fatalf("no-apply transcript included cluster phases:\n%s", text)
	}
}

func TestCreateWizardTaskRequestsExistingResultAccount(t *testing.T) {
	scanner := &sliceScanner{values: []string{"demo", "demo", "orka-system", "openai", "local", "model-key", "reader", "yes"}}
	opt, err := collectCreateOptions(scanner, &bytes.Buffer{}, CreateOptions{Task: "Say hello"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.ResultServiceAccount != "reader" {
		t.Fatalf("missing explicit account: %+v", opt)
	}
}

func TestCreateWizardStdoutTaskDoesNotRequireResultAccount(t *testing.T) {
	scanner := &sliceScanner{values: []string{"demo", "demo", "orka-system", "openai", "local", "model-key"}}
	opt, err := collectCreateOptions(scanner, &bytes.Buffer{}, CreateOptions{Out: "-", Task: "Say hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !opt.NoApply || opt.ResultServiceAccount != "" {
		t.Fatal("stdout wizard required executable result options")
	}
}

func TestCreateWizardRefusesKeyShapedDescriptionBeforePromptEcho(t *testing.T) {
	secret := "sk-" + "proj-" + strings.Repeat("B", 32)
	scanner := &sliceScanner{values: []string{secret, "demo"}}
	var out bytes.Buffer
	_, err := collectCreateOptions(scanner, &out, CreateOptions{NoApply: true})
	if err == nil || strings.Contains(out.String()+err.Error(), strings.ToLower(secret)) {
		t.Fatal("wizard echoed key-shaped input")
	}
}

func TestEditorParsingPinsContext(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `[ "$1" = "--context" ] && [ "$2" = "kind-test" ] || exit 1
 printf '%s' '{"apiVersion":"kagent.dev/v1alpha2","kind":"Agent","metadata":{"name":"demo","namespace":"kagent"},"spec":{"type":"Declarative","declarative":{"modelConfig":"local"}}}'`)
	t.Setenv("PATH", dir)
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}}
	if _, err := a.validateAgentEdit("demo.yaml", "demo"); err != nil {
		t.Fatal(err)
	}
}
