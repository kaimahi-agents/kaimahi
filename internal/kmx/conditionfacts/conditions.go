// Package conditionfacts reads exact-key condition facts from decoded Kubernetes
// JSON. Callers own resource identity, bounds, generation and readiness policy.
package conditionfacts

import "math"

// Shape distinguishes omitted, null, array and malformed condition fields.
type Shape uint8

const (
	Absent Shape = iota
	Null
	Array
	Invalid
)

// Entry preserves row validity and generation presence without deciding readiness.
// A missing observedGeneration is valid zero; an explicit null is invalid.
type Entry struct {
	Type, State, Reason            string
	Object, TypeValid, StateValid  bool
	ObservedGeneration             int64
	ObservedPresent, ObservedValid bool
}

// List retains every row in wire order, including malformed and duplicate rows.
type List struct {
	shape      Shape
	entries    []Entry
	duplicates bool
}

func (l List) Shape() Shape            { return l.shape }
func (l List) Len() int                { return len(l.entries) }
func (l List) HasDuplicateTypes() bool { return l.duplicates }
func (l List) Entries() []Entry {
	if l.entries == nil {
		return nil
	}
	return append([]Entry{}, l.entries...)
}

// Integer accepts only JSON-map float64 values in the exact nonnegative range.
func Integer(value any) (int64, bool) {
	n, ok := value.(float64)
	if !ok || n < 0 || n > 1<<53-1 || math.Trunc(n) != n {
		return 0, false
	}
	return int64(n), true
}

// ReadMap decodes facts without imposing a size cap or rejecting future generations.
// Optional controller messages are not retained; non-string reasons become empty.
func ReadMap(status map[string]any) List {
	raw, present := status["conditions"]
	if !present {
		return List{shape: Absent}
	}
	if raw == nil {
		return List{shape: Null}
	}
	rows, ok := raw.([]any)
	if !ok {
		return List{shape: Invalid}
	}
	result := List{shape: Array, entries: make([]Entry, len(rows))}
	seen := make(map[string]bool)
	for i, value := range rows {
		row, ok := value.(map[string]any)
		if !ok {
			continue
		}
		e := &result.entries[i]
		e.Object = true
		e.Type, e.TypeValid = row["type"].(string)
		if e.TypeValid {
			result.duplicates = result.duplicates || seen[e.Type]
			seen[e.Type] = true
		}
		e.TypeValid = e.TypeValid && e.Type != ""
		e.State, e.StateValid = row["status"].(string)
		e.StateValid = e.StateValid && (e.State == "True" || e.State == "False" || e.State == "Unknown")
		e.Reason, _ = row["reason"].(string)
		observed, present := row["observedGeneration"]
		e.ObservedPresent, e.ObservedValid = present, true
		if present {
			e.ObservedGeneration, e.ObservedValid = Integer(observed)
		}
	}
	return result
}
