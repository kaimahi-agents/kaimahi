package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// orkaGoldenCreateOptions reproduces the exact inputs TestOrkaNoTaskGoldenBytes
// pinned before the port, expressed as the create flags a user actually
// supplies. Routing these through the adapter must still produce the pinned
// golden bytes.
func orkaGoldenCreateOptions() CreateOptions {
	return CreateOptions{
		Name:                    "support-bot",
		Namespace:               "orka-system",
		Description:             "Customer support triage agent",
		ProviderType:            "openai",
		Model:                   "gpt-4o-mini",
		Secret:                  "support-bot-key",
		SecretKey:               "api-key",
		InstructionText:         "You are support-bot. Answer briefly and in plain text, and say plainly when you do not know something.",
		Tools:                   "web-search,ticket-lookup",
		Skills:                  "triage,summarize",
		AgentRequestsPerMinute:  "60",
		ProviderTokensPerMinute: "100000",
		NoApply:                 true,
		Out:                     "-",
	}
}

// The adapter's render must be byte-identical to the pre-port bundle: the
// pinned golden file and its recorded SHA-256 are the contract.
func TestOrkaAdapterRenderMatchesPinnedGoldenBytes(t *testing.T) {
	a := &App{}
	opt := orkaGoldenCreateOptions()
	portable, err := a.portableCreateDocument(opt)
	if err != nil {
		t.Fatal(err)
	}
	adapter := orkaRuntimeAdapter{app: a, create: opt}
	rendered, provenance, err := adapter.renderOrkaBundle(*portable)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	document, err := bundle.YAML(provenance)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "orka-no-task.golden.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if document != string(golden) {
		t.Fatalf("adapter render drifted from the pinned golden fixture:\n--- got ---\n%s\n--- want ---\n%s", document, golden)
	}
	got := sha256.Sum256([]byte(document))
	const wantSHA256 = "29564d0eb4a653fded13934970e7aeebdb734e654642046cf8b4e5b9a63c4b43"
	if hex.EncodeToString(got[:]) != wantSHA256 {
		t.Fatalf("adapter render SHA-256 = %x, want %s", got, wantSHA256)
	}
}

// The RenderedBundle is the immutable identity Deploy consumes: it names its
// own adapter and target, digests the exact portable source separately from
// the rendered bytes, and records the separately provisioned Secret as a
// prerequisite rather than something creation writes.
func TestOrkaAdapterRenderedBundleCarriesIdentityAndPrerequisite(t *testing.T) {
	a := &App{}
	opt := orkaGoldenCreateOptions()
	portable, err := a.portableCreateDocument(opt)
	if err != nil {
		t.Fatal(err)
	}
	adapter := orkaRuntimeAdapter{app: a, create: opt}
	rendered, _, err := adapter.renderOrkaBundle(*portable)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Adapter() != agentruntime.Orka {
		t.Fatalf("adapter = %q", rendered.Adapter())
	}
	if target := rendered.Target(); target.Runtime != agentruntime.Orka || target.Namespace != "orka-system" {
		t.Fatalf("target = %+v", target)
	}
	// Independent expectations: both digests are framed here from DESIGN.md
	// §2's exact rule, not by calling the helpers the constructor uses.
	frame := func(path string, body []byte) string {
		return fmt.Sprintf("%s %d\n%s\n", path, len(body), body)
	}
	portableSum := sha256.Sum256([]byte(frame("portable-agent.yaml", portable.Source())))
	if want := hex.EncodeToString(portableSum[:]); rendered.PortableDigest() != want {
		t.Fatalf("portable digest = %q, want %q", rendered.PortableDigest(), want)
	}
	var renderedFrames []byte
	for i, document := range rendered.Documents() {
		renderedFrames = append(renderedFrames, frame(fmt.Sprintf("rendered/%03d.yaml", i), document)...)
	}
	renderedSum := sha256.Sum256(renderedFrames)
	if want := hex.EncodeToString(renderedSum[:]); rendered.RenderedDigest() != want {
		t.Fatalf("rendered digest = %q, want %q covering every artifact byte", rendered.RenderedDigest(), want)
	}
	if rendered.RenderedDigest() == rendered.PortableDigest() {
		t.Fatal("rendered and portable digests must never be the same value")
	}
	// The portable digest must identify this document, not a constant: an
	// absent source would hash to the same value for every agent.
	if rendered.PortableDigest() == agentruntime.PortableBundleDigest(nil) {
		t.Fatal("portable digest equals the digest of an absent source")
	}
	if losses := rendered.Losses(); len(losses) != 0 {
		t.Fatalf("losses = %+v", losses)
	}
	want := []agentruntime.Prerequisite{{Kind: "Secret", Namespace: "orka-system", Name: "support-bot-key"}}
	if got := rendered.Prerequisites(); !slices.Equal(got, want) {
		t.Fatalf("prerequisites = %+v, want %+v", got, want)
	}
}

// Two creates that differ in one authored input must differ in their portable
// digest, and two identical creates must agree: the digest identifies the
// document, not the code path that produced it.
func TestOrkaAdapterPortableDigestIdentifiesTheInputs(t *testing.T) {
	digest := func(t *testing.T, mutate func(*CreateOptions)) string {
		t.Helper()
		a := &App{}
		opt := orkaGoldenCreateOptions()
		mutate(&opt)
		portable, err := a.portableCreateDocument(opt)
		if err != nil {
			t.Fatal(err)
		}
		rendered, _, err := orkaRuntimeAdapter{app: a, create: opt}.renderOrkaBundle(*portable)
		if err != nil {
			t.Fatal(err)
		}
		return rendered.PortableDigest()
	}
	unchanged := func(*CreateOptions) {}
	base := digest(t, unchanged)
	if repeat := digest(t, unchanged); repeat != base {
		t.Fatalf("identical inputs produced different portable digests: %q vs %q", base, repeat)
	}
	for name, mutate := range map[string]func(*CreateOptions){
		"name":         func(o *CreateOptions) { o.Name = "other-bot" },
		"model":        func(o *CreateOptions) { o.Model = "gpt-4o" },
		"namespace":    func(o *CreateOptions) { o.Namespace = "agents" },
		"instructions": func(o *CreateOptions) { o.InstructionText = "Answer in one sentence." },
		"secret":       func(o *CreateOptions) { o.Secret = "other-key" },
	} {
		if changed := digest(t, mutate); changed == base {
			t.Errorf("changing %s did not change the portable digest", name)
		}
	}
}

// The artifact carries the full review order, but only Provider, Agent and
// the optional Task are deployable: the value-free Secret skeleton is a
// review-only document naming a prerequisite, and Deploy must never see it in
// its own list.
func TestOrkaAdapterSeparatesArtifactFromDeployDocuments(t *testing.T) {
	for _, task := range []string{"", "Summarize the latest release notes."} {
		a := &App{}
		opt := orkaGoldenCreateOptions()
		opt.Task = task
		portable, err := a.portableCreateDocument(opt)
		if err != nil {
			t.Fatal(err)
		}
		rendered, _, err := orkaRuntimeAdapter{app: a, create: opt}.renderOrkaBundle(*portable)
		if err != nil {
			t.Fatal(err)
		}
		artifact, deploy := rendered.Documents(), rendered.DeployDocuments()
		wantDocuments := 3
		if task != "" {
			wantDocuments = 4
		}
		if len(artifact) != wantDocuments {
			t.Fatalf("artifact documents = %d, want %d", len(artifact), wantDocuments)
		}
		if len(deploy) != wantDocuments-1 {
			t.Fatalf("deploy documents = %d, want %d", len(deploy), wantDocuments-1)
		}
		if !bytes.Contains(artifact[0], []byte("kind: Secret")) {
			t.Fatalf("artifact does not open with the Secret skeleton:\n%s", artifact[0])
		}
		for i, document := range deploy {
			if bytes.Equal(document, artifact[0]) || bytes.Contains(document, []byte("kind: Secret")) {
				t.Fatalf("deploy document %d is the Secret skeleton:\n%s", i, document)
			}
			if !bytes.Equal(document, artifact[i+1]) {
				t.Fatalf("deploy document %d is not artifact document %d verbatim", i, i+1)
			}
		}
	}
}

// Defense in depth: even a bundle that claims the value-free Secret skeleton
// is deployable is refused before anything reaches a cluster, so no future
// renderer can turn the artifact's review order into a bulk apply.
func TestOrkaDeployRefusesADeployableSecretSkeleton(t *testing.T) {
	a := &App{}
	opt := orkaGoldenCreateOptions()
	portable, err := a.portableCreateDocument(opt)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _, err := orkaRuntimeAdapter{app: a, create: opt}.renderOrkaBundle(*portable)
	if err != nil {
		t.Fatal(err)
	}
	var documents []agentruntime.Document
	for _, raw := range rendered.Documents() {
		documents = append(documents, agentruntime.ApplyDocument(raw))
	}
	tampered, err := agentruntime.NewRenderedBundle(agentruntime.Orka, portable.Source(), documents, rendered.Prerequisites(), rendered.Target(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orkaBundleFromRendered(tampered); err == nil {
		t.Fatal("a bundle marking the Secret skeleton for deployment was accepted")
	}
}

// Render through the neutral seam must be the same render: the compatibility
// helper exists only because the artifact header also records the schema
// provenance, which the neutral bundle deliberately does not model.
func TestOrkaAdapterSeamRenderEqualsCompatibilityRender(t *testing.T) {
	a := &App{}
	opt := orkaGoldenCreateOptions()
	portable, err := a.portableCreateDocument(opt)
	if err != nil {
		t.Fatal(err)
	}
	adapter := orkaRuntimeAdapter{app: a, create: opt}
	seam, err := adapter.Render(context.Background(), *portable, agentruntime.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	compatibility, _, err := adapter.renderOrkaBundle(*portable)
	if err != nil {
		t.Fatal(err)
	}
	if seam.RenderedDigest() != compatibility.RenderedDigest() || seam.PortableDigest() != compatibility.PortableDigest() {
		t.Fatalf("seam render %q/%q differs from compatibility render %q/%q",
			seam.PortableDigest(), seam.RenderedDigest(), compatibility.PortableDigest(), compatibility.RenderedDigest())
	}
}

// Orka declares exactly the lifecycle verbs Tasks 5-6 implement: Render and
// Deploy (Task 5) plus Status (Task 6). Evaluate is permanently unsupported,
// because a native Orka Task supplies no frozen target revision.
func TestOrkaAdapterDeclaresRenderDeployAndStatusOnly(t *testing.T) {
	adapter := orkaRuntimeAdapter{app: &App{}}
	caps := adapter.Capabilities()
	if !caps.Render || !caps.Deploy || !caps.Status {
		t.Fatalf("Orka lifecycle Capabilities = %+v, want Render, Deploy and Status supported", caps)
	}
	if caps.Evaluate {
		t.Fatalf("Orka lifecycle Capabilities = %+v, want Evaluate unsupported", caps)
	}
}

// A Task name carries fresh random identity. It must be minted once, by
// Render, and Deploy must apply exactly that name — never mint a second one.
func TestOrkaAdapterRenderMintsTaskIdentityOncePerRender(t *testing.T) {
	a := &App{}
	opt := orkaGoldenCreateOptions()
	opt.Task = "Say hello"
	portable, err := a.portableCreateDocument(opt)
	if err != nil {
		t.Fatal(err)
	}
	adapter := orkaRuntimeAdapter{app: a, create: opt}
	first, _, err := adapter.renderOrkaBundle(*portable)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := adapter.renderOrkaBundle(*portable)
	if err != nil {
		t.Fatal(err)
	}
	firstBundle, err := orkaBundleFromRendered(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBundle, err := orkaBundleFromRendered(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstBundle.Task == nil || secondBundle.Task == nil {
		t.Fatal("render dropped the requested Task")
	}
	if orkaObjectName(firstBundle.Task) == orkaObjectName(secondBundle.Task) {
		t.Fatal("two renders reused one Task identity")
	}
	if first.PortableDigest() != second.PortableDigest() {
		t.Fatal("a fresh Task identity changed the portable digest")
	}
	if first.RenderedDigest() == second.RenderedDigest() {
		t.Fatal("a fresh Task identity did not change the rendered digest")
	}
}

// Deploy consumes the immutable bundle: every applied object is byte-for-byte
// the rendered one, in Provider → Agent → Task order, and the value-free
// Secret skeleton is never written.
func TestOrkaAdapterDeployAppliesExactRenderedObjects(t *testing.T) {
	a, opt, out, _, dir := orkaCreateFixture(t, "")
	var requests atomic.Int32
	orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
		fmt.Fprint(w, `{"result":"hello from the deployed Task"}`)
	})
	if err := validateOrkaResultOptions(&opt); err != nil {
		t.Fatal(err)
	}
	portable, err := a.portableCreateDocument(opt)
	if err != nil {
		t.Fatal(err)
	}
	adapter := orkaRuntimeAdapter{app: a, create: opt}
	rendered, _, err := adapter.renderOrkaBundle(*portable)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	// The result session requires a bounded deadline, exactly as the command
	// path supplies one.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ref, err := adapter.Deploy(ctx, rendered, agentruntime.DeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{bundle.Provider, bundle.Agent, bundle.Task}
	var applied []map[string]any
	for _, call := range orkaCalls(t, dir) {
		if call.Document == nil || slices.Contains(call.Args, "--dry-run=server") {
			continue
		}
		if call.Document["kind"] == "Secret" {
			t.Fatal("deploy wrote the value-free Secret skeleton")
		}
		applied = append(applied, call.Document)
	}
	if len(applied) != len(want) {
		t.Fatalf("applied %d objects, want %d", len(applied), len(want))
	}
	for i, doc := range applied {
		if orkaJSON(t, doc) != orkaJSON(t, want[i]) {
			t.Fatalf("applied object %d is not the rendered object:\n--- applied ---\n%s\n--- rendered ---\n%s", i, orkaJSON(t, doc), orkaJSON(t, want[i]))
		}
	}
	if ref.Runtime != agentruntime.Orka || ref.Namespace != "orka-system" || ref.Name != "sample" || ref.Kind != "agents.core.orka.ai" || ref.UID != "agent-uid" {
		t.Fatalf("deploy returned %+v", ref)
	}
	if !strings.Contains(out.String(), "hello from the deployed Task") {
		t.Fatalf("Task answer was not printed: %q", out.String())
	}
}

func orkaJSON(t *testing.T, doc map[string]any) string {
	t.Helper()
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The emitted artifact is the exact rendered bytes plus the installed-schema
// provenance header: routing through the adapter must not change what the
// online path writes for review.
func TestOrkaAdapterDeployEmitsRenderedArtifact(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "")
	opt.Out = filepath.Join(dir, "artifact.yaml")
	if err := validateOrkaResultOptions(&opt); err != nil {
		t.Fatal(err)
	}
	portable, err := a.portableCreateDocument(opt)
	if err != nil {
		t.Fatal(err)
	}
	adapter := orkaRuntimeAdapter{app: a, create: opt}
	rendered, _, err := adapter.renderOrkaBundle(*portable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Deploy(t.Context(), rendered, agentruntime.DeployOptions{}); err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range rendered.Documents() {
		if !bytes.Contains(artifact, document) {
			t.Fatalf("artifact does not contain the exact rendered document:\n--- rendered ---\n%s\n--- artifact ---\n%s", document, artifact)
		}
	}
	if !bytes.Contains(artifact, []byte("installed Orka CRDs")) {
		t.Fatalf("artifact lost its installed-schema provenance:\n%s", artifact)
	}
}

// DESIGN.md §4: an omitted runtime uses shared platform detection. Every
// create that contacts a cluster asks the detector, including an Orka
// shorthand one, so a cluster with neither platform names both install
// prerequisites instead of assuming Orka.
func TestCreateAgentOmittedRuntimeDetectsPlatformOnline(t *testing.T) {
	for _, scenario := range []string{"", "no-platform"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, out, _, dir := orkaCreateFixture(t, scenario)
			err := a.CreateAgent(opt)
			if scenario == "no-platform" {
				var none *agentruntime.NoPlatformInstalledError
				if !errors.As(err, &none) {
					t.Fatalf("err = %v, not *NoPlatformInstalledError", err)
				}
				for _, call := range orkaCalls(t, dir) {
					if call.Document != nil {
						t.Fatalf("an undetected platform still sent a document: %v", call.Args)
					}
				}
				if out.Len() != 0 {
					t.Fatal("an undetected platform emitted bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			probed := false
			for _, call := range orkaCalls(t, dir) {
				if slices.Contains(call.Args, "api-resources") {
					probed = true
				}
			}
			if !probed {
				t.Fatal("an omitted runtime never probed for an installed platform")
			}
		})
	}
}

// Recorded ruling: an offline create contacts no cluster, so there is nothing
// to detect against. An omitted runtime keeps selecting Orka there, which
// preserves the pre-port guarantee that writing an artifact needs no kubectl.
func TestCreateAgentOfflineNeverDetectsPlatform(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "no-platform")
	opt.NoApply = true
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if len(orkaCalls(t, dir)) != 0 {
		t.Fatal("an offline create contacted the cluster")
	}
}

// An explicit runtime overrides detection and never probes.
func TestCreateAgentExplicitOrkaSkipsDetection(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "no-platform")
	opt.Runtime = string(agentruntime.Orka)
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	for _, call := range orkaCalls(t, dir) {
		if slices.Contains(call.Args, "api-resources") {
			t.Fatalf("an explicit runtime still probed: %v", call.Args)
		}
	}
}

// Render applies the structural schema that is authoritative for its mode:
// the pinned snapshot offline, and the cluster's installed CRDs — read inside
// the guarded staged deploy — online. Neither mode skips scaffold's own
// schema-independent structural invariant, and an online render contacts no
// cluster.
func TestOrkaAdapterRenderValidatesPerMode(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	render := func(opt CreateOptions) error {
		a := &App{}
		portable, err := a.portableCreateDocument(opt)
		if err != nil {
			return err
		}
		_, _, err = orkaRuntimeAdapter{app: a, create: opt}.renderOrkaBundle(*portable)
		return err
	}

	// The pinned main snapshot models no rate limits, so an offline render
	// against it refuses exactly what the default v0.1.3 snapshot accepts.
	offline := orkaGoldenCreateOptions()
	offline.SchemaTarget = "main"
	if err := render(offline); err == nil {
		t.Fatal("offline render skipped its pinned schema gate")
	}
	offline.SchemaTarget = ""
	if err := render(offline); err != nil {
		t.Fatalf("offline render refused the pinned default schema: %v", err)
	}

	// The same inputs render for a deploy without any schema read: the
	// installed CRDs decide there, and no offline fallback is substituted.
	online := orkaGoldenCreateOptions()
	online.NoApply, online.Out = false, ""
	online.SchemaTarget = ""
	if err := render(online); err != nil {
		t.Fatalf("online render failed: %v", err)
	}

	// Both modes still refuse a structurally invalid bundle before anything
	// is deployed or emitted.
	for _, mode := range []string{"offline", "online"} {
		invalid := orkaGoldenCreateOptions()
		invalid.BaseURL = "http://user:secret@model.example/v1?token=abc"
		if mode == "online" {
			invalid.NoApply, invalid.Out = false, ""
		}
		if err := render(invalid); err == nil {
			t.Fatalf("%s render accepted a structurally invalid bundle", mode)
		}
	}
}

// portableAgentFile writes a portable document equivalent to the golden
// fixture's inputs, minus the description the closed schema does not model.
func portableAgentFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "portable-agent.yaml")
	document := `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
    name: ` + name + `
spec:
    instructions: You are support-bot. Answer briefly and in plain text, and say plainly when you do not know something.
    model:
        name: gpt-4o-mini
extensions:
    orka:
        apiVersion: core.orka.ai/v1alpha1
        namespace: orka-system
        provider:
            type: openai
            defaultModel: gpt-4o-mini
            secretRef:
                name: support-bot-key
                key: api-key
            rateLimit:
                tokensPerMinute: 100000
        agent:
            tools:
                - name: web-search
                - name: ticket-lookup
            skills:
                - name: triage
                - name: summarize
            rateLimit:
                requestsPerMinute: 60
`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A portable document and the equivalent shorthand flags must render the same
// Orka bytes; only the description, which the closed portable schema does not
// model, is absent.
func TestCreateAgentFileRendersTheSameBytesAsEquivalentFlags(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var flagsOut, fileOut, diagnostics bytes.Buffer

	flagsApp := &App{Out: &flagsOut, Err: &diagnostics}
	flagsOpt := orkaGoldenCreateOptions()
	flagsOpt.Description = ""
	if err := flagsApp.CreateAgent(flagsOpt); err != nil {
		t.Fatal(err)
	}

	fileApp := &App{Out: &fileOut, Err: &diagnostics}
	if err := fileApp.CreateAgent(CreateOptions{Name: "support-bot", File: portableAgentFile(t, "support-bot"), Out: "-"}); err != nil {
		t.Fatal(err)
	}
	if flagsOut.String() != fileOut.String() {
		t.Fatalf("--file render differs from the equivalent flags render:\n--- file ---\n%s\n--- flags ---\n%s", fileOut.String(), flagsOut.String())
	}
	if flagsOut.Len() == 0 {
		t.Fatal("no artifact was emitted")
	}
}

// DESIGN.md §4: with --file, every portable-defined flag conflicts. Silently
// ignoring one would deploy something the document does not say.
func TestCreateAgentFileRefusesPortableDefinedFlags(t *testing.T) {
	file := portableAgentFile(t, "support-bot")
	for _, tc := range []struct {
		flag  string
		apply func(*CreateOptions)
	}{
		{"--namespace", func(o *CreateOptions) { o.Namespace = "orka-system" }},
		{"--description", func(o *CreateOptions) { o.Description = "Customer support triage agent" }},
		{"--provider-type", func(o *CreateOptions) { o.ProviderType = "openai" }},
		{"--model", func(o *CreateOptions) { o.Model = "gpt-4o-mini" }},
		{"--secret", func(o *CreateOptions) { o.Secret = "support-bot-key" }},
		{"--secret-key", func(o *CreateOptions) { o.SecretKey = "api-key" }},
		{"--base-url", func(o *CreateOptions) { o.BaseURL = "http://model.example/v1" }},
		{"--instructions", func(o *CreateOptions) { o.Instructions = "instructions.txt" }},
		{"--tools", func(o *CreateOptions) { o.Tools = "web-search" }},
		{"--skills", func(o *CreateOptions) { o.Skills = "triage" }},
		{"--agent-requests-per-minute", func(o *CreateOptions) { o.AgentRequestsPerMinute = "60" }},
		{"--agent-tokens-per-minute", func(o *CreateOptions) { o.AgentTokensPerMinute = "60" }},
		{"--provider-requests-per-minute", func(o *CreateOptions) { o.ProviderRequestsPerMinute = "60" }},
		{"--provider-tokens-per-minute", func(o *CreateOptions) { o.ProviderTokensPerMinute = "60" }},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			var out, diagnostics bytes.Buffer
			a := &App{Out: &out, Err: &diagnostics}
			opt := CreateOptions{Name: "support-bot", File: file, Out: "-"}
			tc.apply(&opt)
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatalf("--file silently accepted %s", tc.flag)
			}
			if !strings.Contains(err.Error(), tc.flag) {
				t.Fatalf("error does not name %s: %v", tc.flag, err)
			}
			if out.Len() != 0 {
				t.Fatal("conflicting flags emitted bytes")
			}
		})
	}
}

// Deployment and output flags stay legal with --file: they describe what to
// do with the document, not what it says.
func TestCreateAgentFileAcceptsDeploymentFlags(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	opt := CreateOptions{Name: "support-bot", File: portableAgentFile(t, "support-bot"), Out: "-", Task: "Say hello", SchemaTarget: "v0.1.3"}
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "kind: Task") {
		t.Fatalf("--task was not rendered with --file:\n%s", out.String())
	}
}

func TestCreateAgentFileRequiresMatchingNameArgument(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, name := range []string{"", "other-bot"} {
		var out, diagnostics bytes.Buffer
		a := &App{Out: &out, Err: &diagnostics}
		err := a.CreateAgent(CreateOptions{Name: name, File: portableAgentFile(t, "support-bot"), Out: "-"})
		if err == nil {
			t.Fatalf("accepted name %q for a document naming support-bot", name)
		}
		if out.Len() != 0 {
			t.Fatal("name mismatch emitted bytes")
		}
	}
}

// DESIGN.md §4: explicit legacy kagent returns the one shared typed
// unsupported-verb error, not an ad hoc message and not an Orka render.
func TestCreateAgentExplicitLegacyKagentIsUnsupportedRender(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	opt := orkaGoldenCreateOptions()
	opt.Runtime = string(agentruntime.Kagent)
	err := a.CreateAgent(opt)
	var unsupported *agentruntime.UnsupportedVerbError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, not *UnsupportedVerbError", err)
	}
	if unsupported.Runtime != agentruntime.Kagent || unsupported.Verb != agentruntime.VerbRender {
		t.Fatalf("unsupported = %+v", unsupported)
	}
	if err.Error() != "runtime kagent does not support render" {
		t.Fatalf("message = %q", err.Error())
	}
	if out.Len() != 0 {
		t.Fatal("an unsupported runtime emitted bytes")
	}
}

// An explicit runtime this build does not register resolves to exactly one
// typed error, never a silent fallback to Orka.
func TestCreateAgentUnknownExplicitRuntimeIsTyped(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, runtime := range []string{"kagent-v1", "bogus"} {
		t.Run(runtime, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			a := &App{Out: &out, Err: &diagnostics}
			opt := orkaGoldenCreateOptions()
			opt.Runtime = runtime
			if runtime == "kagent-v1" {
				opt = CreateOptions{Name: "support-bot", File: portableAgentFile(t, "support-bot"), Out: "-", Runtime: runtime}
			}
			err := a.CreateAgent(opt)
			var unknown *agentruntime.UnknownRuntimeError
			if !errors.As(err, &unknown) {
				t.Fatalf("err = %v, not *UnknownRuntimeError", err)
			}
			if string(unknown.Runtime) != runtime {
				t.Fatalf("unknown = %+v", unknown)
			}
			if out.Len() != 0 {
				t.Fatal("an unknown runtime emitted bytes")
			}
		})
	}
}

// DESIGN.md §4: explicit kagent-v1 requires --file and its kagent extension.
// That rule is enforced before anything else, so it does not depend on which
// adapters this build happens to register.
func TestCreateAgentExplicitKagentV1RequiresFile(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	opt := orkaGoldenCreateOptions()
	opt.Runtime = string(agentruntime.KagentV1)
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "--file") {
		t.Fatalf("kagent-v1 without --file: %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("refused selection emitted bytes")
	}
}

// A --file create resolves its runtime through the same shared detection and
// then renders the document's own Orka extension.
func TestCreateAgentFileUsesSharedPlatformDetection(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "")
	if err := a.CreateAgent(CreateOptions{Name: "sample", File: portableAgentFile(t, "sample"), Out: opt.Out}); err != nil {
		t.Fatal(err)
	}
	probed, created := false, 0
	for _, call := range orkaCalls(t, dir) {
		if slices.Contains(call.Args, "api-resources") {
			probed = true
		}
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			created++
		}
	}
	if !probed {
		t.Fatal("--file create never probed for an installed platform")
	}
	if created != 2 {
		t.Fatalf("created %d objects, want Provider and Agent", created)
	}
}

// A portable document with no Orka extension cannot be rendered by Orka: it
// fails closed rather than inventing provider, Secret or namespace values.
func TestCreateAgentFileWithoutOrkaExtensionFailsClosed(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(t.TempDir(), "portable-agent.yaml")
	document := `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
    name: support-bot
spec:
    instructions: Answer briefly.
    model:
        name: gpt-4o-mini
`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	err := a.CreateAgent(CreateOptions{Name: "support-bot", File: path, Out: "-"})
	if err == nil || !strings.Contains(err.Error(), "extensions.orka") {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("an unrenderable document emitted bytes")
	}
}
