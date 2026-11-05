package conditionfacts

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestIntegerWireTypes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  int64
		valid bool
	}{
		{"zero", float64(0), 0, true},
		{"whole", float64(4), 4, true},
		{"largest-exact", float64(9007199254740991), 9007199254740991, true},
		{"overflow", float64(9007199254740992), 0, false},
		{"negative", float64(-1), 0, false},
		{"fraction", 1.5, 0, false},
		{"nan", math.NaN(), 0, false},
		{"infinity", math.Inf(1), 0, false},
		{"integer", 1, 0, false},
		{"int64", int64(1), 0, false},
		{"number", json.Number("1"), 0, false},
		{"string", "1", 0, false},
		{"nil", nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, valid := Integer(tc.value); got != tc.want || valid != tc.valid {
				t.Fatalf("got %d/%t", got, valid)
			}
		})
	}
}

func TestReadMapFacts(t *testing.T) {
	for _, tc := range []struct {
		input     string
		shape     Shape
		entries   []Entry
		duplicate bool
	}{
		{`{}`, Absent, nil, false},
		{`{"conditions":null}`, Null, nil, false},
		{`{"conditions":{}}`, Invalid, nil, false},
		{`{"Conditions":[]}`, Absent, nil, false},
		{`{"conditions":[]}`, Array, []Entry{}, false},
		{`{"conditions":[null]}`, Array, []Entry{{}}, false},
		{`{"conditions":[{"type":"Ready","status":"True","observedGeneration":7,"reason":"Known","message":"not-a-fact"}]}`, Array, []Entry{{Type: "Ready", State: "True", Reason: "Known", Object: true, TypeValid: true, StateValid: true, ObservedGeneration: 7, ObservedPresent: true, ObservedValid: true}}, false},
		{`{"conditions":[{"type":"Ready","status":"False"}]}`, Array, []Entry{{Type: "Ready", State: "False", Object: true, TypeValid: true, StateValid: true, ObservedValid: true}}, false},
		{`{"conditions":[{"type":"Ready","status":"Unknown","observedGeneration":null,"reason":4}]}`, Array, []Entry{{Type: "Ready", State: "Unknown", Object: true, TypeValid: true, StateValid: true, ObservedPresent: true}}, false},
		{`{"conditions":[{"type":"Ready","status":"true","observedGeneration":1.5}]}`, Array, []Entry{{Type: "Ready", State: "true", Object: true, TypeValid: true, ObservedPresent: true}}, false},
		{`{"conditions":[{"Type":"Ready","Status":"True"}]}`, Array, []Entry{{Object: true, ObservedValid: true}}, false},
		{`{"conditions":[{"type":"","status":"True"}]}`, Array, []Entry{{State: "True", Object: true, StateValid: true, ObservedValid: true}}, false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var status map[string]any
			if err := json.Unmarshal([]byte(tc.input), &status); err != nil {
				t.Fatal(err)
			}
			got := ReadMap(status)
			if got.Shape() != tc.shape || got.Len() != len(tc.entries) || !reflect.DeepEqual(got.Entries(), tc.entries) || got.HasDuplicateTypes() != tc.duplicate {
				t.Fatalf("got %+v, entries %+v", got, got.Entries())
			}
		})
	}
	if got := ReadMap(nil); got.Shape() != Absent {
		t.Fatal(got)
	}
}

func TestReadMapKeepsOrderAndDuplicates(t *testing.T) {
	var status map[string]any
	json.Unmarshal([]byte(`{"conditions":[{"type":"Ready","status":"True"},{"type":"ready","status":"Unknown"},{"type":"Ready","status":"False"}]}`), &status)
	got := ReadMap(status)
	entries := got.Entries()
	if !got.HasDuplicateTypes() || got.Len() != 3 || entries[0].State != "True" || entries[1].Type != "ready" || entries[2].State != "False" {
		t.Fatal(got, entries)
	}
	status["conditions"] = status["conditions"].([]any)[:2]
	if ReadMap(status).HasDuplicateTypes() {
		t.Fatal("case-distinct types collapsed")
	}
	status["conditions"] = []any{map[string]any{"type": ""}, map[string]any{"type": ""}}
	if !ReadMap(status).HasDuplicateTypes() {
		t.Fatal("repeated empty string types were discarded")
	}
}

func TestReadMapEntriesAreCopies(t *testing.T) {
	row := map[string]any{"type": "Ready", "status": "True"}
	got := ReadMap(map[string]any{"conditions": []any{row}})
	row["status"] = "False"
	entries := got.Entries()
	entries[0].Type = "changed"
	if current := got.Entries()[0]; current.Type != "Ready" || current.State != "True" {
		t.Fatal(current)
	}
}
