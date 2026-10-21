package app

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The operator uses the real bounded inventory and UID/RV-conditioned DELETE
// path; only Kubernetes transport is replaced by the existing API fixture.
func TestFlowCandidateOperatorCleanup(t *testing.T) {
	for _, mode := range []string{"owned", "owned-multiple", "mixed-suite", "mixed-rendered", "wrong-tuple", "missing-run", "unanchored-name"} {
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
			if mode == "mixed-suite" || mode == "owned-multiple" {
				foreign := owner
				other := projection
				if mode == "mixed-suite" {
					other.Suite = "sha256:" + strings.Repeat("e", 64)
					foreign.Digest = other.Suite
				} else {
					other.Case = "other"
					foreign.RenderedDigest = strings.Repeat("d", 64)
				}
				foreign.FlowCandidate = &other
				physical, _ := other.physicalName()
				f.objects["Provider/"+physical] = map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": map[string]any{"name": physical, "namespace": OrkaNamespace, "uid": "foreign", "resourceVersion": "1", "generation": 1, "annotations": foreign.markers()}, "spec": map[string]any{"type": "openai"}}
			}
			if mode == "mixed-rendered" {
				foreign := owner
				foreign.RenderedDigest = strings.Repeat("e", 64)
				nativeMetadata(f.objects["Agent/"+name])["annotations"] = foreign.markers()
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
			if mode == "owned" || mode == "owned-multiple" {
				want := 2
				if mode == "owned-multiple" {
					want = 3
				}
				if err != nil || len(f.deletes) != want || len(f.objects) != 0 {
					t.Fatal("owned flow cleanup failed", err, f.deletes)
				}
			} else if err == nil || len(f.deletes) != 0 {
				t.Fatal("unverified flow cleanup deleted roots", err, f.deletes)
			}
		})
	}
}

func TestFlowCandidateOperatorCleanupKagentMarker(t *testing.T) {
	for _, stage := range []string{"discovery", "root-revalidation"} {
		for _, value := range []struct {
			name   string
			marker any
		}{{"populated", "v0.10.3"}, {"empty", ""}, {"null", nil}} {
			t.Run(stage+"/"+value.name, func(t *testing.T) {
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
				meta := nativeMetadata(f.objects["Agent/"+name])
				rootReads := 0
				if stage == "discovery" {
					meta["annotations"].(map[string]any)[kagentVersionMarker] = value.marker
				} else {
					f.change = func(_ string, cmd *exec.Cmd) error {
						if slices.Contains(cmd.Args, "agents.core.orka.ai") && slices.Contains(cmd.Args, name) && slices.Contains(cmd.Args, "json") {
							rootReads++
							if rootReads == 2 {
								// Leave discovery and the initial root check clean; change
								// ownership only at the fresh pre-DELETE root read.
								meta["annotations"].(map[string]any)[kagentVersionMarker] = value.marker
								meta["resourceVersion"] = "2"
							}
						}
						return nil
					}
				}
				a.Cfg.Confirm = a.Cfg.KubeContext
				err = a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace, RunID: projection.Operation})
				if stage == "root-revalidation" && rootReads != 2 {
					t.Fatalf("did not reach the fresh pre-DELETE root read: reads=%d err=%v", rootReads, err)
				}
				if err == nil || len(f.deletes) != 0 || len(f.objects) != 2 {
					t.Fatalf("mixed kagent marker authorized cleanup: err=%v deletes=%v remaining=%d", err, f.deletes, len(f.objects))
				}
				wantReason := "uncertain candidate inventory"
				if stage == "root-revalidation" {
					wantReason = "root changed or read unknown"
				}
				if !strings.Contains(err.Error(), wantReason) {
					t.Fatalf("wrong refusal path: %v; want %q", err, wantReason)
				}
			})
		}
	}
}
