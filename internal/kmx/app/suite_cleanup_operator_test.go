package app

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPublicCandidateStartupReportsWithoutEffects(t *testing.T) {
	for _, tc := range []string{"empty", "unknown", "unanchored", "unrelated run"} {
		t.Run(tc, func(t *testing.T) {
			a, native, _, f, _, answers, p := candidateCoreSetup(t)
			if tc == "unanchored" {
				f.objects["Job/lost-job"] = cleanupChild("Job", "lost-job", "lost-uid", "Task", "lost-task", "lost-task-uid")
			}
			if tc == "unrelated run" {
				doc := nativeOwnedDoc(t, native, "Provider")
				addOrkaMarker(doc, suiteNativeOwner(native.Subject).markers())
				f.objects["Provider/"+native.Subject.PhysicalMember] = doc
			}
			capture := a.captureCommand
			failed := false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if tc == "unknown" && !failed && slices.Contains(cmd.Args, "--raw") && slices.Contains(cmd.Args, "get") {
					failed = true
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
						t.Fatal("startup discovery lacks bounded window")
					}
					return errors.New(nativeCanary)
				}
				return capture(ctx, cmd)
			}
			f.afterCreate = func(string) {
				if len(f.creates) == 1 && (len(f.deletes) != 0 || f.lists < 6 && tc != "unknown") {
					t.Fatal("startup omitted inventory or deleted", f.lists, f.deletes)
				}
			}
			for _, frame := range p.inventory {
				answers.answers[frame.Case.Input] = nativeCanary + " answer"
			}
			if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
				t.Fatal("startup blocked evaluation", err)
			}
			diag := a.Err.(*bytes.Buffer).String()
			if tc != "empty" && !strings.Contains(diag, "WARNING") {
				t.Fatal("missing startup warning", diag)
			}
			if strings.Contains(diag, nativeCanary) || !strings.Contains(diag, "kmx eval cleanup") {
				t.Fatal("unsafe or absent recovery metadata", diag)
			}
		})
	}
}

func TestOperatorCleanupListingAndConsent(t *testing.T) {
	for _, tc := range []string{"list", "no confirmation", "wrong confirmation", "confirmed", "already guarded"} {
		t.Run(tc, func(t *testing.T) {
			a, r, f, _ := candidateCleanupSetup(t)
			cleanupTask(t, r, f)
			a.guarded = tc == "already guarded"
			a.Cfg.Confirm = ""
			if tc == "confirmed" || tc == "already guarded" {
				a.Cfg.Confirm = a.Cfg.KubeContext
			}
			if tc == "wrong confirmation" {
				a.Cfg.Confirm = "wrong"
			}
			runID := candidateRun
			if tc == "list" {
				runID = ""
			}
			a.Run.Context = t.Context()
			err := a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace, RunID: runID})
			if a.Run.Context != t.Context() {
				t.Fatal("cleanup rebound caller Runner")
			}
			if tc == "confirmed" || tc == "already guarded" {
				if err != nil || len(f.deletes) != 3 || len(f.objects) != 0 {
					t.Fatal(err, f.deletes)
				}
			} else if len(f.deletes) != 0 || tc != "list" && err == nil || tc == "list" && err != nil {
				t.Fatal(err, f.deletes)
			}
			report := a.Err.(*bytes.Buffer).String()
			if tc == "list" {
				report = a.Out.(*bytes.Buffer).String()
				if strings.Contains(a.Err.(*bytes.Buffer).String(), "about to:") {
					t.Fatal("listing prompted")
				}
			}
			if !strings.Contains(report, candidateRun) {
				t.Fatal("missing safe run identity")
			}
		})
	}
}

func TestOperatorCleanupTerminatingAndConflictingGroups(t *testing.T) {
	for _, tc := range []string{"foreground gen+1", "same-run markers", "same-run suite", "retained child chain", "missing run marker", "unmarked candidate"} {
		t.Run(tc, func(t *testing.T) {
			a, r, f, _ := candidateCleanupSetup(t)
			cleanupTask(t, r, f)
			a.Cfg.Confirm = a.Cfg.KubeContext
			if tc == "missing run marker" {
				delete(nativeMetadata(f.objects["Agent/"+r.subject.PhysicalMember])["annotations"].(map[string]any), suiteCandidateRunMarker)
			}
			if tc == "unmarked candidate" {
				nativeMetadata(f.objects["Agent/"+r.subject.PhysicalMember])["annotations"] = map[string]any{}
			}
			if tc == "same-run markers" {
				nativeMetadata(f.objects["Agent/"+r.subject.PhysicalMember])["annotations"].(map[string]any)[orkaSuiteMarkers[4]] = strings.Repeat("a", 64)
			}
			if tc == "same-run suite" {
				owner := *suiteNativeOwner(r.subject)
				owner.Digest = "sha256:" + strings.Repeat("b", 64)
				physical, _ := suiteCandidatePhysicalName(owner.Digest, candidateRun)
				f.objects["Provider/"+physical] = map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": map[string]any{"name": physical, "namespace": OrkaNamespace, "uid": "extra-provider", "generation": 1, "resourceVersion": "1", "annotations": owner.markers()}, "spec": map[string]any{"type": "openai"}}
			}
			if tc == "retained child chain" {
				var task suiteCandidateResource
				for _, resource := range r.Resources() {
					if resource.Kind == "Task" {
						task = resource
					}
				}
				job := cleanupChild("Job", "retained-job", "retained-job-uid", "Task", task.Name, task.UID)
				pod := cleanupChild("Pod", "retained-pod", "retained-pod-uid", "Job", "retained-job", "retained-job-uid")
				f.objects["Job/retained-job"], f.objects["Pod/retained-pod"] = job, pod
				gone, polls := false, 0
				f.afterDelete = func(key string) {
					if strings.HasPrefix(key, "Task/") {
						gone = true
						f.objects["Job/retained-job"], f.objects["Pod/retained-pod"] = job, pod
					}
				}
				f.change = func(_ string, cmd *exec.Cmd) error {
					if gone && slices.Contains(cmd.Args, "--raw") && slices.Contains(cmd.Args, "get") {
						polls++
						if polls == 10 {
							delete(f.objects, "Job/retained-job")
						}
						if polls == 20 {
							delete(f.objects, "Pod/retained-pod")
						}
					}
					return nil
				}
			}
			if tc == "foreground gen+1" {
				var deleting map[string]any
				polls := map[string]int{}
				f.beforeDelete = func(_ context.Context, cmd *exec.Cmd) error {
					path := cmd.Args[slices.Index(cmd.Args, "--raw")+1]
					parts := strings.Split(path, "/")
					kind := map[string]string{"providers": "Provider", "agents": "Agent", "tasks": "Task"}[parts[len(parts)-2]]
					deleting = f.objects[kind+"/"+parts[len(parts)-1]]
					return nil
				}
				f.afterDelete = func(key string) {
					nativeMetadata(deleting)["generation"] = 2
					nativeMetadata(deleting)["deletionTimestamp"] = "2026-10-15T00:00:00Z"
					f.objects[key] = deleting
				}
				f.change = func(_ string, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "--ignore-not-found=true") {
						i := slices.Index(cmd.Args, "-o")
						if i >= 0 && strings.HasPrefix(cmd.Args[i+1], "go-template=") {
							name := cmd.Args[slices.Index(cmd.Args, "get")+2]
							kind := map[string]string{"providers.core.orka.ai": "Provider", "agents.core.orka.ai": "Agent", "tasks.core.orka.ai": "Task"}[cmd.Args[slices.Index(cmd.Args, "get")+1]]
							key := kind + "/" + name
							polls[key]++
							if polls[key] == 2 {
								delete(f.objects, key)
							}
						}
					}
					return nil
				}
			}
			err := a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace, RunID: candidateRun})
			if tc == "foreground gen+1" || tc == "retained child chain" {
				if err != nil || len(f.deletes) != 3 {
					t.Fatal(err, f.deletes)
				}
			} else if !errors.Is(err, errSuiteCandidateCleanup) || len(f.deletes) != 0 {
				t.Fatal("conflicting run groups authorized", err, f.deletes)
			}
		})
	}
}

func TestOperatorCleanupRefusesDriftAndLostAnchors(t *testing.T) {
	for _, tc := range []string{"uid", "spec", "markers", "scope", "cluster scope", "resource version", "child replacement", "new root", "final new root", "final replacement", "frozen config", "generation", "terminating gen+2", "no Task", "missing Job", "no roots"} {
		t.Run(tc, func(t *testing.T) {
			a, r, f, reads := candidateCleanupSetup(t)
			task := cleanupTask(t, r, f)
			job := cleanupChild("Job", "worker-job", "job-uid", "Task", task.Name, task.UID)
			pod := cleanupChild("Pod", "worker-pod", "pod-uid", "Job", "worker-job", "job-uid")
			f.objects["Job/worker-job"], f.objects["Pod/worker-pod"] = job, pod
			a.Cfg.Confirm = a.Cfg.KubeContext
			if tc == "no Task" {
				delete(f.objects, "Task/"+task.Name)
			}
			if tc == "missing Job" {
				delete(f.objects, "Job/worker-job")
			}
			if tc == "no roots" {
				clear(f.objects)
			}
			f.afterDelete = func(key string) {
				if !strings.HasPrefix(key, "Provider/") {
					return
				}
				if tc == "final new root" || tc == "final replacement" {
					name := "extra-task"
					if tc == "final replacement" {
						name = task.Name
					}
					f.objects["Task/"+name] = map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task", "metadata": map[string]any{"name": name, "namespace": OrkaNamespace, "uid": "unconfirmed-task", "resourceVersion": "1", "generation": 1, "annotations": suiteNativeOwner(r.subject).markers()}, "spec": map[string]any{"type": "ai", "agentRef": map[string]any{"name": r.subject.PhysicalMember, "namespace": OrkaNamespace}}}
				}
			}
			capture := a.captureCommand
			changed := false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if !changed && slices.Contains(cmd.Args, "config") {
					changed = true
					doc := f.objects["Agent/"+r.subject.PhysicalMember]
					if doc != nil {
						switch tc {
						case "uid":
							nativeMetadata(doc)["uid"] = "replacement-uid"
						case "spec":
							doc["spec"].(map[string]any)["model"] = "changed"
						case "markers":
							nativeMetadata(doc)["annotations"].(map[string]any)[suiteCandidateRunMarker] = strings.Repeat("b", 32)
						case "generation":
							nativeMetadata(doc)["generation"] = 2
						case "terminating gen+2":
							nativeMetadata(doc)["generation"] = 3
							nativeMetadata(doc)["deletionTimestamp"] = "2026-10-15T00:00:00Z"
						case "resource version":
							nativeMetadata(doc)["resourceVersion"] = "fresh-rv"
						case "child replacement":
							nativeMetadata(job)["uid"] = "replacement-child-uid"
							nativeMetadata(pod)["ownerReferences"].([]any)[0].(map[string]any)["uid"] = "replacement-child-uid"
						case "new root":
							extra := cleanupChild("Task", "extra-task", "extra-uid", "", "", "")
							extra["apiVersion"] = "core.orka.ai/v1alpha1"
							nativeMetadata(extra)["ownerReferences"] = []any{}
							nativeMetadata(extra)["annotations"] = suiteNativeOwner(r.subject).markers()
							extra["spec"] = f.objects["Task/"+task.Name]["spec"]
							f.objects["Task/extra-task"] = extra
						}
					}
					if tc == "frozen config" {
						a.Cfg.KubeContext = "changed-caller-context"
					}
					if tc == "scope" {
						nativeMetadata(reads.objects["namespace/orka-system"])["uid"] = "new-namespace-uid"
					}
					if tc == "cluster scope" {
						nativeMetadata(reads.objects["namespace/kube-system"])["uid"] = "new-cluster-uid"
					}
				}
				return capture(ctx, cmd)
			}
			err := a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace, RunID: candidateRun})
			reasons := map[string]string{"no roots": "no roots for selected run", "no Task": "uncertain candidate inventory at Job/worker-job", "missing Job": "uncertain candidate inventory at Pod/worker-pod", "child replacement": "child closure unknown"}
			if reason := reasons[tc]; reason != "" && (err == nil || !strings.Contains(err.Error(), reason)) {
				t.Fatalf("cleanup lost safe refusal reason %q: %v", reason, err)
			}
			if err != nil && strings.Contains(err.Error(), nativeCanary) {
				t.Fatal("cleanup leaked API payload", err)
			}
			if tc == "resource version" || tc == "frozen config" {
				if err != nil || len(f.deletes) != 3 {
					t.Fatal("fresh RV must be used", err, f.deletes)
				}
			} else if tc == "final new root" || tc == "final replacement" {
				if !errors.Is(err, errSuiteCandidateCleanup) || len(f.deletes) != 3 {
					t.Fatal("unconfirmed root deleted or absence claimed", err, f.deletes)
				}
			} else if !errors.Is(err, errSuiteCandidateCleanup) || len(f.deletes) != 0 {
				t.Fatal("unsafe cleanup", err, f.deletes)
			}
		})
	}
}
