package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"go.yaml.in/yaml/v3"
)

type orkaCall struct {
	Args     []string
	Document map[string]any
	Patch    []map[string]any
}

func orkaTestToken() string { return "private-" + "session-token-never-print" }

func orkaCreateFixture(t *testing.T, scenario string) (*App, CreateOptions, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestOrkaKubectlHelper"))
	fixtures, err := filepath.Abs("../orkaschema/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_ORKA_TEST_DIR", dir)
	t.Setenv("KMX_ORKA_TEST_FIXTURES", fixtures)
	t.Setenv("KMX_ORKA_TEST_SCENARIO", scenario)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	out, diagnostics := &bytes.Buffer{}, &bytes.Buffer{}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Out: out, Err: diagnostics, Run: &run.Runner{Stdout: out, Stderr: diagnostics, Echo: true}, waitTiming: fastWaitTiming()}
	opt := CreateOptions{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "model-key", Out: filepath.Join(dir, "bundle.yaml"), BundlePath: filepath.Join(dir, "agents", "sample")}
	return a, opt, out, diagnostics, dir
}

const orkaTestWaitTimeout = 250 * time.Millisecond

// orkaTestDeadline starts a short deadline only after the intended boundary.
// Every other phase keeps its production cap, including preflight and token
// lifetime validation. The returned context proves expiry at that boundary.
func orkaTestDeadline(t *testing.T, a *App, phase string) func() context.Context {
	t.Helper()
	timing := fastWaitTiming()
	var reached context.Context
	timing.timeout = func(parent context.Context, current string, original time.Duration) (context.Context, context.CancelFunc) {
		if current == phase {
			var cancel context.CancelFunc
			reached, cancel = context.WithTimeout(parent, orkaTestWaitTimeout)
			return reached, cancel
		}
		return (&App{}).waitContext(parent, current, original)
	}
	a.waitTiming = timing
	check := func() context.Context {
		t.Helper()
		if reached == nil {
			t.Fatalf("never reached deadline phase %q", phase)
		}
		return reached
	}
	t.Cleanup(func() { check() })
	return check
}

// seedOrkaCreateObject stores a live object where the create fake reads it.
func seedOrkaCreateObject(t *testing.T, dir string, object map[string]any) {
	t.Helper()
	body, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	meta := object["metadata"].(map[string]any)
	if err := os.WriteFile(filepath.Join(dir, meta["name"].(string)+"-"+orkaPlural(object["kind"].(string))+".json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func orkaCalls(t *testing.T, dir string) []orkaCall {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var calls []orkaCall
	dec := json.NewDecoder(f)
	for {
		var c orkaCall
		err := dec.Decode(&c)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}

func TestOrkaOnlineCreatesSeparateStrictObjectsAndWaitsInOrder(t *testing.T) {
	a, opt, out, diagnostics, dir := orkaCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range orkaCalls(t, dir) {
		if len(c.Args) < 2 || c.Args[0] != "--context" || c.Args[1] != "kind-test" {
			t.Fatalf("unpinned: %v", c.Args)
		}
		if c.Document != nil {
			kind := c.Document["kind"].(string)
			replace := slices.Contains(c.Args, "replace") && slices.Contains(c.Args, "--dry-run=server")
			if kind == "Secret" || !slices.Contains(c.Args, "create") && !replace || !slices.Contains(c.Args, "--validate=strict") {
				t.Fatalf("unsafe write: %+v", c)
			}
			if annotations, _ := c.Document["metadata"].(map[string]any)["annotations"].(map[string]any); annotations[orkaBundleMarker] != "sample" {
				t.Fatalf("write without this bundle's ownership marker: %+v", c)
			}
			switch {
			case replace:
				order = append(order, "own-"+kind)
			case slices.Contains(c.Args, "--dry-run=server"):
				order = append(order, "validate-"+kind)
			default:
				order = append(order, "create-"+kind)
			}
		} else if slices.Contains(c.Args, "get") && !slices.Contains(c.Args, "crd") && slices.Contains(c.Args, "json") {
			kind := map[string]string{"providers.core.orka.ai": "Provider", "agents.core.orka.ai": "Agent"}[c.Args[slices.Index(c.Args, "get")+1]]
			if slices.Contains(c.Args, "--ignore-not-found=true") {
				order = append(order, "inspect-"+kind)
			} else {
				order = append(order, "ready-"+kind)
			}
		}
	}
	// Create reconciles: both resources are inspected and admitted before any
	// write, each is reinspected immediately before its own create, and after
	// Ready its ownership is confirmed again. Provider is still created and
	// Ready before the Agent is written, and a successful Deploy checks both
	// again before issuing its receipt.
	want := []string{
		"inspect-Provider", "validate-Provider", "inspect-Agent", "validate-Agent",
		"inspect-Provider", "validate-Provider", "create-Provider", "ready-Provider", "inspect-Provider", "own-Provider", "ready-Provider",
		"inspect-Agent", "validate-Agent", "create-Agent", "ready-Agent", "inspect-Agent", "own-Agent", "ready-Agent",
		"inspect-Provider", "own-Provider", "ready-Provider", "inspect-Agent", "own-Agent", "ready-Agent",
	}
	if !slices.Equal(order, want) {
		t.Fatalf("order=%v", order)
	}
	if out.Len() != 0 || !strings.Contains(diagnostics.String(), "no model response was tested") {
		t.Fatalf("misleading output %s %s", out, diagnostics)
	}
}

// TestOrkaOnlineDeploysExactlyTheRenderedBytes is the end-to-end statement of
// the render-once rule. A Task's name carries 16 random bytes, so a second
// generation anywhere between rendering and applying would write an object
// the operator's artifact does not describe.
//
// The Task case runs as a server dry-run because that is the longest path a
// Task can take without a live result endpoint, and it still spans the two
// points a re-render could happen between: the strict admission check sends
// the bytes, and the emitted artifact is assembled from them. The no-Task
// case then runs a real create and requires that what the server was asked to
// write is what the artifact describes, skeleton excluded.
func TestOrkaOnlineDeploysExactlyTheRenderedBytes(t *testing.T) {
	t.Run("task identity is minted once", func(t *testing.T) {
		a, opt, _, _, dir := orkaCreateFixture(t, "")
		opt.Task, opt.DryRun = "Say hello", true
		if err := a.CreateAgent(opt); err != nil {
			t.Fatal(err)
		}
		artifact, err := os.ReadFile(opt.Out)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, c := range orkaCalls(t, dir) {
			if c.Document == nil || c.Document["kind"] != "Task" {
				continue
			}
			metadata := c.Document["metadata"].(map[string]any)
			names = append(names, metadata["name"].(string))
		}
		if len(names) != 1 {
			t.Fatalf("expected the Task to be sent exactly once, got %v", names)
		}
		if !strings.Contains(string(artifact), "name: "+names[0]+"\n") {
			t.Fatalf("the emitted artifact does not describe the Task that was sent (%s):\n%s", names[0], artifact)
		}
	})
	t.Run("the artifact describes what was written", func(t *testing.T) {
		a, opt, _, _, dir := orkaCreateFixture(t, "")
		if err := a.CreateAgent(opt); err != nil {
			t.Fatal(err)
		}
		artifact, err := os.ReadFile(opt.Out)
		if err != nil {
			t.Fatal(err)
		}
		// The artifact is assembled from the rendered bytes, so it still
		// carries the review-only Secret prerequisite it always did.
		if !strings.Contains(string(artifact), "kind: Secret") {
			t.Fatalf("the emitted artifact lost the Secret prerequisite:\n%s", artifact)
		}
		written := 0
		for _, c := range orkaCalls(t, dir) {
			if c.Document == nil || slices.Contains(c.Args, "replace") {
				continue // Replace dry-runs echo the live object, not a render.
			}
			if c.Document["kind"] == "Secret" {
				t.Fatalf("the Secret skeleton was sent to the server: %v", c.Args)
			}
			// Ownership markers are written beside the rendered bytes, never
			// into them: a digest cannot be part of what it hashes.
			metadata := c.Document["metadata"].(map[string]any)
			annotations, _ := metadata["annotations"].(map[string]any)
			for _, marker := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker, orkaOriginMarker} {
				if annotations[marker] == nil {
					t.Fatalf("%s written without %s", c.Document["kind"], marker)
				}
				delete(annotations, marker)
			}
			if len(annotations) == 0 {
				delete(metadata, "annotations")
			}
			encoded, err := yaml.Marshal(c.Document)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(artifact), string(encoded)) {
				t.Fatalf("a document sent to the server is not in the artifact:\n%s", encoded)
			}
			written++
		}
		if written == 0 {
			t.Fatal("no documents were sent, so the comparison proved nothing")
		}
	})
}

func TestOrkaPreflightFailureNeverEmitsOrMutates(t *testing.T) {
	// An identical unmarked Provider is no longer a collision: create adopts
	// it (TestOrkaCreateRerun covers that). A differing one still refuses.
	for _, scenario := range []string{"guard", "missing-crd", "denied-crd", "collision", "denied-collision", "missing-secret", "missing-key", "denied-secret", "invalid-marker", "admission", "main", "task-collision"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, out, diagnostics, dir := orkaCreateFixture(t, scenario)
			if scenario == "collision" {
				seedOrkaCreateObject(t, dir, map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider",
					"metadata": map[string]any{"name": "sample", "namespace": "orka-system", "uid": "someone-else", "resourceVersion": "7", "generation": 1},
					"spec":     map[string]any{"type": "openai", "defaultModel": "theirs", "secretRef": map[string]any{"name": "model-key", "key": "api-key"}}})
			}
			if scenario == "main" {
				opt.AgentRequestsPerMinute = "1"
			}
			if scenario == "task-collision" {
				opt.Task = "Say hello"
				opt.ResultServiceAccount = "reader"
			}
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if out.Len() != 0 {
				t.Fatal("preflight emitted output")
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("preflight wrote artifact")
			}
			for _, c := range orkaCalls(t, dir) {
				if slices.Contains(c.Args, "create") && !slices.Contains(c.Args, "--dry-run=server") {
					t.Fatalf("mutated: %v", c.Args)
				}
			}
			if strings.Contains(fmt.Sprint(err)+diagnostics.String(), orkaTestToken()) {
				t.Fatal("private subprocess output leaked")
			}
		})
	}
}

func TestOrkaReadinessFailureStopsSubsequentCreates(t *testing.T) {
	for _, scenario := range []string{"replacement", "changed-generation", "create-race"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, scenario)
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("accepted failed or replaced Provider")
			}
			for _, c := range orkaCalls(t, dir) {
				if c.Document != nil && !slices.Contains(c.Args, "--dry-run=server") && c.Document["kind"] != "Provider" {
					t.Fatalf("continued after failure: %+v", c)
				}
			}
		})
	}
}

func TestOrkaDryRunNeverMintsTokenOrWritesResources(t *testing.T) {
	a, opt, _, diagnostics, dir := orkaCreateFixture(t, "")
	opt.DryRun = true
	opt.Task = "Say hello"
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	for _, c := range orkaCalls(t, dir) {
		if slices.Contains(c.Args, "create") && !slices.Contains(c.Args, "--dry-run=server") || slices.Contains(c.Args, "port-forward") {
			t.Fatalf("execution side effect: %v", c.Args)
		}
	}
	if !strings.Contains(diagnostics.String(), "result access and execution were not tested") {
		t.Fatal(diagnostics.String())
	}
}
