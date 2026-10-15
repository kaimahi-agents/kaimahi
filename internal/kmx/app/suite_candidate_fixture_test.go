package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"
	"time"
)

// Public operation fixture: distinct names/UIDs, exact raw DELETE preconditions,
// real Task/result-session transport and controller-owned children. Canonical
// fixtures remain independent; only external kubectl responses are overlaid.
type publicCandidateFixture struct {
	objects          map[string]map[string]any
	creates, deletes []string
	beforeDelete     func(context.Context, *exec.Cmd) error
	afterDelete      func(string)
	change           func(string, *exec.Cmd) error
	lists            int
	externalTasks    bool
	gets             []string
	afterCreate      func(string)
}

func publicNativeEvaluationFixture(t *testing.T) (*App, *nativeReadFixture, *evalFixture, *publicCandidateFixture) {
	t.Helper()
	a, _, reads := nativeFixture(t)
	f := newEvalFixture(t, nil)
	f.nativeReads = reads
	a.Out, a.Err = f.out, &bytes.Buffer{}
	state := &publicCandidateFixture{objects: map[string]map[string]any{}, externalTasks: true}
	capture := a.captureCommand
	read := capture
	capture = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "config") || slices.Contains(cmd.Args, "token") || slices.Contains(cmd.Args, "services") || slices.Contains(cmd.Args, "service") || slices.Contains(cmd.Args, orkaResultAccount) || slices.Contains(cmd.Args, "tasks.core.orka.ai") || slices.Contains(cmd.Args, "-f") {
			return cmd.Run()
		}
		return read(ctx, cmd)
	}
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		if state.change != nil {
			if err := state.change("get", cmd); err != nil {
				return err
			}
		}
		if slices.Contains(args, "delete") {
			if state.beforeDelete != nil {
				if err := state.beforeDelete(ctx, cmd); err != nil {
					return err
				}
			}
			i := slices.Index(args, "--raw")
			if i < 0 || !slices.Contains(args, "-f") {
				t.Fatal("not a raw delete", args)
			}
			parts := strings.Split(args[i+1], "/")
			kind := map[string]string{"providers": "Provider", "agents": "Agent", "tasks": "Task"}[parts[len(parts)-2]]
			key := kind + "/" + parts[len(parts)-1]
			var options struct {
				APIVersion, Kind, PropagationPolicy string
				Preconditions                       struct{ UID, ResourceVersion string }
				GracePeriodSeconds                  *int64
			}
			raw, _ := io.ReadAll(cmd.Stdin)
			doc := state.objects[key]
			if json.Unmarshal(raw, &options) != nil || options.APIVersion != "v1" || options.Kind != "DeleteOptions" || options.PropagationPolicy != "Foreground" || options.GracePeriodSeconds != nil || doc == nil || options.Preconditions.UID != nativeMetadata(doc)["uid"] || options.Preconditions.ResourceVersion != nativeMetadata(doc)["resourceVersion"] {
				t.Fatal("unsafe delete", key, string(raw))
			}
			state.deletes = append(state.deletes, key)
			delete(state.objects, key)
			// Controller GC removes the exact Task -> Job -> Pod and ConfigMap chain.
			removed := map[string]bool{options.Preconditions.UID: true}
			for changed := true; changed; {
				changed = false
				for key, child := range state.objects {
					owners, _ := nativeMetadata(child)["ownerReferences"].([]any)
					for _, value := range owners {
						owner := value.(map[string]any)
						if removed[owner["uid"].(string)] {
							removed[nativeMetadata(child)["uid"].(string)] = true
							delete(state.objects, key)
							changed = true
							break
						}
					}
				}
			}
			if state.afterDelete != nil {
				state.afterDelete(key)
			}
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Success"})
		}
		if i := slices.Index(args, "get"); i >= 0 && i+2 < len(args) {
			if args[i+1] == "--raw" {
				state.lists++
				path := args[i+2]
				kind := ""
				for plural, k := range map[string]string{"jobs": "Job", "pods": "Pod", "configmaps": "ConfigMap"} {
					if strings.Contains(path, "/namespaces/orka-system/"+plural+"?limit=128") {
						kind = k
					}
				}
				if kind == "" {
					t.Fatal("unknown/unbounded list", args)
				}
				items := []any{}
				for key, doc := range state.objects {
					if strings.HasPrefix(key, kind+"/") {
						items = append(items, doc)
					}
				}
				version := "v1"
				if kind == "Job" {
					version = "batch/v1"
				}
				return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": version, "kind": kind + "List", "metadata": map[string]any{"resourceVersion": "1"}, "items": items})
			}
			kind := map[string]string{"providers.core.orka.ai": "Provider", "agents.core.orka.ai": "Agent", "tasks.core.orka.ai": "Task"}[args[i+1]]
			name := args[i+2]
			if kind != "" && strings.HasPrefix(name, "kmx-eval-") {
				state.gets = append(state.gets, kind+"/"+name)
				doc := state.objects[kind+"/"+name]
				if doc == nil {
					if !slices.Contains(args, "--ignore-not-found=true") {
						return errors.New("not found")
					}
					return nil
				}
				oi := slices.Index(args, "-o")
				if args[oi+1] == "name" {
					_, err := fmt.Fprint(cmd.Stdout, kind+"/"+name)
					return err
				}
				if strings.HasPrefix(args[oi+1], "go-template=") {
					projection, err := template.New("poll").Parse(strings.TrimPrefix(args[oi+1], "go-template="))
					if err != nil {
						t.Fatal(err)
					}
					return projection.Execute(cmd.Stdout, doc)
				}
				if kind == "Task" && state.externalTasks {
					return capture(ctx, cmd)
				}
				return json.NewEncoder(cmd.Stdout).Encode(doc)
			}
		}
		if slices.Contains(args, "-f") {
			raw, _ := io.ReadAll(cmd.Stdin)
			cmd.Stdin = bytes.NewReader(raw)
			var doc map[string]any
			_ = json.Unmarshal(raw, &doc)
			kind, _ := doc["kind"].(string)
			if kind == "Provider" || kind == "Agent" || kind == "Task" {
				name := orkaObjectName(doc)
				key := kind + "/" + name
				if slices.Contains(args, "--dry-run=server") {
					return json.NewEncoder(cmd.Stdout).Encode(doc)
				}
				if !slices.Contains(args, "create") || !strings.HasPrefix(name, "kmx-eval-") {
					t.Fatal("public operation did not fresh-create candidate", args, key)
				}
				if state.objects[key] != nil {
					return errors.New("already exists")
				}
				meta := nativeMetadata(doc)
				meta["uid"], meta["generation"], meta["resourceVersion"] = fmt.Sprintf("uid-%d-%s", len(state.creates), name), 1, "1"
				if kind == "Task" {
					var response bytes.Buffer
					out := cmd.Stdout
					cmd.Stdout = &response
					err := capture(ctx, cmd)
					cmd.Stdout = out
					if err != nil {
						return err
					}
					_ = json.Unmarshal(response.Bytes(), &doc)
					nativeMetadata(doc)["resourceVersion"] = "1"
					writeFile(t, filepath.Join(f.dir, "task-"+name+".json"), string(mustJSON(t, doc)))
				} else {
					doc["status"] = map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}}
				}
				state.objects[key] = doc
				state.creates = append(state.creates, key)
				if kind == "Task" {
					uid := nativeMetadata(doc)["uid"].(string)
					childName := fmt.Sprintf("worker-%d", len(state.creates))
					job := cleanupChild("Job", childName+"-job", uid+"-job", "Task", name, uid)
					pod := cleanupChild("Pod", childName+"-pod", uid+"-pod", "Job", childName+"-job", uid+"-job")
					cm := cleanupChild("ConfigMap", childName+"-cm", uid+"-cm", "Task", name, uid)
					state.objects["Job/"+childName+"-job"], state.objects["Pod/"+childName+"-pod"], state.objects["ConfigMap/"+childName+"-cm"] = job, pod, cm
				}
				if state.afterCreate != nil {
					state.afterCreate(key)
				}
				return json.NewEncoder(cmd.Stdout).Encode(doc)
			}
		}
		return capture(ctx, cmd)
	}
	return a, f.nativeReads, f, state
}

func candidateCoreSetup(t *testing.T) (*App, *suiteNativePrepared, *suiteCandidateRun, *publicCandidateFixture, *nativeReadFixture, *evalFixture, *PreparedSuiteWorkspaceEvaluation) {
	t.Helper()
	a, reads, f, state := publicNativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	opt.ResultPort = f.opt.ResultPort
	prepared, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { prepared.Close() })
	native, err := a.prepareSuiteNativeCandidate(t.Context(), prepared.suite, OrkaNamespace, "inference", candidateRun)
	if err != nil {
		t.Fatal(err)
	}
	run, err := newSuiteCandidateRun(native, func(ctx context.Context) error { return workspace.CheckCurrent(ctx, prepared.root, prepared.prepared) })
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range prepared.inventory {
		f.answers[frame.Case.Input] = nativeCanary + " answer"
	}
	a.guarded = true
	return a, native, run, state, reads, f, prepared
}
func candidateCleanupSetup(t *testing.T) (*App, *suiteCandidateRun, *publicCandidateFixture, *nativeReadFixture) {
	t.Helper()
	a, p, r, f, reads, _, _ := candidateCoreSetup(t)
	f.externalTasks = false
	if _, err := a.placeSuiteNativeCandidate(t.Context(), p, r); err != nil {
		t.Fatal(err)
	}
	return a, r, f, reads
}
func cleanupContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func cleanupTask(t *testing.T, r *suiteCandidateRun, f *publicCandidateFixture) suiteCandidateResource {
	t.Helper()
	name := r.subject.PhysicalMember[:40] + "-eval-0123456789abcdef"
	doc := map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task", "metadata": map[string]any{"name": name, "namespace": "orka-system", "uid": "task-uid-123", "generation": 1, "resourceVersion": "2", "annotations": suiteNativeOwner(r.subject).markers()}, "spec": map[string]any{"type": "ai", "agentRef": map[string]any{"name": r.subject.PhysicalMember, "namespace": "orka-system"}, "prompt": nativeCanary}}
	raw := mustJSON(t, doc)
	digest, err := suiteCandidateSpecDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	resource := suiteCandidateResource{Kind: "Task", Name: name, UID: "task-uid-123", Generation: 1, SpecDigest: digest}
	if err := r.recordIntent(t.Context(), resource.Kind, resource.Name, digest); err != nil {
		t.Fatal(err)
	}
	if err := r.acknowledge(t.Context(), orkaIdentity{Kind: resource.Kind, Name: resource.Name, UID: resource.UID, Generation: 1}, raw); err != nil {
		t.Fatal(err)
	}
	f.objects["Task/"+resource.Name] = doc
	return resource
}

func cleanupChild(kind, name, uid, parentKind, parentName, parentUID string) map[string]any {
	version := "v1"
	parentVersion := "core.orka.ai/v1alpha1"
	if kind == "Job" {
		version = "batch/v1"
	}
	if parentKind == "Job" {
		parentVersion = "batch/v1"
	}
	return map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{"name": name, "namespace": "orka-system", "uid": uid, "resourceVersion": "3", "ownerReferences": []any{map[string]any{"apiVersion": parentVersion, "kind": parentKind, "name": parentName, "uid": parentUID, "controller": true}}}, "spec": map[string]any{"canary": nativeCanary}, "data": map[string]any{"canary": nativeCanary}}
}
