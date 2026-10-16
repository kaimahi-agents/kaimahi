package app

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReleaseLiftKagentProvenance(t *testing.T) {
	a, opt, _, _ := releaseKagentFixture(t)
	t.Setenv("KMX_HOME", t.TempDir())
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(p.kagent.prepared.documents)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err != nil || result.State != "ready" {
		t.Fatalf("apply: %s %v", result.State, err)
	}
	after, _ := json.Marshal(p.kagent.prepared.documents)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stamping mutated prepared kagent documents")
	}
	var stamp string
	for _, row := range result.Resources {
		raw, err := a.kagentCapture(t.Context(), nil, "-n", p.summary.Target.Namespace, "get", kagentPlural(row.Kind), row.Name, "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var object kagentObject
		if json.Unmarshal(raw, &object) != nil {
			t.Fatal("invalid object")
		}
		record := object.Metadata.Annotations["kaimahi.dev/lift-record"]
		if record == "" {
			t.Fatal("kagent placement has no Lift record")
		}
		if stamp != "" && stamp != record {
			t.Fatal("kagent pair stamped inconsistently")
		}
		stamp = record
	}
}

func TestReleaseLiftProvenanceUsesActualGate(t *testing.T) {
	a, _, opt, dir := releaseApplyFixture(t, "both")
	t.Setenv("KMX_HOME", t.TempDir())
	opt.Promote = true
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil {
		t.Fatal(err)
	}
	for _, call := range persistentOrkaCalls(t, dir) {
		raw := call.Document["metadata"].(map[string]any)["annotations"].(map[string]any)["kaimahi.dev/lift-record"].(string)
		var record map[string]any
		if json.Unmarshal([]byte(raw), &record) != nil || record["gate"] != "same-subject" || record["evaluatedOnTarget"] != true {
			t.Fatalf("requested promotion rewrote actual evidence gate: %s", raw)
		}
	}
}

func TestWorkspaceLiftProvenanceFrozenAliasAndSkip(t *testing.T) {
	a, opt, _, _, _, _ := workspaceLiftFixture(t)
	for i := 0; i < 2; i++ {
		p, err := PrepareWorkspaceLift(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.LiftWorkspace(t.Context(), p)
		closeErr := p.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("run %d: %v %v", i, err, closeErr)
		}
		if i == 1 && result.Lift.Status != "skipped" {
			t.Fatal("repeat did not remain GET-only skip")
		}
	}
	raw, err := a.orkaCapture(t.Context(), nil, "-n", OrkaNamespace, "get", "agents.core.orka.ai", "greeter", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &object) != nil {
		t.Fatal("invalid object")
	}
	var record map[string]any
	if json.Unmarshal([]byte(object.Metadata.Annotations["kaimahi.dev/lift-record"]), &record) != nil {
		t.Fatal("missing Lift annotation")
	}
	if record["evaluationName"] != "dev" || record["gate"] != "promotion" || record["evaluatedOnTarget"] != false {
		t.Fatalf("lost frozen eval alias/promotion: %+v", record)
	}
}
