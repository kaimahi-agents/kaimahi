package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type probeAdapter struct {
	id     ID
	probe  Probe
	err    error
	probed *int
}

func (a probeAdapter) ID() ID { return a.id }
func (a probeAdapter) Probe(context.Context, Target) (Probe, error) {
	if a.probed != nil {
		*a.probed++
	}
	return a.probe, a.err
}
func (a probeAdapter) Open(context.Context, Target) (Session, error) { return nil, nil }

func TestNewRegistryRejectsAmbiguousRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		adapters []Adapter
	}{
		{"nil adapter", []Adapter{probeAdapter{id: testRuntime}, nil}},
		{"empty ID", []Adapter{probeAdapter{}}},
		{"duplicate ID", []Adapter{probeAdapter{id: testRuntime}, probeAdapter{id: testRuntime}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRegistry(tc.adapters...); err == nil {
				t.Fatal("NewRegistry accepted registrations it must refuse")
			}
		})
	}
	if _, err := NewRegistry(); err != nil {
		t.Fatalf("an empty registry is legal: %v", err)
	}
}

// An explicit runtime ID resolves to exactly one adapter or to a typed error,
// never to a different runtime.
func TestLookupIsExactOrTyped(t *testing.T) {
	registry, err := NewRegistry(probeAdapter{id: testRuntime}, probeAdapter{id: otherRuntime})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	adapter, err := registry.Lookup(otherRuntime)
	if err != nil || adapter.ID() != otherRuntime {
		t.Fatalf("Lookup(%q) = %v, %v", otherRuntime, adapter, err)
	}

	var unknown *UnknownRuntimeError
	if _, err = registry.Lookup("missing"); !errors.As(err, &unknown) {
		t.Fatalf("Lookup of an unregistered ID returned %v, want *UnknownRuntimeError", err)
	}
	if unknown.Runtime != "missing" {
		t.Fatalf("UnknownRuntimeError.Runtime = %q, want %q", unknown.Runtime, "missing")
	}
}

func TestResolveReturnsFirstMatchInRegistrationOrder(t *testing.T) {
	later := 0
	registry, err := NewRegistry(
		probeAdapter{id: testRuntime, probe: Probe{Found: true, Agent: AgentRef{Runtime: testRuntime, Name: "agent"}}},
		probeAdapter{id: otherRuntime, probe: Probe{Found: true}, probed: &later},
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ref, err := registry.Resolve(context.Background(), Target{Namespace: "ns", Name: "agent"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Runtime != testRuntime {
		t.Fatalf("Resolve chose %q, want the first registered match %q", ref.Runtime, testRuntime)
	}
	if later != 0 {
		t.Fatalf("later adapter was probed %d times after an earlier match", later)
	}
}

// A read error is never absence: resolution stops and the error is returned
// as-is, so a caller can never silently fall back to a different runtime after
// an unreadable cluster.
func TestResolveNeverFallsBackAfterProbeError(t *testing.T) {
	unreadable := errors.New("cannot read cluster")
	later := 0
	registry, err := NewRegistry(probeAdapter{id: testRuntime, err: unreadable}, probeAdapter{id: otherRuntime, probe: Probe{Found: true}, probed: &later})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ref, err := registry.Resolve(context.Background(), Target{Namespace: "ns", Name: "agent"})
	if !errors.Is(err, unreadable) {
		t.Fatalf("Resolve error = %v, want the probe error unchanged", err)
	}
	if ref != (AgentRef{}) || later != 0 {
		t.Fatalf("Resolve fell back after a read error: ref %+v, later probes %d", ref, later)
	}
}

func TestResolveReportsNotFound(t *testing.T) {
	registry, err := NewRegistry(probeAdapter{id: testRuntime})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, err = registry.Resolve(context.Background(), Target{Namespace: "ns", Name: "agent"}); err == nil {
		t.Fatal("Resolve reported success with no adapter finding the Agent")
	}
}

func TestSelectPlatformPrefersTheFirstInstalledCandidate(t *testing.T) {
	selected, err := SelectPlatform(
		PlatformDetection{Runtime: testRuntime, Installed: true},
		PlatformDetection{Runtime: otherRuntime, Installed: true},
	)
	if err != nil || selected != testRuntime {
		t.Fatalf("SelectPlatform = %q, %v, want %q in preference order", selected, err, testRuntime)
	}
	selected, err = SelectPlatform(
		PlatformDetection{Runtime: testRuntime},
		PlatformDetection{Runtime: otherRuntime, Installed: true},
	)
	if err != nil || selected != otherRuntime {
		t.Fatalf("SelectPlatform = %q, %v, want the installed fallback %q", selected, err, otherRuntime)
	}
}

// Detection failure is not absence. A candidate that could not be read fails
// closed before any lower-preference candidate is considered.
func TestSelectPlatformFailsClosedOnDetectionError(t *testing.T) {
	unreadable := errors.New("cannot read cluster")
	if _, err := SelectPlatform(
		PlatformDetection{Runtime: testRuntime, Err: unreadable},
		PlatformDetection{Runtime: otherRuntime, Installed: true},
	); !errors.Is(err, unreadable) {
		t.Fatalf("SelectPlatform error = %v, want the detection error unchanged", err)
	}
	if selected, err := SelectPlatform(
		PlatformDetection{Runtime: testRuntime, Installed: true},
		PlatformDetection{Runtime: otherRuntime, Err: unreadable},
	); err != nil || selected != testRuntime {
		t.Fatalf("SelectPlatform = %q, %v: a lower-preference failure must not mask an installed platform", selected, err)
	}
}

func TestSelectPlatformRejectsAmbiguousCandidates(t *testing.T) {
	if _, err := SelectPlatform(PlatformDetection{Installed: true}); err == nil {
		t.Fatal("SelectPlatform accepted a candidate with an empty runtime ID")
	}
	if _, err := SelectPlatform(
		PlatformDetection{Runtime: testRuntime},
		PlatformDetection{Runtime: testRuntime, Installed: true},
	); err == nil {
		t.Fatal("SelectPlatform accepted duplicate runtime IDs")
	}
}

func TestSelectPlatformReportsCheckedCandidatesWhenNoneAreInstalled(t *testing.T) {
	_, err := SelectPlatform(PlatformDetection{Runtime: testRuntime}, PlatformDetection{Runtime: otherRuntime})
	var none *NoPlatformInstalledError
	if !errors.As(err, &none) {
		t.Fatalf("SelectPlatform error = %v, want *NoPlatformInstalledError", err)
	}
	candidates := none.Candidates()
	if len(candidates) != 2 || candidates[0] != testRuntime || candidates[1] != otherRuntime {
		t.Fatalf("Candidates() = %v, want the checked candidates in preference order", candidates)
	}
	candidates[0] = "mutated"
	if none.Candidates()[0] != testRuntime {
		t.Fatal("Candidates() exposed mutable internal state")
	}
	for _, candidate := range []ID{testRuntime, otherRuntime} {
		if !strings.Contains(none.Error(), string(candidate)) {
			t.Fatalf("error %q does not name checked candidate %q", none.Error(), candidate)
		}
	}
	if _, err = SelectPlatform(); !errors.As(err, &none) || len(none.Candidates()) != 0 {
		t.Fatalf("SelectPlatform with no candidates = %v, want an empty *NoPlatformInstalledError", err)
	}
}
