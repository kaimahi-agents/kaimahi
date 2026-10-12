package app

import (
	"bytes"
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
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
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
	// Expire at Ready, leaving the original setup budget untouched.
	deadline := orkaTestDeadline(t, a, "orka-ready-provider")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	a.Run.Context = ctx
	err := a.CreateAgent(opt)
	if err == nil || deadline().Err() != context.DeadlineExceeded {
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
	a.waitTiming = fastWaitTiming()
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

func TestOrkaCreatePreviewThenApplyKeepsArtifact(t *testing.T) {
	a, opt, _, diagnostics, dir := orkaCreateFixture(t, "v0.2.0")
	opt.NoApply = true
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	preview, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(preview, []byte("# Orka v0.2.0 at ")) || len(orkaCalls(t, dir)) != 0 {
		t.Fatal("preview did not use embedded schemas offline")
	}
	opt.NoApply = false
	diagnostics.Reset()
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("preview-to-apply refused: %v", err)
	}
	if got := orkaWriteKinds(orkaClusterWrites(t, dir, 0)); !slices.Equal(got, []string{"Provider", "Agent"}) {
		t.Fatalf("applying preview wrote %v", got)
	}
	crds, admission := 0, 0
	for _, call := range orkaCalls(t, dir) {
		if slices.Contains(call.Args, "crd") {
			crds++
		}
		if slices.Contains(call.Args, "--dry-run=server") && slices.Contains(call.Args, "--validate=strict") {
			admission++
		}
	}
	// Each resource has two create dry-runs and two ownership replace dry-runs:
	// preflight, immediate pre-write inspection, Ready and final receipt checks.
	if crds != 3 || admission != 8 {
		t.Fatalf("preview bypassed online validation: CRDs=%d admission=%d", crds, admission)
	}
	if kept, err := os.ReadFile(opt.Out); err != nil || !bytes.Equal(kept, preview) {
		t.Fatalf("applying changed the preview: %v", err)
	}
	if !strings.Contains(diagnostics.String(), "original schema-source comment retained; this invocation validated against installed CRDs and server admission") {
		t.Fatalf("schema validation distinction not reported: %s", diagnostics)
	}
	before := len(orkaCalls(t, dir))
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("rerun with retained preview refused: %v", err)
	}
	if writes := orkaClusterWrites(t, dir, before); len(writes) != 0 {
		t.Fatalf("rerun wrote %v", orkaWriteKinds(writes))
	}
}

func TestOrkaCreateRefusesArtifactEditObservedAtEmission(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "v0.2.0")
	opt.NoApply = true
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	preview, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	edited := append(preview, []byte("# operator edit after preflight\n")...)
	observed := false
	a.operationProgress = func(stage, status string, _ error) {
		if stage == "Validate server admission" && status == "active" {
			observed = true
			if err := os.WriteFile(opt.Out, edited, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	opt.NoApply = false
	err = a.CreateAgent(opt)
	if !observed || err == nil || !strings.Contains(err.Error(), opt.Out+" already exists and differs") {
		t.Fatalf("late artifact edit not refused at emission: observed=%t err=%v", observed, err)
	}
	if writes := orkaClusterWrites(t, dir, 0); len(writes) != 0 {
		t.Fatalf("wrote despite observed artifact edit: %v", orkaWriteKinds(writes))
	}
	if kept, err := os.ReadFile(opt.Out); err != nil || !bytes.Equal(kept, edited) {
		t.Fatalf("changed edited preview: %v", err)
	}
}

func TestOrkaArtifactReuseRefusesEveryOtherByteDifference(t *testing.T) {
	// Include provenance-looking text inside resource content: it must not
	// expand the one generated header block exempted from comparison.
	documents := [][]byte{[]byte("kind: Agent\nspec:\n  instructions: |\n    # Orka bundle, scaffolded by kmx. Schema source:\n    # embedded\n")}
	preview, err := scaffold.OrkaArtifact("embedded", documents)
	if err != nil {
		t.Fatal(err)
	}
	online, err := scaffold.OrkaArtifact("installed", documents)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "preview.yaml")
	var diagnostics bytes.Buffer
	a := &App{Err: &diagnostics}
	opt := CreateOptions{Out: path}
	if err := os.WriteFile(path, []byte(preview), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.emitOrkaArtifact(opt, online, true); err != nil {
		t.Fatalf("provenance-only difference refused: %v", err)
	}
	if kept, err := os.ReadFile(path); err != nil || string(kept) != preview {
		t.Fatalf("preview changed: %v", err)
	}
	start := strings.Index(preview, "# embedded\n")
	end := start + len("# embedded\n")
	for i := range len(preview) {
		if i >= start && i < end {
			continue
		}
		edited := []byte(preview)
		edited[i] ^= 1
		if err := os.WriteFile(path, edited, 0600); err != nil {
			t.Fatal(err)
		}
		if err := preflightOrkaArtifact(opt, documents); err == nil || !strings.Contains(err.Error(), path+" already exists and differs") {
			t.Fatalf("byte %d difference passed preflight: %v", i, err)
		}
		err := a.emitOrkaArtifact(opt, online, true)
		if err == nil || !strings.Contains(err.Error(), path+" already exists and differs") {
			t.Fatalf("byte %d difference not refused: %v", i, err)
		}
		if kept, err := os.ReadFile(path); err != nil || !bytes.Equal(kept, edited) {
			t.Fatalf("byte %d changed edited artifact: %v", i, err)
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
	prepared, err := agentruntime.PreparePortableRender(source, adapter)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _, err := adapter.renderOrka(prepared)
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
