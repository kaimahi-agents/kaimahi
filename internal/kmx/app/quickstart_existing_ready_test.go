package app

import (
	"strings"
	"testing"
)

// The already-attached tool path must not turn name discovery into readiness.
func TestQuickstartExistingAgentRequiresCurrentReadiness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ready    bool
		observed int
		want     string
	}{
		{"ready", true, 2, ""},
		{"not ready", false, 2, "not Ready"},
		{"stale Ready condition", true, 1, "not Ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, f := nativeFixture(t)
			f.objects["agents.core.orka.ai/existing"] = map[string]any{
				"kind":     "Agent",
				"metadata": map[string]any{"name": "existing", "namespace": OrkaNamespace, "uid": "existing-uid", "resourceVersion": "1", "generation": 2},
				"spec":     map[string]any{"tools": []any{map[string]any{"name": "k8s-get-resources"}}},
				"status":   map[string]any{"ready": tc.ready, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": tc.observed}}},
			}
			// This fixture accepts reads only: patches, admission and Tasks fail.
			err := a.attachQuickstartK8sTool("existing", OrkaNamespace)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("existing Agent incorrectly accepted as ready: %v", err)
			}
		})
	}
}
