package kmx

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDigestCanonicalRoundTrip(t *testing.T) {
	digest := agentSourceDigest([]byte("agent source"))
	const want = "b33be396932ae95ab5b5958886391fa94a69a676640516d442e0881a742fa323"
	if got := digest.String(); got != want {
		t.Fatalf("Digest = %q, want %q", got, want)
	}
	parsed, err := ParseDigest(want)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != digest {
		t.Fatalf("parsed digest = %q, want %q", parsed, digest)
	}
	if _, err := ParseDigest("sha256:ABC"); err == nil {
		t.Fatal("ParseDigest accepted a non-canonical digest")
	}
	if _, err := ParseDigest("0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("ParseDigest accepted the all-zero unspecified value")
	}
}

func TestDigestDomainsMatchPortableAndRenderedFraming(t *testing.T) {
	source := []byte("same bytes")
	revision := agentSourceDigest(source)
	rendered := framedDigest(digestEntry{path: "rendered/000.yaml", data: source})
	if revision == rendered {
		t.Fatal("portable and rendered digest domains collapsed")
	}
	const portableWant = "58ffa43055abb647e61f397356d89da693a0a259c7a1b6a55d91d38a02afd2d9"
	const renderedWant = "2f0e0cc314a5458f53507186b83394a4f38fac030654ff0fd8fe4b03991c0bb2"
	if revision.String() != portableWant || rendered.String() != renderedWant {
		t.Fatalf("framed digests = %s, %s; want %s, %s", revision, rendered, portableWant, renderedWant)
	}
}

func TestSourceRevisionAndBindingAreImmutable(t *testing.T) {
	sourceBytes := []byte("agent source")
	source, err := NewAgentSource(sourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := NewAgentRevision(source)
	if err != nil {
		t.Fatal(err)
	}
	sourceBytes[0] = 'X'
	copyOfSource := revision.Source()
	copyOfSource[0] = 'Y'
	if got := string(revision.Source()); got != "agent source" {
		t.Fatalf("revision source changed through caller mutation: %q", got)
	}
	if revision.Ref().Digest != agentSourceDigest([]byte("agent source")) {
		t.Fatal("revision digest was not computed from its source")
	}

	target := TargetRef{Platform: "example", ID: "target-1"}
	bindingBytes := []byte("binding")
	binding, err := NewTargetBinding(target, "example.dev/v1alpha1", "ExampleBinding", bindingBytes)
	if err != nil {
		t.Fatal(err)
	}
	bindingBytes[0] = 'X'
	copyOfBinding := binding.Bytes()
	copyOfBinding[0] = 'Y'
	if got := string(binding.Bytes()); got != "binding" {
		t.Fatalf("target binding changed through caller mutation: %q", got)
	}
}

func TestRuntimeBundleIsBoundToRuntimeTargetAndImmutable(t *testing.T) {
	source, _ := NewAgentSource([]byte("agent source"))
	revision, err := NewAgentRevision(source)
	if err != nil {
		t.Fatal(err)
	}
	target := TargetRef{Platform: "example", ID: "target-1"}
	binding, _ := NewTargetBinding(target, "example.dev/v1alpha1", "ExampleBinding", []byte("binding"))
	runtime := RuntimeRef{Runtime: "example", Installation: "install-1", Target: target}
	sourceRef, _ := NewRevisionDeploymentSource(revision)
	input, err := NewRuntimeBuildInput("lift-1", sourceRef, runtime, binding)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("native artifact")
	bundle, err := NewRuntimeBundle(input, []RuntimeDocument{ReviewDocument([]byte("review")), ApplyDocument(artifact)})
	if err != nil {
		t.Fatal(err)
	}
	artifact[0] = 'X'
	copyOfArtifact := bundle.Documents()[1].Bytes()
	copyOfArtifact[0] = 'Y'
	if got := string(bundle.Documents()[1].Bytes()); got != "native artifact" {
		t.Fatalf("runtime artifact changed through caller mutation: %q", got)
	}
	if got := bundle.DeployDocuments(); len(got) != 1 || string(got[0]) != "native artifact" {
		t.Fatalf("DeployDocuments = %#v, want only native artifact", got)
	}
	if bundle.Operation() != "lift-1" || bundle.Runtime() != runtime || !reflect.DeepEqual(bundle.Source(), sourceRef.Ref()) || bundle.BindingDigest() != binding.Digest() {
		t.Fatal("runtime bundle lost its build identity")
	}

	other := runtime
	other.Target.ID = "target-2"
	if _, err := NewRuntimeBuildInput("lift-1", sourceRef, other, binding); err == nil {
		t.Fatal("runtime build accepted a binding for another target")
	}
}

func TestRuntimeBundleCoversReviewDocumentsButDeploysOnlyApplyDocuments(t *testing.T) {
	source, _ := NewAgentSource([]byte("agent source"))
	revision, _ := NewAgentRevision(source)
	target := TargetRef{Platform: "example", ID: "target-1"}
	binding, _ := NewTargetBinding(target, "example.dev/v1alpha1", "ExampleBinding", []byte("binding"))
	runtime := RuntimeRef{Runtime: "example", Installation: "install-1", Target: target}
	sourceRef, _ := NewRevisionDeploymentSource(revision)
	input, _ := NewRuntimeBuildInput("lift-1", sourceRef, runtime, binding)

	first, err := NewRuntimeBundle(input, []RuntimeDocument{
		ReviewDocument([]byte("review-a")), ApplyDocument([]byte("apply")),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRuntimeBundle(input, []RuntimeDocument{
		ReviewDocument([]byte("review-b")), ApplyDocument([]byte("apply")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.RenderedDigest() == second.RenderedDigest() {
		t.Fatal("rendered digest ignored review-only document bytes")
	}
	if got := first.DeployDocuments(); len(got) != 1 || string(got[0]) != "apply" {
		t.Fatalf("DeployDocuments = %#v, want only apply", got)
	}
	if _, err := NewRuntimeBundle(input, []RuntimeDocument{ReviewDocument([]byte("review"))}); err == nil {
		t.Fatal("runtime bundle accepted only review documents")
	}
}

func TestReceiptsDeriveMutationSubjectsAndRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	target := TargetRef{Platform: "example", ID: "target-1"}
	infrastructure, err := NewInfrastructureReceipt("infra-1", "up-1", target, at)
	if err != nil {
		t.Fatal(err)
	}
	teardown, err := NewTeardownReceipt("down-1", "down-operation-1", infrastructure, MutationSucceeded, at)
	if err != nil {
		t.Fatal(err)
	}
	if teardown.Target != target {
		t.Fatal("teardown did not derive its target from infrastructure evidence")
	}

	source, _ := NewAgentSource([]byte("agent source"))
	revision, err := NewAgentRevision(source)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := NewTargetBinding(target, "example.dev/v1alpha1", "ExampleBinding", []byte("binding"))
	runtime := RuntimeRef{Runtime: "example", Installation: "install-1", Target: target}
	sourceRef, _ := NewRevisionDeploymentSource(revision)
	input, _ := NewRuntimeBuildInput("lift-1", sourceRef, runtime, binding)
	bundle, _ := NewRuntimeBundle(input, []RuntimeDocument{ApplyDocument([]byte("artifact"))})
	deployment, err := NewDeploymentReceipt("receipt-1", "deployment-1", bundle, at)
	if err != nil {
		t.Fatal(err)
	}
	retirement, err := NewRetirementReceipt("retire-1", "retire-operation-1", deployment, MutationSucceeded, at)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.Deployment != deployment.Deployment {
		t.Fatal("retirement did not derive its deployment from deployment evidence")
	}

	data, err := json.Marshal(deployment)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DeploymentReceipt
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, deployment) {
		t.Fatalf("receipt round trip = %#v, want %#v", decoded, deployment)
	}
	if err := json.Unmarshal([]byte(`{"id":"broken"}`), &decoded); err == nil {
		t.Fatal("invalid receipt JSON was accepted")
	}
	invalid := deployment
	invalid.Deployment.ID = DeploymentID(string([]byte{0xff}))
	if _, err := json.Marshal(invalid); err == nil {
		t.Fatal("receipt with invalid UTF-8 identity was accepted")
	}
}

func TestDurableRefsRejectInvalidUTF8AndRoundTrip(t *testing.T) {
	target := TargetRef{Platform: "example", ID: "target-1"}
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TargetRef
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != target {
		t.Fatalf("target round trip = %#v, want %#v", decoded, target)
	}
	invalid := TargetRef{Platform: "example", ID: TargetID(string([]byte{0xff}))}
	if _, err := json.Marshal(invalid); err == nil {
		t.Fatal("TargetRef accepted invalid UTF-8")
	}
	if err := json.Unmarshal([]byte{'{', '"', 'i', 'd', '"', ':', '"', 0xff, '"', '}'}, &decoded); err == nil {
		t.Fatal("TargetRef decoded invalid UTF-8 JSON")
	}
}

func TestUpResultRejectsContradictoryEvidence(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	target := TargetRef{Platform: "example", ID: "target-1"}
	runtime := RuntimeRef{Runtime: "runtime", Installation: "install-1", Target: target}
	infrastructure, _ := NewInfrastructureReceipt("infra-1", "up-1", target, at)
	runtimeReceipt, _ := NewRuntimeReceipt("runtime-1", "up-1", runtime, at)
	result := UpResult{
		Operation: "up-1", Target: target, Runtime: runtime,
		Infrastructure: &infrastructure, RuntimeReceipt: runtimeReceipt,
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	result.Runtime.Target.ID = "another-target"
	if err := result.Validate(); err == nil {
		t.Fatal("UpResult accepted contradictory target and runtime identities")
	}
}

func TestInProcessValuesRefuseAccidentalJSON(t *testing.T) {
	source, _ := NewAgentSource([]byte("agent source"))
	revision, err := NewAgentRevision(source)
	if err != nil {
		t.Fatal(err)
	}
	target := TargetRef{Platform: "example", ID: "target-1"}
	binding, _ := NewTargetBinding(target, "example.dev/v1alpha1", "ExampleBinding", []byte("binding"))
	runtime := RuntimeRef{Runtime: "example", Installation: "install-1", Target: target}
	sourceRef, _ := NewRevisionDeploymentSource(revision)
	input, _ := NewRuntimeBuildInput("lift-1", sourceRef, runtime, binding)
	bundle, _ := NewRuntimeBundle(input, []RuntimeDocument{ApplyDocument([]byte("artifact"))})
	for name, values := range map[string]struct {
		encode any
		decode any
	}{
		"source":            {source, new(AgentSource)},
		"revision":          {revision, new(AgentRevision)},
		"binding":           {binding, new(TargetBinding)},
		"deployment source": {sourceRef, new(DeploymentSource)},
		"lift request":      {LiftRequest{Source: sourceRef, Binding: binding}, new(LiftRequest)},
		"build input":       {input, new(RuntimeBuildInput)},
		"document":          {ApplyDocument([]byte("artifact")), new(RuntimeDocument)},
		"bundle":            {bundle, new(RuntimeBundle)},
	} {
		if _, err := json.Marshal(values.encode); err == nil {
			t.Fatalf("%s silently serialized", name)
		}
		if err := json.Unmarshal([]byte(`{}`), values.decode); err == nil {
			t.Fatalf("%s silently deserialized", name)
		}
	}
}

func TestOutcomeUnknownErrorPreservesCause(t *testing.T) {
	cause := errors.New("connection ended after submission")
	err, constructionErr := NewOutcomeUnknownError("lift", "lift-1", cause)
	if constructionErr != nil {
		t.Fatal(constructionErr)
	}
	if !errors.Is(err, cause) {
		t.Fatal("OutcomeUnknownError did not preserve its cause")
	}
	if err.OperationID() != "lift-1" {
		t.Fatalf("OperationID = %q, want lift-1", err.OperationID())
	}
	if _, constructionErr := NewOutcomeUnknownError("lift", "", cause); constructionErr == nil {
		t.Fatal("NewOutcomeUnknownError accepted an empty operation ID")
	}
	invalid := OperationID(string([]byte{0xff}))
	if invalid.Validate() == nil {
		t.Fatal("OperationID accepted invalid UTF-8")
	}
	if _, constructionErr := NewOutcomeUnknownError("lift", invalid, cause); constructionErr == nil {
		t.Fatal("NewOutcomeUnknownError accepted invalid UTF-8 operation ID")
	}
}

func TestTargetBindingRejectsInvalidMetadata(t *testing.T) {
	target := TargetRef{Platform: "example", ID: "target-1"}
	if _, err := NewTargetBinding(target, string([]byte{0xff}), "Binding", []byte("data")); err == nil {
		t.Fatal("NewTargetBinding accepted invalid UTF-8 API version")
	}
}

func TestDeploymentSourceRequiresExactlyOneInput(t *testing.T) {
	if err := (DeploymentSourceRef{}).Validate(); err == nil {
		t.Fatal("empty deployment source was accepted")
	}
	source, _ := NewAgentSource([]byte("agent source"))
	revision, _ := NewAgentRevision(source)
	revisionRef := revision.Ref()
	image := AgentSandboxImage{
		Image: OCIArtifactRef{
			Location:  "registry.example/agents/writer",
			Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MediaType: MediaTypeOCIImageManifest,
		},
		SuiteManifest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Agent:         "writer",
		Platform:      SandboxPlatform{OS: "linux", Architecture: "amd64"},
		BuildProfile:  "default",
		Composition:   "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Binding:       "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
	revisionSource, _ := NewRevisionDeploymentSourceRef(revisionRef)
	sandboxSource, _ := NewSandboxDeploymentSourceRef(image)
	if revisionSource == sandboxSource {
		t.Fatal("revision and sandbox deployment sources compare equal")
	}
	data, err := json.Marshal(sandboxSource)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DeploymentSourceRef
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != sandboxSource {
		t.Fatalf("sandbox source round trip = %#v, want %#v", decoded, sandboxSource)
	}
	invalidUnion := `{"kind":"revision","revision":{"digest":"` + revisionRef.Digest.String() + `"},"sandboxImage":{}}`
	if err := json.Unmarshal([]byte(invalidUnion), &decoded); err == nil {
		t.Fatal("deployment source JSON accepted both union arms")
	}
	if _, err := NewSandboxDeploymentSource(image); err != nil {
		t.Fatal(err)
	}
	image.Binding = ""
	if _, err := NewSandboxDeploymentSource(image); err == nil {
		t.Fatal("sandbox deployment source accepted no binding digest")
	}
}

func TestAgentSuiteArtifactAndSandboxImageValidation(t *testing.T) {
	suite := AgentSuiteArtifact{
		Manifest: OCIArtifactRef{
			Location:     "/tmp/team-suite-layout",
			Digest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MediaType:    MediaTypeOCIImageManifest,
			ArtifactType: ArtifactTypeAgentSuite,
		},
	}
	if err := suite.Validate(); err != nil {
		t.Fatal(err)
	}
	report := SuiteReport{
		Name: "team", Agents: 2, Compositions: 1,
		CompositionSelections: []SuiteCompositionSelection{{
			Agent: "writer", Platform: SandboxPlatform{OS: "linux", Architecture: "amd64"},
			BuildProfile: "default",
			Digest:       "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		}},
	}
	selection, err := report.Composition("writer", SandboxPlatform{OS: "linux", Architecture: "amd64"})
	if err != nil || selection.BuildProfile != "default" {
		t.Fatalf("Composition selection = %+v, %v", selection, err)
	}
	request := BuildSandboxRequest{
		Operation: "sandbox-1", Suite: suite, Agent: "writer",
		Platform:     SandboxPlatform{OS: "linux", Architecture: "amd64"},
		BuildProfile: selection.BuildProfile,
		Composition:  selection.Digest,
		Output:       "/tmp/writer-image-layout",
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	result := PackageSuiteResult{Artifact: suite, Report: report}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decodedResult PackageSuiteResult
	if err := json.Unmarshal(data, &decodedResult); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedResult, result) {
		t.Fatalf("package result round trip = %#v, want %#v", decodedResult, result)
	}
	bad := suite
	bad.Manifest.Digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := bad.Validate(); err == nil {
		t.Fatal("AgentSuite artifact accepted a non-OCI digest")
	}
	request.Platform.Architecture = "riscv64"
	if err := request.Validate(); err == nil {
		t.Fatal("sandbox build accepted a platform outside the AgentSuite draft")
	}
}

func TestSuiteReportRejectsDuplicateCompositionSelection(t *testing.T) {
	selection := SuiteCompositionSelection{
		Agent: "writer", Platform: SandboxPlatform{OS: "linux", Architecture: "amd64"},
		BuildProfile: "default",
		Digest:       "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	report := SuiteReport{
		Name: "team", Agents: 1, Compositions: 2,
		CompositionSelections: []SuiteCompositionSelection{selection, selection},
	}
	if err := report.Validate(); err == nil {
		t.Fatal("SuiteReport accepted duplicate agent/platform selections")
	}
}

func TestUpProgressPreservesPartialInfrastructureEvidence(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	target := TargetRef{Platform: "example", ID: "target-1"}
	infrastructure, _ := NewInfrastructureReceipt("infra-1", "up-1", target, at)
	progress := UpProgress{Operation: "up-1", Target: target, Infrastructure: &infrastructure}
	if err := progress.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	var decoded UpProgress
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, progress) {
		t.Fatalf("progress round trip = %#v, want %#v", decoded, progress)
	}
	progress.Complete = true
	if err := progress.Validate(); err == nil {
		t.Fatal("UpProgress accepted complete setup without a runtime receipt")
	}
}

type managedPlatform struct{}

func (managedPlatform) Describe() PlatformDescriptor { return PlatformDescriptor{ID: "managed"} }
func (managedPlatform) Resolve(context.Context, TargetSpec) (TargetRef, error) {
	return TargetRef{}, nil
}
func (managedPlatform) Provision(context.Context, ProvisionRequest) (InfrastructureReceipt, error) {
	return InfrastructureReceipt{}, nil
}
func (managedPlatform) Inspect(context.Context, TargetRef) (TargetObservation, error) {
	return TargetObservation{}, nil
}
func (managedPlatform) Deprovision(context.Context, DeprovisionRequest) (TeardownReceipt, error) {
	return TeardownReceipt{}, nil
}

var (
	_ Platform              = managedPlatform{}
	_ PlatformResolver      = managedPlatform{}
	_ PlatformProvisioner   = managedPlatform{}
	_ PlatformInspector     = managedPlatform{}
	_ PlatformDeprovisioner = managedPlatform{}
)

type createOnlyRuntime struct{}

func (createOnlyRuntime) Describe() RuntimeDescriptor { return RuntimeDescriptor{ID: "create-only"} }
func (createOnlyRuntime) Build(context.Context, RuntimeBuildInput) (RuntimeBundle, error) {
	return RuntimeBundle{}, nil
}
func (createOnlyRuntime) Deploy(context.Context, RuntimeBundle, DeployOptions) (DeploymentReceipt, error) {
	return DeploymentReceipt{}, nil
}

var (
	_ Runtime         = createOnlyRuntime{}
	_ RuntimeBuilder  = createOnlyRuntime{}
	_ RuntimeDeployer = createOnlyRuntime{}
)

type suiteBuilder struct{}

func (suiteBuilder) Package(context.Context, PackageSuiteRequest) (PackageSuiteResult, error) {
	return PackageSuiteResult{}, nil
}
func (suiteBuilder) BuildSandbox(context.Context, BuildSandboxRequest) (AgentSandboxImage, error) {
	return AgentSandboxImage{}, nil
}

var _ AgentSuiteBuilder = suiteBuilder{}
