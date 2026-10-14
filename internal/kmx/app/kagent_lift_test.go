package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func kagentLiftFixture(t *testing.T, scenario string) (*App, struct{}, string) {
	t.Helper()
	return kagentLiftFixtureMode(t, scenario, true)
}

func kagentLiftFixtureMode(t *testing.T, scenario string, inProcess bool) (*App, struct{}, string) {
	t.Helper()
	a, _, _, _, dir := kagentCreateFixtureMode(t, scenario, inProcess)
	profile := kagentLiftProfile()
	profile.AgentSchema = a.kagentProfile.AgentSchema
	profile.ModelSchema = a.kagentProfile.ModelSchema
	profile.RemoteSchema = profile.AgentSchema
	a.kagentProfile = &profile
	t.Setenv("KMX_KAGENT_TEST_VERSION", "v0.10.3")
	t.Setenv("KMX_HOME", filepath.Join(dir, "state"))
	return a, struct{}{}, dir
}

func TestKagentDeploymentTemplateFingerprintPreservesInt64(t *testing.T) {
	deployment := json.RawMessage(`{"template":{"metadata":{"labels":{"app":"kagent"}},"spec":{"securityContext":{"runAsUser":9007199254740992}}}}`)
	replicaSet := json.RawMessage(`{"template":{"metadata":{"labels":{"app":"kagent","pod-template-hash":"current"}},"spec":{"securityContext":{"runAsUser":9007199254740993}}}}`)
	want, err := kagentDeploymentTemplateFingerprint(deployment)
	if err != nil {
		t.Fatal(err)
	}
	got, err := kagentDeploymentTemplateFingerprint(replicaSet)
	if err != nil {
		t.Fatal(err)
	}
	if got == want {
		t.Fatal("adjacent int64 security identities produced equal pod-template fingerprints")
	}
	matching := bytes.Replace(replicaSet, []byte("9007199254740993"), []byte("9007199254740992"), 1)
	got, err = kagentDeploymentTemplateFingerprint(matching)
	if err != nil || got != want {
		t.Fatalf("matching int64 template with controller-added hash label rejected: %v", err)
	}
	if _, err := kagentDeploymentTemplateFingerprint(append(deployment, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON accepted as a pod-template spec")
	}
}

func fmtKind(body map[string]any) string { kind, _ := body["kind"].(string); return kind }

func assertKagentWriteKinds(t *testing.T, calls []kagentCall, want ...string) {
	t.Helper()
	var writes []string
	for _, call := range calls {
		if call.Body != nil && !slices.Contains(call.Args, "--dry-run=server") {
			writes = append(writes, fmtKind(call.Body))
		}
	}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("actual ordered writes = %v, want %v", writes, want)
	}
}

func assertKagentReadOnlyCalls(t *testing.T, calls []kagentCall) {
	t.Helper()
	for _, call := range calls {
		if call.Body != nil {
			t.Fatalf("read-only operation attempted admission or mutation: %+v", call)
		}
	}
}
