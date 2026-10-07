package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

func revisionSource(t *testing.T) kmx.DeploymentSource {
	t.Helper()
	source, err := kmx.NewAgentSource([]byte("agent source"))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := kmx.NewAgentRevision(source)
	if err != nil {
		t.Fatal(err)
	}
	result, err := kmx.NewRevisionDeploymentSource(revision)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func runtimeInput(t *testing.T) RuntimeBuildInput {
	t.Helper()
	target := kmx.TargetRef{Platform: "example", ID: "target-1"}
	binding, err := kmx.NewTargetBinding(target, "example.dev/v1alpha1", "ExampleBinding", []byte("binding"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := kmx.RuntimeRef{Runtime: "example", Installation: "install-1", Target: target}
	input, err := NewRuntimeBuildInput("lift-1", revisionSource(t), runtime, binding)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestRuntimeBundleIsBoundAndImmutable(t *testing.T) {
	input := runtimeInput(t)
	artifact := []byte("native artifact")
	bundle, err := NewRuntimeBundle(input, []RuntimeDocument{
		ReviewDocument([]byte("review")), ApplyDocument(artifact),
	})
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
	if bundle.Operation() != input.Operation() || bundle.Runtime() != input.Runtime() ||
		!reflect.DeepEqual(bundle.Source(), input.Source().Ref()) || bundle.BindingDigest() != input.Binding().Digest() {
		t.Fatal("runtime bundle lost its build identity")
	}
}

func TestRuntimeBundleHashesReviewDocumentsAndRequiresApplyDocument(t *testing.T) {
	input := runtimeInput(t)
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
	if _, err := NewRuntimeBundle(input, []RuntimeDocument{ReviewDocument([]byte("review"))}); err == nil {
		t.Fatal("runtime bundle accepted only review documents")
	}
}

func TestDeployDigestIncludesDocumentDisposition(t *testing.T) {
	input := runtimeInput(t)
	review, err := NewRuntimeBundle(input, []RuntimeDocument{
		ReviewDocument([]byte("same")), ApplyDocument([]byte("apply")),
	})
	if err != nil {
		t.Fatal(err)
	}
	apply, err := NewRuntimeBundle(input, []RuntimeDocument{
		ApplyDocument([]byte("same")), ApplyDocument([]byte("apply")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.RenderedDigest() != apply.RenderedDigest() {
		t.Fatal("shipped rendered identity changed when only disposition changed")
	}
	if review.DeployDigest() == apply.DeployDigest() {
		t.Fatal("deploy identity ignored review/apply disposition")
	}
}

func TestRuntimeBundleRenderedDigestMatchesShippedFraming(t *testing.T) {
	bundle, err := NewRuntimeBundle(runtimeInput(t), []RuntimeDocument{
		ReviewDocument([]byte("review")),
		ApplyDocument([]byte("apply")),
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "ab73d97cdf085ad6cb286dbe514c4532b7c2100a0eed8e6700abf315e3c607c8"
	if got := bundle.RenderedDigest().String(); got != want {
		t.Fatalf("RenderedDigest = %q, want %q", got, want)
	}
}

func TestRuntimeBuildInputRejectsDifferentTarget(t *testing.T) {
	input := runtimeInput(t)
	runtime := input.Runtime()
	runtime.Target.ID = "another-target"
	if _, err := NewRuntimeBuildInput(input.Operation(), input.Source(), runtime, input.Binding()); err == nil {
		t.Fatal("runtime build accepted a binding for another target")
	}
}

func TestRuntimeIntermediatesRefuseJSON(t *testing.T) {
	input := runtimeInput(t)
	bundle, err := NewRuntimeBundle(input, []RuntimeDocument{ApplyDocument([]byte("apply"))})
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string]struct {
		encode any
		decode any
	}{
		"build input": {input, new(RuntimeBuildInput)},
		"document":    {ApplyDocument([]byte("apply")), new(RuntimeDocument)},
		"bundle":      {bundle, new(RuntimeBundle)},
	} {
		if _, err := json.Marshal(values.encode); err == nil {
			t.Fatalf("%s silently serialized", name)
		}
		if err := json.Unmarshal([]byte(`{}`), values.decode); err == nil {
			t.Fatalf("%s silently deserialized", name)
		}
	}
}

func TestReceiptFactoriesDeriveMutationSubjects(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	target := kmx.TargetRef{Platform: "example", ID: "target-1"}
	infrastructure, err := NewInfrastructureReceipt("infra-1", "up-1", target, at)
	if err != nil {
		t.Fatal(err)
	}
	teardown, err := NewTeardownReceipt("down-1", "down-operation-1", infrastructure, at)
	if err != nil {
		t.Fatal(err)
	}
	if teardown.Target != target {
		t.Fatal("teardown did not derive its target from infrastructure evidence")
	}
	bundle, err := NewRuntimeBundle(runtimeInput(t), []RuntimeDocument{ApplyDocument([]byte("artifact"))})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := NewDeploymentReceipt("receipt-1", "deployment-1", bundle, at)
	if err != nil {
		t.Fatal(err)
	}
	retirement, err := NewRetirementReceipt("retire-1", "retire-operation-1", deployment, at)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.Deployment != deployment.Deployment {
		t.Fatal("retirement did not derive its deployment from deployment evidence")
	}
}

type managedPlatform struct{}

func (managedPlatform) Describe() PlatformDescriptor { return PlatformDescriptor{ID: "managed"} }
func (managedPlatform) Resolve(context.Context, kmx.TargetSpec) (TargetResolution, error) {
	return TargetResolution{}, nil
}
func (managedPlatform) Provision(context.Context, ProvisionRequest) (kmx.InfrastructureReceipt, error) {
	return kmx.InfrastructureReceipt{}, nil
}
func (managedPlatform) Inspect(context.Context, kmx.TargetRef) (TargetObservation, error) {
	return TargetObservation{}, nil
}
func (managedPlatform) Deprovision(context.Context, DeprovisionRequest) (kmx.TeardownReceipt, error) {
	return kmx.TeardownReceipt{}, nil
}

var (
	_ Platform              = managedPlatform{}
	_ PlatformResolver      = managedPlatform{}
	_ PlatformProvisioner   = managedPlatform{}
	_ PlatformInspector     = managedPlatform{}
	_ PlatformDeprovisioner = managedPlatform{}
)

type resolvingPlatform struct {
	resolution TargetResolution
	err        error
	receipt    *kmx.InfrastructureReceipt
	provisions int
}

func (p *resolvingPlatform) Resolve(context.Context, kmx.TargetSpec) (TargetResolution, error) {
	return p.resolution, p.err
}

func (p *resolvingPlatform) Provision(_ context.Context, request ProvisionRequest) (kmx.InfrastructureReceipt, error) {
	p.provisions++
	if p.receipt != nil {
		return *p.receipt, nil
	}
	target := kmx.TargetRef{Platform: request.Target.Platform, ID: "target-1"}
	return NewInfrastructureReceipt("infra-1", request.Operation, target, timeForTest())
}

func TestResolveTargetRejectsMismatchedImplementationEvidence(t *testing.T) {
	request := kmx.UpRequest{
		Operation: "up-1", Mode: kmx.EnvironmentResolve,
		Target: kmx.TargetSpec{Name: "local", Platform: "example"}, Runtime: "runtime",
	}
	platform := &resolvingPlatform{resolution: TargetResolution{
		Found: true, Target: kmx.TargetRef{Platform: "another", ID: "target-1"},
	}}
	if _, _, err := ResolveTarget(t.Context(), request, platform, platform); err == nil {
		t.Fatal("resolver output for another platform was accepted")
	}
	request.Mode = kmx.EnvironmentProvision
	bad := kmx.InfrastructureReceipt{}
	platform.receipt = &bad
	if _, _, err := ResolveTarget(t.Context(), request, platform, platform); err == nil {
		t.Fatal("invalid provision receipt was accepted")
	}
	bad = kmx.InfrastructureReceipt{
		ID: "infra-1", Operation: "another-operation",
		Target: kmx.TargetRef{Platform: "example", ID: "target-1"}, RecordedAt: timeForTest(),
	}
	platform.receipt = &bad
	if _, _, err := ResolveTarget(t.Context(), request, platform, platform); err == nil {
		t.Fatal("provision receipt for another operation was accepted")
	}
	bad.Operation = request.Operation
	bad.Target.Platform = "another"
	platform.receipt = &bad
	if _, _, err := ResolveTarget(t.Context(), request, platform, platform); err == nil {
		t.Fatal("provision receipt for another platform was accepted")
	}
}

func TestResolveOrProvisionFallsBackOnlyOnEstablishedAbsence(t *testing.T) {
	request := kmx.UpRequest{
		Operation: "up-1", Mode: kmx.EnvironmentResolveOrProvision,
		Target: kmx.TargetSpec{Name: "local", Platform: "example"}, Runtime: "runtime",
	}
	unreadable := errors.New("target read denied")
	platform := &resolvingPlatform{err: unreadable}
	if _, _, err := ResolveTarget(t.Context(), request, platform, platform); !errors.Is(err, unreadable) {
		t.Fatalf("resolver error = %v, want unchanged denial", err)
	}
	if platform.provisions != 0 {
		t.Fatal("resolver error triggered provisioning")
	}
	platform.err = nil
	resolution, receipt, err := ResolveTarget(t.Context(), request, platform, platform)
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Found || receipt == nil || platform.provisions != 1 {
		t.Fatalf("absence result = %+v, receipt=%+v, provisions=%d", resolution, receipt, platform.provisions)
	}
}

func timeForTest() time.Time {
	return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
}

type createOnlyRuntime struct{}

func (createOnlyRuntime) Describe() RuntimeDescriptor { return RuntimeDescriptor{ID: "create-only"} }
func (createOnlyRuntime) Build(context.Context, RuntimeBuildInput) (RuntimeBundle, error) {
	return RuntimeBundle{}, nil
}
func (createOnlyRuntime) Deploy(context.Context, RuntimeBundle, DeployOptions) (kmx.DeploymentReceipt, error) {
	return kmx.DeploymentReceipt{}, nil
}

var (
	_ Runtime         = createOnlyRuntime{}
	_ RuntimeBuilder  = createOnlyRuntime{}
	_ RuntimeDeployer = createOnlyRuntime{}
)

type suiteBuilder struct{}

func (suiteBuilder) Package(context.Context, kmx.PackageSuiteRequest) (kmx.PackageSuiteResult, error) {
	return kmx.PackageSuiteResult{}, nil
}
func (suiteBuilder) BuildSandbox(context.Context, kmx.BuildSandboxRequest) (kmx.AgentSandboxImage, error) {
	return kmx.AgentSandboxImage{}, nil
}

var _ AgentSuiteBuilder = suiteBuilder{}
