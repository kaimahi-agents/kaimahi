// Neutral adapter bookkeeping: unique-ID validation, typed explicit lookup,
// ordered probe resolution that never falls back after a read error, and
// platform selection over caller-supplied detection results. Nothing here
// names a runtime, imports Kubernetes, shells out, or gives install advice.
package runtime

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// Registry is an immutable, ordered collection of Adapters keyed by ID.
// Construct it once through NewRegistry; there is no mutation API, so one
// Registry can be shared without synchronization.
type Registry struct {
	order []Adapter
	byID  map[ID]Adapter
}

// NewRegistry validates every adapter at construction: a nil adapter, an empty
// ID, or a duplicate ID fails closed rather than producing an ambiguous or
// partially unusable registry. Registration order is the resolution order.
func NewRegistry(adapters ...Adapter) (*Registry, error) {
	registry := &Registry{order: make([]Adapter, 0, len(adapters)), byID: make(map[ID]Adapter, len(adapters))}
	for i, adapter := range adapters {
		if isNilAdapter(adapter) {
			return nil, fmt.Errorf("registry: adapter at position %d is nil", i)
		}
		id := adapter.ID()
		if id == "" {
			return nil, fmt.Errorf("registry: adapter at position %d has an empty ID", i)
		}
		if _, exists := registry.byID[id]; exists {
			return nil, fmt.Errorf("registry: duplicate adapter ID %q", id)
		}
		registry.byID[id] = adapter
		registry.order = append(registry.order, adapter)
	}
	return registry, nil
}

// isNilAdapter reports whether adapter is nil either as an untyped nil
// interface or as a nil value stored inside a non-nil interface (a
// "typed nil", e.g. a nil *SomeAdapter assigned to the Adapter interface).
// Calling a method on such a value would either panic or, worse, silently
// operate on a nil receiver, so registration must reject it before ever
// invoking ID(). Only kinds that can hold a nil dynamic value are checked;
// every other kind (e.g. a struct value) can never be nil.
func isNilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return value.IsNil()
	default:
		return false
	}
}

// UnknownRuntimeError is Lookup's typed failure: no adapter is registered
// under the requested ID. Callers recover it with errors.As, matching
// UnsupportedVerbError's contract.
type UnknownRuntimeError struct {
	Runtime ID
}

func (e *UnknownRuntimeError) Error() string {
	return fmt.Sprintf("unknown runtime %q", e.Runtime)
}

// Lookup returns the adapter registered under id. An explicit runtime ID
// always resolves to exactly one adapter or to *UnknownRuntimeError, never to
// a different runtime.
func (r *Registry) Lookup(id ID) (Adapter, error) {
	if adapter, ok := r.byID[id]; ok {
		return adapter, nil
	}
	return nil, &UnknownRuntimeError{Runtime: id}
}

// Resolve probes every registered adapter in order and returns the first Agent
// found. A probe error is never treated as absence: it stops resolution
// immediately and is returned unchanged, so no caller can silently fall back
// to a different runtime after an unreadable read.
func (r *Registry) Resolve(ctx context.Context, target Target) (AgentRef, error) {
	for _, adapter := range r.order {
		probe, err := adapter.Probe(ctx, target)
		if err != nil {
			return AgentRef{}, err
		}
		if probe.Found {
			return probe.Agent, nil
		}
	}
	return AgentRef{}, fmt.Errorf("Agent %s/%s was not found in the registered runtimes", target.Namespace, target.Name)
}

// PlatformDetection is one platform's installation-detection result, supplied
// by the caller that performed the check: this package never inspects a
// cluster. Err, like a probe error, is never treated as "not installed".
type PlatformDetection struct {
	Runtime   ID
	Installed bool
	Err       error
}

// NoPlatformInstalledError is SelectPlatform's typed failure when no candidate
// is installed. It names exactly the candidates that were checked, so a caller
// can report what was looked for without this package knowing any platform's
// install prerequisites.
type NoPlatformInstalledError struct {
	candidates []ID
}

// Candidates returns a copy of the checked candidates, in preference order.
func (e *NoPlatformInstalledError) Candidates() []ID {
	return append([]ID(nil), e.candidates...)
}

func (e *NoPlatformInstalledError) Error() string {
	if len(e.candidates) == 0 {
		return "no runtime platform is installed: no platforms were checked"
	}
	names := make([]string, len(e.candidates))
	for i, candidate := range e.candidates {
		names[i] = string(candidate)
	}
	return fmt.Sprintf("no runtime platform is installed: checked %s", strings.Join(names, ", "))
}

// SelectPlatform chooses a runtime from detection results given in preference
// order: the first installed candidate wins, a detection error on a candidate
// of equal or higher preference fails closed and is returned unchanged, and no
// candidate installed yields *NoPlatformInstalledError naming what was
// checked. Candidates are validated first, because an empty or duplicated ID
// makes any answer ambiguous.
func SelectPlatform(detections ...PlatformDetection) (ID, error) {
	candidates := make([]ID, 0, len(detections))
	seen := make(map[ID]struct{}, len(detections))
	for i, detection := range detections {
		if detection.Runtime == "" {
			return "", fmt.Errorf("platform selection: candidate at position %d has an empty runtime ID", i)
		}
		if _, exists := seen[detection.Runtime]; exists {
			return "", fmt.Errorf("platform selection: duplicate runtime ID %q", detection.Runtime)
		}
		seen[detection.Runtime] = struct{}{}
		candidates = append(candidates, detection.Runtime)
	}
	for _, detection := range detections {
		if detection.Err != nil {
			return "", detection.Err
		}
		if detection.Installed {
			return detection.Runtime, nil
		}
	}
	return "", &NoPlatformInstalledError{candidates: candidates}
}
