package app

import (
	"strings"
	"testing"
)

// The operator uses the real bounded inventory and UID/RV-conditioned DELETE
// path; only Kubernetes transport is replaced by the existing API fixture.
func TestFlowCandidateOperatorCleanup(t *testing.T) {
	for _, mode := range []string{"owned", "mixed-suite", "wrong-tuple", "missing-run", "unanchored-name"} {
		t.Run(mode, func(t *testing.T) {
			a, r, f, _ := candidateCleanupSetup(t)
			projection := suiteFlowCandidateProjection{flowCandidateVersion, r.subject.SuiteDigest, r.subject.Member, "facts", 1, "draft", r.subject.EvaluationRun}
			name, err := projection.physicalName()
			if err != nil {
				t.Fatal(err)
			}
			owner := *suiteNativeOwner(r.subject)
			owner.FlowCandidate = &projection
			for _, kind := range []string{"Provider", "Agent"} {
				old := kind + "/" + r.subject.PhysicalMember
				doc := f.objects[old]
				delete(f.objects, old)
				meta := nativeMetadata(doc)
				meta["name"] = name
				meta["annotations"] = owner.markers()
				f.objects[kind+"/"+name] = doc
			}
			if mode == "mixed-suite" {
				foreign := owner
				other := projection
				other.Suite = "sha256:" + strings.Repeat("e", 64)
				foreign.Digest = other.Suite
				foreign.FlowCandidate = &other
				physical, _ := other.physicalName()
				f.objects["Provider/"+physical] = map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": map[string]any{"name": physical, "namespace": OrkaNamespace, "uid": "foreign", "resourceVersion": "1", "generation": 1, "annotations": foreign.markers()}, "spec": map[string]any{"type": "openai"}}
			}
			if mode == "wrong-tuple" {
				foreign := owner
				other := projection
				other.Case = "other"
				foreign.FlowCandidate = &other
				nativeMetadata(f.objects["Agent/"+name])["annotations"] = foreign.markers()
			}
			if mode == "missing-run" {
				delete(nativeMetadata(f.objects["Agent/"+name])["annotations"].(map[string]any), suiteCandidateRunMarker)
			}
			if mode == "unanchored-name" {
				// A replacement appearing in a GET snapshot with mismatched name/UID
				// cannot be attributed to the frozen name; discovery refuses completeness.
				nativeMetadata(f.objects["Agent/"+name])["name"] = "foreign-name"
			}
			a.Cfg.Confirm = a.Cfg.KubeContext
			err = a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace, RunID: projection.Operation})
			if mode == "owned" {
				if err != nil || len(f.deletes) != 2 || len(f.objects) != 0 {
					t.Fatal("owned flow cleanup failed", err, f.deletes)
				}
			} else if err == nil || len(f.deletes) != 0 {
				t.Fatal("unverified flow cleanup deleted roots", err, f.deletes)
			}
		})
	}
}
