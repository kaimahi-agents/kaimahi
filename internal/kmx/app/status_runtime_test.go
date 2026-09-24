package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// DESIGN.md §4: `kmx status` with no --runtime uses shared platform
// detection, and that detector considers only Orka and kagent-v1. A
// legacy-only quickstart therefore gets the explicit error naming those two
// installations rather than silently selecting legacy kagent.
func TestStatusOmittedRuntimeDetectsOnlyOrkaAndKagentV1(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	err := a.StatusWithOptions(StatusOptions{})
	var absent *agentruntime.NoPlatformInstalledError
	if !errors.As(err, &absent) {
		t.Fatalf("err = %v, not *NoPlatformInstalledError", err)
	}
	for _, want := range []string{"core.orka.ai", "kagent.dev/v1alpha3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
	if out.String() != "" {
		t.Fatalf("a refused status printed:\n%s", out.String())
	}
	if calls := fixtureCalls(t, dir); strings.Contains(calls, "get agents.kagent.dev,modelconfigs,pods") {
		t.Fatalf("a refused status still collected the legacy runtime:\n%s", calls)
	}
}

// Detection selecting Orka without the selectors lifecycle Status needs must
// fail before anything is collected or printed: no partial table, and no
// implication that the ancillary sections were checked.
func TestStatusDetectedOrkaWithoutSelectorsEmitsNoPartialOutput(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			a, out, dir := legacyRuntimeFixture(t)
			t.Setenv("KMX_TEST_ORKA_PLATFORM", "present")
			err := a.StatusWithOptions(StatusOptions{Output: format})
			if err == nil {
				t.Fatal("status collected an Orka runtime it had no selectors for")
			}
			for _, want := range []string{string(agentruntime.Orka), "--namespace", "--agent"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %s: %v", want, err)
				}
			}
			if out.String() != "" {
				t.Fatalf("a refused status printed:\n%s", out.String())
			}
			calls := fixtureCalls(t, dir)
			for _, unwanted := range []string{"get agents.kagent.dev,modelconfigs,pods", "get agents.core.orka.ai", "get deployments"} {
				if strings.Contains(calls, unwanted) {
					t.Errorf("a refused status still read %q:\n%s", unwanted, calls)
				}
			}
		})
	}
}

// With its selectors supplied, detection-selected Orka reports exactly the
// workload state its LifecycleAdapter returns — and nothing that would
// suggest the app-owned governance/Ollama/MCP/certificate sections were read.
func TestStatusDetectedOrkaReportsWorkloadStatusOnly(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	t.Setenv("KMX_TEST_ORKA_PLATFORM", "present")
	if err := a.StatusWithOptions(StatusOptions{Namespace: "team-a", Agent: "concierge"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"concierge", "ready", "active tasks", "last used"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Orka status lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Governance") || strings.Contains(out.String(), "Models") {
		t.Fatalf("Orka status claimed app-owned aggregate sections:\n%s", out.String())
	}
	calls := fixtureCalls(t, dir)
	if !strings.Contains(calls, "get agents.core.orka.ai concierge") {
		t.Fatalf("Orka status did not read the exact Agent:\n%s", calls)
	}
	if strings.Contains(calls, "get agents.kagent.dev,modelconfigs,pods") {
		t.Fatalf("Orka status read the legacy runtime:\n%s", calls)
	}
}

// Orka's status is machine-readable in the same formats, as a closed
// document rather than the legacy kubectl-native envelope.
func TestStatusDetectedOrkaStructuredDocument(t *testing.T) {
	a, out, _ := legacyRuntimeFixture(t)
	t.Setenv("KMX_TEST_ORKA_PLATFORM", "present")
	if err := a.StatusWithOptions(StatusOptions{Output: "json", Namespace: "team-a", Agent: "concierge"}); err != nil {
		t.Fatal(err)
	}
	var document runtimeStatusDocument
	if err := json.Unmarshal([]byte(out.String()), &document); err != nil {
		t.Fatal(err)
	}
	if document.Runtime != string(agentruntime.Orka) || document.Namespace != "team-a" || document.Name != "concierge" {
		t.Fatalf("document = %+v", document)
	}
	if document.Instance != nil {
		t.Fatalf("Orka has no instance section: %+v", document.Instance)
	}
	if len(document.Pair.Fields) == 0 {
		t.Fatal("pair status published no fields")
	}
}

// DESIGN.md §4: explicit legacy `--runtime kagent` preserves the current
// combined status, its non-runtime sections and its runtime line — and that
// runtime line now comes from the LifecycleAdapter's slice.
func TestStatusExplicitKagentPreservesTheCombinedTable(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	if err := a.StatusWithOptions(StatusOptions{Runtime: string(agentruntime.Kagent)}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Kaimahi status", "Agents", "hello-world", "Models", "local",
		"Runtime", "  kagent:     1/2 pods ready, 3 restarts\n",
		"ollama:", "governance:", "Runtime pods", "kagent-controller-0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the combined status lost %q:\n%s", want, text)
		}
	}
	if got := countCalls(fixtureCalls(t, dir), "get agents.kagent.dev,modelconfigs,pods"); got != 1 {
		t.Fatalf("the combined read happened %d times, want exactly one snapshot", got)
	}
}

// The JSON automation shape is unchanged: kubectl's own objects verbatim
// under `items`, inside the governance envelope.
func TestStatusExplicitKagentPreservesJSONItems(t *testing.T) {
	a, out, _ := legacyRuntimeFixture(t)
	if err := a.StatusWithOptions(StatusOptions{Runtime: string(agentruntime.Kagent), Output: "json"}); err != nil {
		t.Fatal(err)
	}
	var document statusDocument
	if err := json.Unmarshal([]byte(out.String()), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Items) != 4 {
		t.Fatalf("items = %d, want the four verbatim kagent objects", len(document.Items))
	}
	if document.Context != "kind-test" {
		t.Fatalf("document = %+v", document)
	}
	if document.Governance.ModelSeams.Total != 1 {
		t.Fatalf("the app-owned governance section was dropped: %+v", document.Governance)
	}
}

// Legacy kagent lists its own fixed namespace. A different one is a
// conflict, and conflicts fail rather than being ignored.
func TestStatusExplicitKagentRefusesSelectorConflicts(t *testing.T) {
	for _, opt := range []StatusOptions{
		{Runtime: string(agentruntime.Kagent), Namespace: "team-a"},
		{Runtime: string(agentruntime.Kagent), Agent: "concierge"},
	} {
		a, out, dir := legacyRuntimeFixture(t)
		err := a.StatusWithOptions(opt)
		if err == nil {
			t.Fatalf("%+v was accepted", opt)
		}
		if out.String() != "" {
			t.Fatalf("a refused status printed:\n%s", out.String())
		}
		if calls := fixtureCalls(t, dir); calls != "" {
			t.Fatalf("a refused status contacted the cluster:\n%s", calls)
		}
	}
}

// Explicit Orka still requires its own selectors, and says so before
// contacting anything.
func TestStatusExplicitOrkaRequiresItsSelectors(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	err := a.StatusWithOptions(StatusOptions{Runtime: string(agentruntime.Orka)})
	if err == nil || !strings.Contains(err.Error(), "--namespace") {
		t.Fatalf("err = %v", err)
	}
	if out.String() != "" || fixtureCalls(t, dir) != "" {
		t.Fatalf("a refused status printed %q and called:\n%s", out.String(), fixtureCalls(t, dir))
	}
}

// kagent-v1 is detected by shared platform detection but not implemented in
// this build, so both an explicit and a detected kagent-v1 resolve to the
// registry's own typed unknown-runtime error, never to another runtime's
// implementation.
func TestStatusKagentV1IsNotYetRegistered(t *testing.T) {
	a, out, _ := legacyRuntimeFixture(t)
	t.Setenv("KMX_TEST_KAGENTV1_PLATFORM", "present")
	err := a.StatusWithOptions(StatusOptions{Namespace: "kagent", Agent: "hello"})
	var unknown *agentruntime.UnknownRuntimeError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, not *UnknownRuntimeError", err)
	}
	if unknown.Runtime != agentruntime.KagentV1 {
		t.Fatalf("unknown = %+v", unknown)
	}
	if out.String() != "" {
		t.Fatalf("an unresolved runtime printed:\n%s", out.String())
	}
}

// An unknown explicit runtime is the registry's typed failure, decided
// before any cluster read.
func TestStatusRejectsAnUnknownRuntime(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	err := a.StatusWithOptions(StatusOptions{Runtime: "bogus"})
	var unknown *agentruntime.UnknownRuntimeError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, not *UnknownRuntimeError", err)
	}
	if out.String() != "" || fixtureCalls(t, dir) != "" {
		t.Fatalf("an unknown runtime printed %q and called:\n%s", out.String(), fixtureCalls(t, dir))
	}
}
