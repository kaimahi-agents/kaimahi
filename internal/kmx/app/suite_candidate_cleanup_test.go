package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSuiteCandidateCleanupKnownPair(t *testing.T) {
	a, r, f, reads := candidateCleanupSetup(t)
	shared := mustJSON(t, reads.objects)
	f.objects["Agent/greeter"] = map[string]any{"metadata": map[string]any{"uid": "canonical-uid"}}
	result, err := a.cleanupSuiteNativeCandidate(cleanupContext(t), r)
	if err != nil || !result.AllGone || len(f.deletes) != 2 {
		t.Fatalf("cleanup: %+v %v deletes=%v", result, err, f.deletes)
	}
	if f.objects["Agent/greeter"] == nil || !bytes.Equal(shared, mustJSON(t, reads.objects)) {
		t.Fatal("shared/canonical touched")
	}
}

func TestSuiteCandidateCleanupTaskClosure(t *testing.T) {
	a, r, f, _ := candidateCleanupSetup(t)
	task := cleanupTask(t, r, f)
	// Pinned Orka truncates the Task prefix, then appends UID-prefix/attempt;
	// the physical Agent name is not a Job-name authority.
	jobName := task.Name[:48] + "-job-task-uid-0"
	f.objects["Job/"+jobName] = cleanupChild("Job", jobName, "job-uid", "Task", task.Name, task.UID)
	f.objects["Pod/task-pod"] = cleanupChild("Pod", "task-pod", "pod-uid", "Job", jobName, "job-uid")
	f.objects["ConfigMap/"+jobName+"-skills"] = cleanupChild("ConfigMap", jobName+"-skills", "cm-uid", "Task", task.Name, task.UID)
	f.objects["Pod/foreign"] = cleanupChild("Pod", "foreign", "foreign-uid", "Job", "same-prefix", "foreign-job-uid")
	observed := false
	f.afterDelete = func(key string) {
		if key == "Task/"+task.Name {
			for _, x := range r.Resources() {
				if x.UID == "job-uid" {
					observed = true
				}
			}
			delete(f.objects, "Job/"+jobName)
			delete(f.objects, "ConfigMap/"+jobName+"-skills")
		}
	}
	f.change = func(action string, cmd *exec.Cmd) error {
		if f.lists >= 6 && len(f.deletes) > 0 {
			delete(f.objects, "Pod/task-pod")
		}
		return nil
	}
	result, err := a.cleanupSuiteNativeCandidate(cleanupContext(t), r)
	if err != nil || !result.AllGone || !observed || f.objects["Pod/foreign"] == nil || len(f.deletes) != 3 || !strings.HasPrefix(f.deletes[0], "Task/") {
		t.Fatalf("closure %+v %v observed=%v deletes=%v", result, err, observed, f.deletes)
	}
}

func TestSuiteCandidateCleanupRefusals(t *testing.T) {
	for _, stage := range []string{"UID", "spec", "markers", "namespace", "RV", "permission", "null", "empty-object", "cluster", "namespaceUID", "conflict", "unknown"} {
		t.Run(stage, func(t *testing.T) {
			a, r, f, reads := candidateCleanupSetup(t)
			key := "Agent/" + r.subject.PhysicalMember
			doc := f.objects[key]
			meta := nativeMetadata(doc)
			switch stage {
			case "UID":
				meta["uid"] = "replacement"
			case "spec":
				doc["spec"].(map[string]any)["description"] = "mutated"
			case "markers":
				delete(meta["annotations"].(map[string]any), suiteCandidateRunMarker)
			case "namespace":
				meta["namespace"] = "foreign"
			case "RV":
				meta["resourceVersion"] = ""
			case "cluster":
				nativeMetadata(reads.objects["namespace/kube-system"])["uid"] = "new-cluster"
			case "namespaceUID":
				nativeMetadata(reads.objects["namespace/orka-system"])["uid"] = "new-namespace"
			case "conflict":
				f.beforeDelete = func(context.Context, *exec.Cmd) error { return errors.New(nativeCanary) }
			case "unknown":
				if err := r.recordIntent(t.Context(), "Task", "unknown-task", strings.Repeat("a", 64)); err != nil {
					t.Fatal(err)
				}
			case "permission", "null", "empty-object":
				f.change = func(action string, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, r.subject.PhysicalMember) && action == "get" {
						if stage == "permission" {
							return errors.New(nativeCanary)
						}
						body := "null"
						if stage == "empty-object" {
							body = "{}"
						}
						fmt.Fprint(cmd.Stdout, body)
						return errors.New(nativeCanary)
					}
					return nil
				}
			}
			result, err := a.cleanupSuiteNativeCandidate(cleanupContext(t), r)
			if err == nil || result.AllGone || strings.Contains(err.Error(), nativeCanary) {
				t.Fatalf("unsafe result %+v %v", result, err)
			}
			wantReason := "root changed or read unknown"
			switch stage {
			case "UID":
				wantReason = "owned UID absent; foreign name occupied"
			case "cluster", "namespaceUID":
				wantReason = "scope changed or unknown"
			case "conflict":
				wantReason = "delete conflict or acknowledgement unknown"
			case "unknown":
				wantReason = "unknown UID intent remains"
			}
			if result.Reason != wantReason {
				t.Fatalf("wrong refusal %q want %q", result.Reason, wantReason)
			}
			if stage == "UID" && (len(result.Absent) != 1 || len(result.ForeignOccupancies) != 1 || f.objects[key] == nil) {
				t.Fatal("owned absence confused with foreign occupancy")
			}
			if stage == "unknown" {
				if slices.Contains(f.gets, "Task/unknown-task") {
					t.Fatal("recovered unknown intent")
				}
			} else if stage != "conflict" && len(f.deletes) != 0 {
				t.Fatalf("mutated after unsafe preflight %v", f.deletes)
			}
		})
	}
}
func TestSuiteCandidateCleanupFinalClosureIsNotEarlierSnapshot(t *testing.T) {
	a, r, f, _ := candidateCleanupSetup(t)
	task := cleanupTask(t, r, f)
	f.afterDelete = func(key string) {
		if key == "Provider/"+r.subject.PhysicalMember {
			f.objects["Job/late-job"] = cleanupChild("Job", "late-job", "late-job-uid", "Task", task.Name, task.UID)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 1400*time.Millisecond)
	defer cancel()
	result, err := a.cleanupSuiteNativeCandidate(ctx, r)
	if err == nil || result.AllGone || f.objects["Job/late-job"] == nil {
		t.Fatalf("late child ignored %+v %v", result, err)
	}
	found := false
	for _, x := range r.Resources() {
		found = found || x.UID == "late-job-uid"
	}
	if !found {
		t.Fatal("late child identity lost")
	}
}
func TestSuiteCandidateCleanupNoAmbiguousDeleteReplay(t *testing.T) {
	a, r, f, _ := candidateCleanupSetup(t)
	f.beforeDelete = func(context.Context, *exec.Cmd) error { return errors.New(nativeCanary) }
	if _, err := a.cleanupSuiteNativeCandidate(cleanupContext(t), r); err == nil {
		t.Fatal("conflict accepted")
	}
	f.beforeDelete = nil
	writes := len(f.deletes)
	if _, err := a.cleanupSuiteNativeCandidate(cleanupContext(t), r); err == nil || len(f.deletes) != writes {
		t.Fatalf("replayed ambiguous deletion %v", f.deletes)
	}
}

// A label or matching owner UID alone never substitutes for the complete typed controller link.
func TestCandidateCoreCleanupTypedChildAuthority(t *testing.T) {
	for _, stage := range []string{"parent name", "owner version", "noncontroller", "multiple owners", "unanchored label", "orphan survives"} {
		t.Run(stage, func(t *testing.T) {
			a, r, f, _ := candidateCleanupSetup(t)
			task := cleanupTask(t, r, f)
			job := cleanupChild("Job", "task-job", "job-uid", "Task", task.Name, task.UID)
			f.objects["Job/task-job"] = job
			owner := nativeMetadata(job)["ownerReferences"].([]any)[0].(map[string]any)
			switch stage {
			case "parent name":
				owner["name"] = "wrong"
			case "owner version":
				owner["apiVersion"] = "wrong/v1"
			case "noncontroller":
				owner["controller"] = false
			case "multiple owners":
				nativeMetadata(job)["ownerReferences"] = []any{owner, owner}
			case "unanchored label":
				owner["uid"] = "foreign"
				nativeMetadata(job)["labels"] = map[string]any{"orka.ai/task": task.Name}
			case "orphan survives":
				f.objects["Pod/orphan"] = cleanupChild("Pod", "orphan", "pod-uid", "Job", "task-job", "job-uid")
				f.afterDelete = func(string) {
					delete(f.objects, "Job/task-job")
					f.objects["Pod/orphan"] = cleanupChild("Pod", "orphan", "pod-uid", "Job", "task-job", "job-uid")
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer cancel()
			result, err := a.cleanupSuiteNativeCandidate(ctx, r)
			if err == nil || result.AllGone || len(f.deletes) > 1 {
				t.Fatal("unproven closure", result, err)
			}
			if stage != "orphan survives" && len(f.deletes) != 0 {
				t.Fatal("untyped child authorized effect")
			}
			if stage == "orphan survives" && (len(f.deletes) != 1 || f.objects["Pod/orphan"] == nil) {
				t.Fatal("Task404 lost retained Job→Pod UID")
			}
		})
	}
}

func TestSuiteCandidateCleanupAlreadyFinalizingGeneration(t *testing.T) {
	for _, stage := range []string{"one bump disappears", "one bump waits", "normal bump", "terminating spec drift", "terminating two bumps", "terminating invalid timestamp"} {
		t.Run(stage, func(t *testing.T) {
			a, r, f, _ := candidateCleanupSetup(t)
			key := "Agent/" + r.subject.PhysicalMember
			doc := f.objects[key]
			meta := nativeMetadata(doc)
			meta["generation"] = 2
			if stage != "normal bump" {
				meta["deletionTimestamp"] = "2026-01-01T00:00:00Z"
				meta["finalizers"] = []any{"example.invalid/finalizer"}
			}
			if stage == "terminating spec drift" {
				doc["spec"].(map[string]any)["description"] = "changed"
			}
			if stage == "terminating two bumps" {
				meta["generation"] = 3
			}
			if stage == "terminating invalid timestamp" {
				meta["deletionTimestamp"] = ""
			}
			agentReads := 0
			f.change = func(action string, cmd *exec.Cmd) error {
				if action == "get" && slices.Contains(cmd.Args, "agents.core.orka.ai") {
					agentReads++
					if stage == "one bump disappears" && agentReads == 4 {
						delete(f.objects, key)
					}
				}
				return nil
			}
			duration := 2 * time.Second
			if stage == "one bump waits" {
				duration = 80 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), duration)
			defer cancel()
			result, err := a.cleanupSuiteNativeCandidate(ctx, r)
			if slices.Contains(f.deletes, key) {
				t.Fatal("duplicate DELETE of already-finalizing owned UID")
			}
			switch stage {
			case "one bump disappears":
				if err != nil || !result.AllGone || len(f.deletes) != 1 || agentReads < 4 {
					t.Fatalf("legitimate finalized generation bump refused: %+v %v deletes=%v reads=%d", result, err, f.deletes, agentReads)
				}
			case "one bump waits":
				if err == nil || result.AllGone || result.Reason != "cleanup budget exhausted" || len(f.deletes) != 0 || agentReads < 3 {
					t.Fatalf("finalizing UID was not polled: %+v %v reads=%d", result, err, agentReads)
				}
			default:
				if err == nil || result.AllGone || result.Reason != "root changed or read unknown" || len(f.deletes) != 0 {
					t.Fatalf("unsafe generation/spec mutation accepted: %+v %v", result, err)
				}
			}
		})
	}
}
