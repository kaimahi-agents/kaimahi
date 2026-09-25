package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// lifecycleTestApp is an App with no cluster behind it. Every test below
// either renders (which contacts nothing) or asserts a refusal that happens
// before the first kubectl call, so a real cluster would add nothing.
func lifecycleTestApp(t *testing.T) *App {
	t.Helper()
	var out, errOut bytes.Buffer
	return &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: &out, Stderr: &errOut}, Out: &out, Err: &errOut,
	}
}

func lifecycleAdapter(t *testing.T, opt CreateOptions) orkaRuntimeAdapter {
	t.Helper()
	return orkaRuntimeAdapter{app: lifecycleTestApp(t), create: &opt}
}

// renderForTest renders the golden create's portable source through the
// adapter, which is the same path CreateAgent takes.
func renderForTest(t *testing.T, opt CreateOptions) (orkaRuntimeAdapter, agentruntime.RenderedBundle) {
	t.Helper()
	adapter := lifecycleAdapter(t, opt)
	source, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := adapter.Render(context.Background(), source, agentruntime.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, rendered
}

// TestOrkaAdapterSatisfiesLifecycleAdapter is the whole point of the seam:
// the Orka adapter is usable as a LifecycleAdapter, not merely shaped like
// one, and it reuses the chat Adapter's identity rather than a parallel one.
func TestOrkaAdapterSatisfiesLifecycleAdapter(t *testing.T) {
	var adapter agentruntime.LifecycleAdapter = orkaRuntimeAdapter{app: lifecycleTestApp(t)}
	if adapter.ID() != agentruntime.Orka {
		t.Fatalf("lifecycle adapter reports runtime %q", adapter.ID())
	}
}

// TestOrkaAdapterDeclaresOnlyImplementedVerbs pins the capability rule.
// Render and Deploy act on one create's own flags, so an adapter no create
// configured must not advertise them: it would otherwise render some other
// agent out of an empty CreateOptions. Status reads only the AgentRef it is
// handed, so it is always available. Evaluate is never supported.
func TestOrkaAdapterDeclaresOnlyImplementedVerbs(t *testing.T) {
	unconfigured := orkaRuntimeAdapter{app: lifecycleTestApp(t)}.Capabilities()
	if unconfigured.Render || unconfigured.Deploy {
		t.Fatalf("an adapter with no create advertised render/deploy: %+v", unconfigured)
	}
	if !unconfigured.Status {
		t.Fatal("status reads only its AgentRef and must always be available")
	}
	configured := lifecycleAdapter(t, goldenNoTaskCreate("")).Capabilities()
	if !configured.Render || !configured.Deploy || !configured.Status {
		t.Fatalf("a configured adapter declined an implemented verb: %+v", configured)
	}
	if unconfigured.Evaluate || configured.Evaluate {
		t.Fatal("Orka must never advertise evaluate")
	}
}

// TestOrkaAdapterRefusesUndeclaredVerbs proves the declaration is load
// bearing: an undeclared verb returns the one shared typed error rather than
// being attempted, and Evaluate returns it even when everything else works.
func TestOrkaAdapterRefusesUndeclaredVerbs(t *testing.T) {
	adapter := orkaRuntimeAdapter{app: lifecycleTestApp(t)}
	for _, tc := range []struct {
		verb string
		call func() error
	}{
		{agentruntime.VerbRender, func() error {
			_, err := adapter.Render(context.Background(), []byte("x"), agentruntime.RenderOptions{})
			return err
		}},
		{agentruntime.VerbDeploy, func() error {
			_, err := adapter.Deploy(context.Background(), agentruntime.RenderedBundle{}, agentruntime.DeployOptions{})
			return err
		}},
		{agentruntime.VerbEvaluate, func() error {
			_, err := adapter.Evaluate(context.Background(), agentruntime.AgentRef{}, agentruntime.EvaluationRequest{})
			return err
		}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			var unsupported *agentruntime.UnsupportedVerbError
			err := tc.call()
			if !errors.As(err, &unsupported) {
				t.Fatalf("%s returned %v, not the shared unsupported-verb error", tc.verb, err)
			}
			if unsupported.Runtime != agentruntime.Orka || unsupported.Verb != tc.verb {
				t.Fatalf("unsupported error names %s/%s", unsupported.Runtime, unsupported.Verb)
			}
		})
	}
	// A configured adapter still refuses evaluate: it is not a capability
	// this runtime has, so no amount of configuration supplies it.
	var unsupported *agentruntime.UnsupportedVerbError
	_, err := lifecycleAdapter(t, goldenNoTaskCreate("")).Evaluate(context.Background(), agentruntime.AgentRef{}, agentruntime.EvaluationRequest{})
	if !errors.As(err, &unsupported) || unsupported.Verb != agentruntime.VerbEvaluate {
		t.Fatalf("a configured adapter did not refuse evaluate: %v", err)
	}
}

// TestOrkaRenderProducesTheGoldenArtifactBytes is the port's central claim:
// the documents the adapter renders, assembled into an artifact, are the
// exact bytes the committed golden pins. Render is not allowed to change
// what creation emits.
func TestOrkaRenderProducesTheGoldenArtifactBytes(t *testing.T) {
	_, rendered := renderForTest(t, goldenNoTaskCreate(""))
	artifact, err := scaffold.OrkaArtifact(orkaGoldenProvenance(t), rendered.Documents())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "orka-no-task.golden.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if artifact != string(want) {
		t.Fatalf("rendered documents do not assemble into the golden artifact.\n--- got ---\n%s\n--- want ---\n%s", artifact, want)
	}
}

func orkaGoldenProvenance(t *testing.T) string {
	t.Helper()
	validator, err := orkaschema.Offline("v0.1.3")
	if err != nil {
		t.Fatal(err)
	}
	return validator.Provenance()
}

// TestOrkaRenderKeepsTheSecretSkeletonOutOfDeployment is the safety property
// the artifact/deploy split exists for. The value-free Secret skeleton is the
// artifact's first document and belongs to the rendered digest, but Deploy
// must never be handed it.
func TestOrkaRenderKeepsTheSecretSkeletonOutOfDeployment(t *testing.T) {
	_, rendered := renderForTest(t, goldenNoTaskCreate(""))
	artifact, deploy := rendered.Documents(), rendered.DeployDocuments()
	if len(artifact) != 3 || len(deploy) != 2 {
		t.Fatalf("a no-Task bundle rendered %d artifact and %d deploy documents", len(artifact), len(deploy))
	}
	var skeleton map[string]any
	if err := yaml.Unmarshal(artifact[0], &skeleton); err != nil {
		t.Fatal(err)
	}
	if skeleton["kind"] != "Secret" {
		t.Fatalf("the artifact's first document is %v, not the Secret skeleton", skeleton["kind"])
	}
	if _, stated := skeleton["data"]; stated {
		t.Fatal("the Secret skeleton carries data")
	}
	for _, doc := range deploy {
		if bytes.Equal(doc, artifact[0]) {
			t.Fatal("the Secret skeleton was marked for deployment")
		}
	}
	for i, kind := range []string{"Provider", "Agent"} {
		var doc map[string]any
		if err := yaml.Unmarshal(deploy[i], &doc); err != nil {
			t.Fatal(err)
		}
		if doc["kind"] != kind {
			t.Fatalf("deploy document %d is %v, not %s", i, doc["kind"], kind)
		}
	}
}

// TestOrkaRenderMintsTaskIdentityOnce is why Deploy consumes rendered bytes
// rather than re-rendering. A Task's name carries 16 random bytes, so a
// second generation would deploy a different object than the artifact the
// operator reviewed.
func TestOrkaRenderMintsTaskIdentityOnce(t *testing.T) {
	opt := goldenNoTaskCreate("")
	opt.Task = "what is two plus two"
	adapter, rendered := renderForTest(t, opt)
	first, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if first.Task == nil {
		t.Fatal("a create with --task rendered no Task")
	}
	// Decoding the same immutable bundle twice is the operation Deploy
	// performs; it must not move the identity.
	again, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if orkaObjectName(again.Task) != orkaObjectName(first.Task) {
		t.Fatalf("decoding a rendered bundle twice changed the Task name: %s then %s", orkaObjectName(first.Task), orkaObjectName(again.Task))
	}
	// A fresh render is a different agent instance and is expected to differ;
	// this states that the random identity is real, so the check above is not
	// vacuously comparing two constants.
	source, err := portableOrkaSource(opt)
	if err != nil {
		t.Fatal(err)
	}
	other, err := adapter.Render(context.Background(), source, agentruntime.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	otherBundle, err := orkaBundleFromRendered(other)
	if err != nil {
		t.Fatal(err)
	}
	if orkaObjectName(otherBundle.Task) == orkaObjectName(first.Task) {
		t.Fatal("two separate renders minted the same Task name; the identity is not random")
	}
}

// TestOrkaRenderUsesSpecModelAsTheOnlyModelSource pins the closed document's
// one statement of which model to use. The Orka extension deliberately does
// not restate it, so nothing but spec.model.name can reach defaultModel.
func TestOrkaRenderUsesSpecModelAsTheOnlyModelSource(t *testing.T) {
	opt := goldenNoTaskCreate("")
	opt.Model = "claude-sonnet-4"
	opt.ProviderType = "anthropic"
	opt.BaseURL = ""
	_, rendered := renderForTest(t, opt)
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	spec := bundle.Provider["spec"].(map[string]any)
	if spec["defaultModel"] != "claude-sonnet-4" {
		t.Fatalf("Provider.spec.defaultModel is %v, not spec.model.name", spec["defaultModel"])
	}
}

// TestOrkaRenderRefusesSourceItDidNotParse proves Render parses the exact
// portable source rather than trusting its own flags. A document the closed
// schema refuses cannot be rendered even though the flags beside it are fine.
func TestOrkaRenderRefusesSourceItDidNotParse(t *testing.T) {
	adapter := lifecycleAdapter(t, goldenNoTaskCreate(""))
	for _, tc := range []struct{ name, source string }{
		{"empty", ""},
		{"not the portable kind", "apiVersion: v1\nkind: ConfigMap\n"},
		{"unknown field", "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nsurprise: yes\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adapter.Render(context.Background(), []byte(tc.source), agentruntime.RenderOptions{}); err == nil {
				t.Fatal("Render accepted source the closed document refuses")
			}
		})
	}
}

// TestOrkaDeployRejectsBundlesItDidNotRender is the refusal set Deploy owes
// its caller. Each case is a different way applying the bundle would write
// something other than what was rendered and reviewed.
func TestOrkaDeployRejectsBundlesItDidNotRender(t *testing.T) {
	adapter := lifecycleAdapter(t, goldenNoTaskCreate(""))
	provider := []byte("apiVersion: core.orka.ai/v1alpha1\nkind: Provider\nmetadata:\n    name: sample\n    namespace: orka-system\n")
	agent := []byte("apiVersion: core.orka.ai/v1alpha1\nkind: Agent\nmetadata:\n    name: sample\n    namespace: orka-system\n")
	secret := []byte("apiVersion: v1\nkind: Secret\nmetadata:\n    name: model-key\n    namespace: orka-system\n")
	foreign, err := agentruntime.NewRenderedBundle(agentruntime.Kagent, []byte("source"),
		[]agentruntime.Document{agentruntime.ReviewDocument(secret), agentruntime.ApplyDocument(provider), agentruntime.ApplyDocument(agent)})
	if err != nil {
		t.Fatal(err)
	}
	noSkeleton, err := agentruntime.NewRenderedBundle(agentruntime.Orka, []byte("source"),
		[]agentruntime.Document{agentruntime.ApplyDocument(provider), agentruntime.ApplyDocument(agent)})
	if err != nil {
		t.Fatal(err)
	}
	twoSkeletons, err := agentruntime.NewRenderedBundle(agentruntime.Orka, []byte("source"),
		[]agentruntime.Document{agentruntime.ReviewDocument(secret), agentruntime.ReviewDocument(secret),
			agentruntime.ApplyDocument(provider), agentruntime.ApplyDocument(agent)})
	if err != nil {
		t.Fatal(err)
	}
	notASecret, err := agentruntime.NewRenderedBundle(agentruntime.Orka, []byte("source"),
		[]agentruntime.Document{agentruntime.ReviewDocument(agent), agentruntime.ApplyDocument(provider), agentruntime.ApplyDocument(agent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		bundle     agentruntime.RenderedBundle
	}{
		{"zero bundle", "not \"orka\"", agentruntime.RenderedBundle{}},
		{"another runtime's bundle", "not \"orka\"", foreign},
		{"no review-only Secret skeleton", "exactly one", noSkeleton},
		{"two review-only documents", "exactly one", twoSkeletons},
		{"review-only document is not a Secret", "Secret", notASecret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.Deploy(context.Background(), tc.bundle, agentruntime.DeployOptions{})
			if err == nil {
				t.Fatal("Deploy accepted a bundle it did not render")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal does not say why: %v", err)
			}
		})
	}
}

// TestOrkaDeployRequiresTheSkeletonToMatchTheProvider closes the last gap
// between the prerequisite Deploy proves and the Secret the rendered Provider
// actually reads: a skeleton naming a different object or namespace would
// have Deploy prove the wrong Secret exists and then write a Provider that
// cannot authenticate.
func TestOrkaDeployRequiresTheSkeletonToMatchTheProvider(t *testing.T) {
	adapter := lifecycleAdapter(t, goldenNoTaskCreate(""))
	_, rendered := renderForTest(t, goldenNoTaskCreate(""))
	deploy := rendered.DeployDocuments()
	for _, tc := range []struct{ name, skeleton, want string }{
		{"another Secret", "apiVersion: v1\nkind: Secret\nmetadata:\n    name: other-key\n    namespace: orka-system\n", "secretRef"},
		{"another namespace", "apiVersion: v1\nkind: Secret\nmetadata:\n    name: model-key\n    namespace: elsewhere\n", "namespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			documents := []agentruntime.Document{agentruntime.ReviewDocument([]byte(tc.skeleton))}
			for _, doc := range deploy {
				documents = append(documents, agentruntime.ApplyDocument(doc))
			}
			bundle, err := agentruntime.NewRenderedBundle(agentruntime.Orka, []byte("source"), documents)
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{})
			if err == nil {
				t.Fatal("Deploy accepted a skeleton the Provider does not reference")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal does not say why: %v", err)
			}
		})
	}
}

// TestOrkaStatusRequiresAnExplicitTarget states that Status reports on the
// AgentRef it is given and never guesses one. Orka watches namespaces
// explicitly, so a defaulted namespace would report "not found" about a
// namespace the caller never meant.
func TestOrkaStatusRequiresAnExplicitTarget(t *testing.T) {
	adapter := orkaRuntimeAdapter{app: lifecycleTestApp(t)}
	for _, ref := range []agentruntime.AgentRef{
		{Runtime: agentruntime.Orka, Name: "sample"},
		{Runtime: agentruntime.Orka, Namespace: "orka-system"},
		{Runtime: agentruntime.Orka, Namespace: "  ", Name: "  "},
	} {
		if _, err := adapter.Status(context.Background(), ref, agentruntime.StatusOptions{}); err == nil {
			t.Fatalf("status accepted an incomplete reference %+v", ref)
		}
	}
}

// The Status tests below are the only ones here that reach a cluster, so they
// get the fake kubectl `kmx agent show`'s tests use: it answers the preflight
// probe and the Agent read, and it records every invocation, which is how a
// test proves a call was NOT made.
const fakeStatusKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"version --client"*) printf 'Client Version: v1.31.0\n' ;;
  *"get agents.core.orka.ai"*) printf '%s' "$KMX_TEST_AGENT" ;;
esac
exit 0
`

const statusAgentJSON = `{"metadata":{"name":"concierge","namespace":"demo","uid":"11111111-1111-1111-1111-111111111111"},
"status":{"ready":true,"activeTasks":2,"lastUsed":"2026-09-24T18:00:00Z"}}`

func statusFixture(t *testing.T, agent string) (orkaRuntimeAdapter, string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeStatusKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(dir, "args")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", args)
	t.Setenv("KMX_TEST_AGENT", agent)
	// A unit test must never fetch a toolchain, and nothing here should try:
	// the fake is already on PATH.
	t.Setenv("KMX_TOOLCHAIN", "off")
	return orkaRuntimeAdapter{app: lifecycleTestApp(t)}, args
}

// kubectlCalls returns every command the fake was asked to run.
func kubectlCalls(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

func statusRef(t *testing.T) agentruntime.AgentRef {
	t.Helper()
	return agentruntime.AgentRef{Runtime: agentruntime.Orka, Context: "kind-test", Namespace: "demo",
		Kind: "agents.core.orka.ai", Name: "concierge", UID: "11111111-1111-1111-1111-111111111111"}
}

func statusFieldText(status agentruntime.Status) string {
	var parts []string
	for _, field := range status.Fields {
		parts = append(parts, field.Label+"="+field.Value)
	}
	return strings.Join(parts, " ")
}

// TestOrkaStatusReportsTheSameFieldsForEveryValidReference pins the output.
// Runtime, Context, Kind and UID are optional in a reference — a caller
// holding only a namespace and a name still gets a status — and stating them
// must not change a single reported field.
func TestOrkaStatusReportsTheSameFieldsForEveryValidReference(t *testing.T) {
	const want = "ready=yes active tasks=2 last used=2026-09-24T18:00:00Z"
	for _, tc := range []struct {
		name string
		ref  agentruntime.AgentRef
	}{
		{"fully qualified", statusRef(t)},
		{"namespace and name only", agentruntime.AgentRef{Namespace: "demo", Name: "concierge"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, _ := statusFixture(t, statusAgentJSON)
			status, err := adapter.Status(context.Background(), tc.ref, agentruntime.StatusOptions{})
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if got := statusFieldText(status); got != want {
				t.Fatalf("status reported %q, want %q", got, want)
			}
			if status.Agent != tc.ref {
				t.Fatalf("status reported reference %+v, not the one it was given %+v", status.Agent, tc.ref)
			}
		})
	}
}

// TestOrkaStatusStopsBeforeReadingOnACancelledContext is the cancellation
// claim, and the assertion that matters is the second one: a cancelled status
// must not preflight (which may FETCH kubectl) or read the cluster at all.
func TestOrkaStatusStopsBeforeReadingOnACancelledContext(t *testing.T) {
	adapter, args := statusFixture(t, statusAgentJSON)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := adapter.Status(ctx, statusRef(t), agentruntime.StatusOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled status returned %v, not the cancellation", err)
	}
	if calls := kubectlCalls(t, args); len(calls) != 0 {
		t.Fatalf("a cancelled status still ran %d command(s): %v", len(calls), calls)
	}
}

// TestStatusContextBindsOnlyTheCopyThatUsesIt states why the context is bound
// to a copy: the Runner is shared by every caller holding this App, so one
// entry point's cancellation must not become the deadline of a command
// somebody else started.
func TestStatusContextBindsOnlyTheCopyThatUsesIt(t *testing.T) {
	app := lifecycleTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scoped := app.withRunContext(ctx)
	if scoped.Run.Context != ctx {
		t.Fatal("the scoped copy does not carry the caller's context")
	}
	if app.Run.Context != nil {
		t.Fatal("binding a context rebound the Runner every other caller shares")
	}
	// Binding the context a Runner already carries is a no-op: the copy
	// exists to avoid sharing, not to be made for its own sake.
	if same := scoped.withRunContext(ctx); same != scoped {
		t.Fatal("rebinding the same context copied the App again")
	}
}

// TestOrkaStatusRefusesAForeignReference: a reference naming another runtime,
// another kube context or another resource kind is not one this adapter can
// report on. Answering anyway would describe a different object while looking
// like it had honoured the reference — so it is refused before any read.
func TestOrkaStatusRefusesAForeignReference(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		ref        agentruntime.AgentRef
	}{
		{"another runtime", "kagent", agentruntime.AgentRef{Runtime: agentruntime.Kagent, Namespace: "demo", Name: "concierge"}},
		{"another kind", "agents.kagent.dev", agentruntime.AgentRef{Namespace: "demo", Name: "concierge", Kind: "agents.kagent.dev"}},
		{"another context", "kind-other", agentruntime.AgentRef{Namespace: "demo", Name: "concierge", Context: "kind-other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, args := statusFixture(t, statusAgentJSON)
			_, err := adapter.Status(context.Background(), tc.ref, agentruntime.StatusOptions{})
			if err == nil {
				t.Fatalf("status accepted a foreign reference %+v", tc.ref)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not name what disagreed: %v", err)
			}
			if calls := kubectlCalls(t, args); len(calls) != 0 {
				t.Fatalf("a foreign reference still reached the cluster: %v", calls)
			}
		})
	}
}

// TestOrkaStatusRefusesAReusedAgentName is what the UID is for. Names are
// reused: an Agent deleted and recreated under the same name is a different
// workload, and reporting its state against the old reference would claim the
// original one recovered.
func TestOrkaStatusRefusesAReusedAgentName(t *testing.T) {
	t.Run("a different object answers", func(t *testing.T) {
		adapter, _ := statusFixture(t, statusAgentJSON)
		ref := statusRef(t)
		ref.UID = "22222222-2222-2222-2222-222222222222"
		_, err := adapter.Status(context.Background(), ref, agentruntime.StatusOptions{})
		if err == nil {
			t.Fatal("status reported on an Agent with a different UID")
		}
		for _, want := range []string{"11111111-1111-1111-1111-111111111111", ref.UID} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal does not name both identities: %v", err)
			}
		}
	})
	// A stated UID that cannot be checked is not a confirmed one: reporting
	// anyway would silently downgrade the caller's identity check.
	t.Run("the object states no UID", func(t *testing.T) {
		adapter, _ := statusFixture(t, `{"metadata":{"name":"concierge","namespace":"demo"},"status":{"ready":true}}`)
		_, err := adapter.Status(context.Background(), statusRef(t), agentruntime.StatusOptions{})
		if err == nil || !strings.Contains(err.Error(), "cannot be confirmed") {
			t.Fatalf("an unconfirmable UID was accepted: %v", err)
		}
	})
}
