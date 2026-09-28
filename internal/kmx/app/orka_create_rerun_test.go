package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// orkaClusterWrites counts the calls, from index from onward, that could
// change the cluster: creates, replaces and patches that are not dry-runs.
func orkaClusterWrites(t *testing.T, dir string, from int) []orkaCall {
	t.Helper()
	var writes []orkaCall
	for _, c := range orkaCalls(t, dir)[from:] {
		if slices.Contains(c.Args, "--dry-run=server") || slices.Contains(c.Args, "token") {
			continue
		}
		if slices.Contains(c.Args, "create") || slices.Contains(c.Args, "replace") || slices.Contains(c.Args, "patch") {
			writes = append(writes, c)
		}
	}
	return writes
}

func orkaWriteKinds(writes []orkaCall) []string {
	var kinds []string
	for _, c := range writes {
		if i := slices.Index(c.Args, "patch"); i >= 0 {
			kinds = append(kinds, "markers:"+c.Args[i+1])
			continue
		}
		kinds = append(kinds, fmt.Sprint(c.Document["kind"]))
	}
	return kinds
}

// A create that stopped while its Provider never became Ready has already
// written the artifact, the bundle and the Provider. Rerunning the same
// command reuses all three and carries on with the Agent.
func TestOrkaCreateRerunAfterProviderReadyTimeout(t *testing.T) {
	a, opt, _, diagnostics, dir := orkaCreateFixture(t, "")
	flag := filepath.Join(dir, "provider-not-ready")
	if err := os.WriteFile(flag, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Room for race-instrumented helper startup before the Ready poll begins.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	a.Run.Context = ctx
	err := a.CreateAgent(opt)
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("unready Provider did not stop the create: %v", err)
	}
	if !strings.Contains(err.Error(), "Rerunning the same command is safe") {
		t.Fatalf("failed create does not say a rerun is safe: %v", err)
	}
	if got := orkaWriteKinds(orkaClusterWrites(t, dir, 0)); !slices.Equal(got, []string{"Provider"}) {
		t.Fatalf("first attempt writes = %v", got)
	}
	if _, err := os.Stat(opt.Out); err != nil {
		t.Fatalf("first attempt did not leave its artifact: %v", err)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	a.Run.Context = t.Context()
	diagnostics.Reset()
	before := len(orkaCalls(t, dir))
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("identical rerun refused: %v", err)
	}
	if got := orkaWriteKinds(orkaClusterWrites(t, dir, before)); !slices.Equal(got, []string{"Agent"}) {
		t.Fatalf("rerun writes = %v, want only the Agent created", got)
	}
	for _, want := range []string{"Provider/sample (UID provider-uid) reused", "Created Agent/sample (UID agent-uid)", "kept " + opt.Out} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Errorf("rerun output lacks %q:\n%s", want, diagnostics)
		}
	}
}

func TestOrkaCreateRerunAfterSuccessReusesBoth(t *testing.T) {
	a, opt, _, diagnostics, dir := orkaCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics.Reset()
	before := len(orkaCalls(t, dir))
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("identical rerun refused: %v", err)
	}
	if writes := orkaClusterWrites(t, dir, before); len(writes) != 0 {
		t.Fatalf("rerun of a finished create wrote: %v", orkaWriteKinds(writes))
	}
	for _, want := range []string{"Provider/sample (UID provider-uid) reused", "Agent/sample (UID agent-uid) reused", "no model response was tested"} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Errorf("rerun output lacks %q:\n%s", want, diagnostics)
		}
	}
	if again, err := os.ReadFile(opt.Out); err != nil || string(again) != string(artifact) {
		t.Fatalf("rerun changed the artifact: %v", err)
	}

	// The same outcomes reach the lifecycle receipt.
	adapter := orkaRuntimeAdapter{app: a, create: &opt, staged: true}
	source, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _, err := adapter.renderOrka(source)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Deploy(t.Context(), rendered, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Receipt.Resources) != 2 || result.Receipt.Resources[0].Outcome != "reused" || result.Receipt.Resources[1].Outcome != "reused" || result.Ref.UID != "agent-uid" {
		t.Fatalf("rerun receipt: %+v", result)
	}
}

// An identical unmarked Provider is adopted — marked as this bundle's — and
// the create continues; nothing about the Provider's spec is changed.
func TestOrkaCreateAdoptsIdenticalUnmarkedProvider(t *testing.T) {
	a, opt, _, diagnostics, dir := orkaCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	// Leave only an unmarked Provider identical to the render.
	provider := filepath.Join(dir, "sample-providers.core.orka.ai.json")
	raw, err := os.ReadFile(provider)
	if err != nil {
		t.Fatal(err)
	}
	unmarked := strings.NewReplacer(`"kaimahi.dev/bundle"`, `"x-bundle"`, `"kaimahi.dev/portable-digest"`, `"x-portable"`, `"kaimahi.dev/rendered-digest"`, `"x-rendered"`).Replace(string(raw))
	if err := os.WriteFile(provider, []byte(unmarked), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "sample-agents.core.orka.ai.json"), opt.Out} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	diagnostics.Reset()
	before := len(orkaCalls(t, dir))
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("identical unmarked Provider not adopted: %v", err)
	}
	writes := orkaClusterWrites(t, dir, before)
	if got := orkaWriteKinds(writes); !slices.Equal(got, []string{"Provider", "Agent"}) || !slices.Contains(writes[0].Args, "replace") {
		t.Fatalf("adoption writes = %v", writes)
	}
	if !strings.Contains(diagnostics.String(), "Adopting identical unmarked Provider/sample") {
		t.Fatalf("adoption not reported:\n%s", diagnostics)
	}
}

func TestOrkaCreateRerunRefusesDifferingArtifact(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	edited := []byte("# operator note\n")
	body, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	edited = append(body, edited...)
	if err := os.WriteFile(opt.Out, edited, 0600); err != nil {
		t.Fatal(err)
	}
	before := len(orkaCalls(t, dir))
	err = a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), opt.Out+" already exists and differs") || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("differing artifact not refused by name: %v", err)
	}
	if calls := orkaCalls(t, dir)[before:]; len(calls) != 0 {
		t.Fatalf("reached kubectl before the local refusal: %+v", calls)
	}
	if kept, _ := os.ReadFile(opt.Out); string(kept) != string(edited) {
		t.Fatal("differing artifact was changed")
	}
}

func TestOrkaCreateRefusesUnmarkedDifferingAgent(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "")
	seedOrkaCreateObject(t, dir, map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Agent",
		"metadata": map[string]any{"name": "sample", "namespace": "orka-system", "uid": "someone-else", "resourceVersion": "3", "generation": 1},
		"spec":     map[string]any{"providerRef": map[string]any{"name": "sample"}, "systemPrompt": map[string]any{"inline": "theirs"}}})
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "Agent/sample has no bundle marker and differs") {
		t.Fatalf("unmarked differing Agent not refused: %v", err)
	}
	// Both resources are inspected before the first write, so the Provider
	// is not created either.
	if writes := orkaClusterWrites(t, dir, 0); len(writes) != 0 {
		t.Fatalf("wrote despite a foreign Agent: %v", orkaWriteKinds(writes))
	}
}

// A Task's name is random, so a --task rerun never matches its old artifact:
// it is refused with advice to choose a new --out, and with one the Provider
// and Agent are reused and a new Task is submitted. The rendered digest
// covers the Task, so reuse refreshes only the ownership markers.
func TestOrkaCreateRerunWithTask(t *testing.T) {
	a, opt, out, diagnostics, dir := orkaCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
		if _, err := os.Stat(filepath.Join(dir, task+"-tasks.core.orka.ai.json")); err != nil {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
		fmt.Fprint(w, `{"result":"hello"}`)
	})
	before := len(orkaCalls(t, dir))
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "--task names a fresh Task") || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("--task rerun onto the old artifact not refused: %v", err)
	}
	if calls := orkaCalls(t, dir)[before:]; len(calls) != 0 {
		t.Fatalf("reached kubectl before the local refusal: %+v", calls)
	}

	opt.Out = filepath.Join(dir, "with-task.yaml")
	diagnostics.Reset()
	before = len(orkaCalls(t, dir))
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("--task rerun with a new --out refused: %v", err)
	}
	if got := orkaWriteKinds(orkaClusterWrites(t, dir, before)); !slices.Equal(got, []string{"markers:providers.core.orka.ai", "markers:agents.core.orka.ai", "Task"}) {
		t.Fatalf("--task rerun writes = %v, want marker refreshes and a new Task", got)
	}
	if out.String() != "hello\n" || !strings.Contains(diagnostics.String(), "Agent/sample (UID agent-uid) reused") {
		t.Fatalf("--task rerun output: %q\n%s", out, diagnostics)
	}
}
