package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// registryFakeAdapter is a minimal Adapter used only to exercise Registry's
// own bookkeeping (ID validation, lookup, ordered resolution). It knows
// nothing about Orka, kagent or Kubernetes, matching the neutrality
// Registry itself must have.
type registryFakeAdapter struct {
	id    ID
	found bool
	err   error
	calls *int
}

func (a registryFakeAdapter) ID() ID { return a.id }
func (a registryFakeAdapter) Probe(ctx context.Context, target Target) (Probe, error) {
	if a.calls != nil {
		*a.calls++
	}
	if a.err != nil {
		return Probe{}, a.err
	}
	return Probe{Found: a.found, Agent: AgentRef{Runtime: a.id, Name: target.Name}}, nil
}
func (a registryFakeAdapter) Open(context.Context, Target) (Session, error) {
	return nil, errors.New("not used")
}

func TestRegistryRejectsNilAdapter(t *testing.T) {
	if _, err := NewRegistry(nil); err == nil {
		t.Fatal("a nil adapter was accepted")
	}
}

func TestRegistryRejectsEmptyID(t *testing.T) {
	if _, err := NewRegistry(registryFakeAdapter{id: ""}); err == nil {
		t.Fatal("an adapter with an empty ID was accepted")
	}
}

func TestRegistryRejectsDuplicateIDs(t *testing.T) {
	if _, err := NewRegistry(registryFakeAdapter{id: Orka}, registryFakeAdapter{id: Orka}); err == nil {
		t.Fatal("a duplicate adapter ID was accepted")
	}
}

func TestRegistryLookupReturnsRegisteredAdapter(t *testing.T) {
	r, err := NewRegistry(registryFakeAdapter{id: Orka}, registryFakeAdapter{id: Kagent})
	if err != nil {
		t.Fatal(err)
	}
	found, err := r.Lookup(Kagent)
	if err != nil || found.ID() != Kagent {
		t.Fatalf("Lookup(%q) = %v, %v", Kagent, found, err)
	}
}

// Explicit lookup is typed: a caller recovers *UnknownRuntimeError with
// errors.As rather than matching an error string.
func TestRegistryLookupReturnsTypedErrorForUnknownID(t *testing.T) {
	r, err := NewRegistry(registryFakeAdapter{id: Orka})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Lookup(KagentV1)
	var unknown *UnknownRuntimeError
	if !errors.As(err, &unknown) {
		t.Fatalf("Lookup error = %v, not *UnknownRuntimeError", err)
	}
	if unknown.Runtime != KagentV1 {
		t.Errorf("unknown.Runtime = %q, want %q", unknown.Runtime, KagentV1)
	}
}

func TestRegistryLookupOnNilRegistryIsTypedNotAPanic(t *testing.T) {
	var r *Registry
	_, err := r.Lookup(Orka)
	var unknown *UnknownRuntimeError
	if !errors.As(err, &unknown) {
		t.Fatalf("Lookup on nil registry = %v, not *UnknownRuntimeError", err)
	}
}

// A Probe error is never treated as absence: it must stop resolution
// immediately, preserving PR #197's read-error/no-fallback semantics.
func TestRegistryResolveNeverFallsBackOnProbeError(t *testing.T) {
	var firstCalls, secondCalls int
	r, err := NewRegistry(
		registryFakeAdapter{id: "first", err: errors.New("access denied"), calls: &firstCalls},
		registryFakeAdapter{id: "second", found: true, calls: &secondCalls},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), Target{Name: "same-name"}); err == nil {
		t.Fatal("a probe read failure fell through to the next adapter")
	}
	if firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("firstCalls=%d secondCalls=%d", firstCalls, secondCalls)
	}
}

func TestRegistryResolveReturnsFirstFoundInOrder(t *testing.T) {
	r, err := NewRegistry(
		registryFakeAdapter{id: "first"},
		registryFakeAdapter{id: "second", found: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.Resolve(context.Background(), Target{Name: "same-name"})
	if err != nil || ref.Runtime != "second" {
		t.Fatalf("Resolve() = %v, %v", ref, err)
	}
}

func TestRegistryResolveReportsAbsenceWhenNoAdapterFoundIt(t *testing.T) {
	r, err := NewRegistry(registryFakeAdapter{id: "first"}, registryFakeAdapter{id: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), Target{Namespace: "ns", Name: "missing"}); err == nil {
		t.Fatal("Resolve succeeded although no adapter reported Found")
	}
}

func TestRegistryResolveOnNilRegistryReportsAbsenceNotAPanic(t *testing.T) {
	var r *Registry
	if _, err := r.Resolve(context.Background(), Target{Name: "x"}); err == nil {
		t.Fatal("Resolve on a nil registry did not fail")
	}
}

// Orka wins when both are installed.
func TestSelectPlatformPrefersOrkaWhenBothInstalled(t *testing.T) {
	id, err := SelectPlatform(PlatformDetection{Installed: true}, PlatformDetection{Installed: true})
	if err != nil || id != Orka {
		t.Fatalf("SelectPlatform() = %q, %v", id, err)
	}
}

func TestSelectPlatformFallsBackToKagentV1WhenOrkaAbsent(t *testing.T) {
	id, err := SelectPlatform(PlatformDetection{}, PlatformDetection{Installed: true})
	if err != nil || id != KagentV1 {
		t.Fatalf("SelectPlatform() = %q, %v", id, err)
	}
}

// Discovery errors fail closed and never fall through: an Orka detection
// error must not be silently treated as "Orka absent, try kagent-v1".
func TestSelectPlatformFailsClosedOnOrkaDetectionError(t *testing.T) {
	boom := errors.New("cannot read API resources")
	_, err := SelectPlatform(PlatformDetection{Err: boom}, PlatformDetection{Installed: true})
	if !errors.Is(err, boom) {
		t.Fatalf("SelectPlatform() error = %v, want %v", err, boom)
	}
}

func TestSelectPlatformFailsClosedOnKagentV1DetectionErrorWhenOrkaAbsent(t *testing.T) {
	boom := errors.New("cannot read API resources")
	_, err := SelectPlatform(PlatformDetection{}, PlatformDetection{Err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("SelectPlatform() error = %v, want %v", err, boom)
	}
}

// When neither supported platform is installed, the error names both
// install prerequisites (DESIGN.md §1).
func TestSelectPlatformNamesBothPrerequisitesWhenNoneInstalled(t *testing.T) {
	_, err := SelectPlatform(PlatformDetection{}, PlatformDetection{})
	if err == nil {
		t.Fatal("SelectPlatform succeeded although neither platform is installed")
	}
	for _, want := range []string{"core.orka.ai", "kagent.dev/v1alpha3", string(Orka), string(KagentV1)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestSelectPlatformNoInstallErrorIsTypedAndUnwrappable(t *testing.T) {
	_, err := SelectPlatform(PlatformDetection{}, PlatformDetection{})
	var target *NoPlatformInstalledError
	if !errors.As(err, &target) {
		t.Fatalf("SelectPlatform() error = %v, not *NoPlatformInstalledError", err)
	}
}
