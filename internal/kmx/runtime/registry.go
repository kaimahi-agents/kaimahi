// Registry: DESIGN.md §1's shared, neutral adapter bookkeeping. It moves
// PR #197's ordered app registry loop into internal/kmx/runtime unchanged in
// behavior — unique-ID validation, typed explicit lookup, and ordered Agent
// Probe resolution that never falls back after a read error — plus
// SelectPlatform, the shared platform installation-detection policy. Neither
// this file nor any type it defines imports Kubernetes or shells out:
// Kubernetes-aware detection lives in app, which supplies this package only
// neutral PlatformDetection results.
package runtime

import (
	"context"
	"fmt"
)

// Registry is an immutable, ordered collection of Adapters keyed by their
// declared ID. Construct it once via NewRegistry; there is no mutation API,
// so a caller can share one Registry safely without synchronization.
type Registry struct {
	order []Adapter
	byID  map[ID]Adapter
}

// NewRegistry validates every adapter once, at construction: a nil adapter,
// an adapter with an empty ID, or a duplicate ID all fail closed rather than
// silently producing an ambiguous or partially unusable registry. Order is
// preserved for Resolve; by-ID lookup is O(1) via Lookup.
func NewRegistry(adapters ...Adapter) (*Registry, error) {
	byID := make(map[ID]Adapter, len(adapters))
	order := make([]Adapter, 0, len(adapters))
	for i, adapter := range adapters {
		if adapter == nil {
			return nil, fmt.Errorf("registry: adapter at position %d is nil", i)
		}
		id := adapter.ID()
		if id == "" {
			return nil, fmt.Errorf("registry: adapter at position %d has an empty ID", i)
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("registry: duplicate adapter ID %q", id)
		}
		byID[id] = adapter
		order = append(order, adapter)
	}
	return &Registry{order: order, byID: byID}, nil
}

// UnknownRuntimeError is Lookup's typed failure: no adapter is registered
// under the requested ID. Callers recover it with errors.As rather than
// matching a message string, matching UnsupportedVerbError's contract.
type UnknownRuntimeError struct {
	Runtime ID
}

func (e *UnknownRuntimeError) Error() string {
	return fmt.Sprintf("unknown runtime %q", e.Runtime)
}

// Lookup returns the adapter registered under id. An explicit runtime ID
// always resolves to exactly one adapter or a typed *UnknownRuntimeError,
// never a fallback to a different runtime.
func (r *Registry) Lookup(id ID) (Adapter, error) {
	if r != nil {
		if adapter, ok := r.byID[id]; ok {
			return adapter, nil
		}
	}
	return nil, &UnknownRuntimeError{Runtime: id}
}

// Resolve probes every registered adapter in order and returns the first
// Agent found, preserving PR #197's exact read-error semantics: a Probe
// error is never treated as absence. It stops resolution immediately and is
// returned as-is, so a caller can never silently fall back to a different
// runtime after an unreadable cluster.
func (r *Registry) Resolve(ctx context.Context, target Target) (AgentRef, error) {
	if r != nil {
		for _, adapter := range r.order {
			probe, err := adapter.Probe(ctx, target)
			if err != nil {
				return AgentRef{}, err
			}
			if probe.Found {
				return probe.Agent, nil
			}
		}
	}
	return AgentRef{}, fmt.Errorf("Agent %s/%s was not found in the registered runtimes", target.Namespace, target.Name)
}

// PlatformDetection is one platform's app-supplied installation-detection
// result. internal/kmx/runtime never inspects Kubernetes itself: app
// performs the actual API-resource check (Orka's core.orka.ai Agent CRD,
// kagent-v1's kagent.dev/v1alpha3 AgentTemplate CRD) and hands back this
// neutral installed/error result. Err, like Probe's read-error semantics,
// is never silently treated as "not installed".
type PlatformDetection struct {
	Installed bool
	Err       error
}

// Prerequisite names naming Orka and kagent-v1's install requirements in
// NoPlatformInstalledError's message, matching DESIGN.md §1's exact wording.
const (
	orkaInstallPrerequisite     = "core.orka.ai Agent CRD"
	kagentV1InstallPrerequisite = "kagent.dev/v1alpha3 AgentTemplate CRD"
)

// NoPlatformInstalledError is SelectPlatform's typed failure when neither
// supported platform is installed. It names both install prerequisites so
// an operator knows exactly what to install next.
type NoPlatformInstalledError struct{}

func (e *NoPlatformInstalledError) Error() string {
	return fmt.Sprintf("no supported runtime platform is installed: install %s (%s) or %s (%s)",
		Orka, orkaInstallPrerequisite, KagentV1, kagentV1InstallPrerequisite)
}

// SelectPlatform implements DESIGN.md §1's shared platform auto-detection
// policy for lifecycle commands with no explicit runtime: Orka wins when
// both are installed; kagent-v1 is the fallback; a detection error on
// either platform fails closed and is returned as-is rather than being
// treated as absence; and when neither is installed, the returned error
// names both prerequisites. Legacy kagent never participates here — per
// DESIGN.md §1 it "remains available only by explicit ID" — so this takes
// exactly Orka's and kagent-v1's results, not a general adapter list.
func SelectPlatform(orka, kagentV1 PlatformDetection) (ID, error) {
	if orka.Err != nil {
		return "", orka.Err
	}
	if orka.Installed {
		return Orka, nil
	}
	if kagentV1.Err != nil {
		return "", kagentV1.Err
	}
	if kagentV1.Installed {
		return KagentV1, nil
	}
	return "", &NoPlatformInstalledError{}
}
