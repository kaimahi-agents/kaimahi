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
	digest := NewDigest([]byte("agent source"))
	const want = "sha256:c549b21352c7de3ca9ef374f63b058da30aa6716a2fae7951aff89f171079fa6"
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
	if _, err := ParseDigest("sha256:0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("ParseDigest accepted the all-zero unspecified value")
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
	if revision.Ref().Digest != NewDigest([]byte("agent source")) {
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
	input, err := NewRuntimeBuildInput(revision, runtime, binding)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("native artifact")
	bundle, err := NewRuntimeBundle(input, artifact)
	if err != nil {
		t.Fatal(err)
	}
	artifact[0] = 'X'
	copyOfArtifact := bundle.Artifact()
	copyOfArtifact[0] = 'Y'
	if got := string(bundle.Artifact()); got != "native artifact" {
		t.Fatalf("runtime artifact changed through caller mutation: %q", got)
	}
	if bundle.Runtime() != runtime || bundle.Revision() != revision.Ref() || bundle.BindingDigest() != binding.Digest() {
		t.Fatal("runtime bundle lost its build identity")
	}

	other := runtime
	other.Target.ID = "target-2"
	if _, err := NewRuntimeBuildInput(revision, other, binding); err == nil {
		t.Fatal("runtime build accepted a binding for another target")
	}
}

func TestReceiptsDeriveMutationSubjectsAndRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	target := TargetRef{Platform: "example", ID: "target-1"}
	infrastructure, err := NewInfrastructureReceipt("infra-1", target, []byte("target spec"), at)
	if err != nil {
		t.Fatal(err)
	}
	teardown, err := NewTeardownReceipt("down-1", infrastructure, MutationSucceeded, at)
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
	input, _ := NewRuntimeBuildInput(revision, runtime, binding)
	bundle, _ := NewRuntimeBundle(input, []byte("artifact"))
	deployment, err := NewDeploymentReceipt("receipt-1", "deployment-1", bundle, at)
	if err != nil {
		t.Fatal(err)
	}
	retirement, err := NewRetirementReceipt("retire-1", deployment, MutationSucceeded, at)
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
	input, _ := NewRuntimeBuildInput(revision, runtime, binding)
	bundle, _ := NewRuntimeBundle(input, []byte("artifact"))
	for name, values := range map[string]struct {
		encode any
		decode any
	}{
		"source":       {source, new(AgentSource)},
		"revision":     {revision, new(AgentRevision)},
		"binding":      {binding, new(TargetBinding)},
		"lift request": {LiftRequest{Revision: revision, Binding: binding}, new(LiftRequest)},
		"build input":  {input, new(RuntimeBuildInput)},
		"bundle":       {bundle, new(RuntimeBundle)},
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
	err := &OutcomeUnknownError{Operation: "lift", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("OutcomeUnknownError did not preserve its cause")
	}
}

type managedPlatform struct{}

func (managedPlatform) Describe() PlatformDescriptor { return PlatformDescriptor{ID: "managed"} }
func (managedPlatform) Resolve(context.Context, TargetSpec) (TargetRef, error) {
	return TargetRef{}, nil
}
func (managedPlatform) Provision(context.Context, TargetSpec) (InfrastructureReceipt, error) {
	return InfrastructureReceipt{}, nil
}
func (managedPlatform) Inspect(context.Context, TargetRef) (TargetObservation, error) {
	return TargetObservation{}, nil
}
func (managedPlatform) Deprovision(context.Context, InfrastructureReceipt) (TeardownReceipt, error) {
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
