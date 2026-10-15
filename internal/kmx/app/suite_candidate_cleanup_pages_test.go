package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestSuiteCandidateCleanupBoundedRawInventory(t *testing.T) {
	for _, stage := range []string{"two pages", "page cap", "object cap", "byte cap", "snapshot drift", "repeat token", "null items", "wrong list kind", "wrong namespace", "missing UID"} {
		t.Run(stage, func(t *testing.T) {
			a, r, f, _ := candidateCleanupSetup(t)
			task := cleanupTask(t, r, f)
			capture := a.captureCommand
			pageCalls := 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := cmd.Args
				ri := slices.Index(args, "--raw")
				if slices.Contains(args, "get") && ri >= 0 && strings.HasPrefix(args[ri+1], "/apis/batch/v1/namespaces/orka-system/jobs?") {
					pageCalls++
					if pageCalls > 4 && stage == "page cap" {
						t.Fatal("read beyond page cap")
					}
					path := args[ri+1]
					if len(f.deletes) > 0 {
						return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "batch/v1", "kind": "JobList", "metadata": map[string]any{"resourceVersion": "9"}, "items": []any{}})
					}
					if stage == "byte cap" {
						_, err := fmt.Fprint(cmd.Stdout, `{"apiVersion":"batch/v1","kind":"JobList","metadata":{"resourceVersion":"1"},"items":[],"opaque":"`+strings.Repeat("x", 4<<20)+`"}`)
						return err
					}
					token := "opaque/?&=+ token"
					rv := "1"
					items := []any{}
					next := token
					if stage == "two pages" && strings.Contains(path, "&continue=") {
						if path != "/apis/batch/v1/namespaces/orka-system/jobs?limit=128&continue=opaque%2F%3F%26%3D%2B+token" {
							t.Fatalf("opaque token not escaped: %s", path)
						}
						items = append(items, cleanupChild("Job", "task-job", "job-page-uid", "Task", task.Name, task.UID))
						next = ""
					}
					if stage == "page cap" {
						next = fmt.Sprintf("token-%d", pageCalls)
					}
					if stage == "snapshot drift" && pageCalls > 1 {
						rv = "changed"
						next = ""
					}
					if stage == "object cap" {
						for i := 0; i < 129; i++ {
							items = append(items, cleanupChild("Job", fmt.Sprintf("job-%d", i), fmt.Sprintf("uid-%d", i), "Task", task.Name, task.UID))
						}
						next = ""
					}
					if stage == "wrong namespace" || stage == "missing UID" {
						doc := cleanupChild("Job", "task-job", "job-page-uid", "Task", task.Name, task.UID)
						if stage == "wrong namespace" {
							nativeMetadata(doc)["namespace"] = "foreign"
						} else {
							delete(nativeMetadata(doc), "uid")
						}
						items = append(items, doc)
						next = ""
					}
					var values any = items
					if stage == "null items" {
						values = nil
					}
					kind := "JobList"
					if stage == "wrong list kind" {
						kind = "PodList"
					}
					return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "batch/v1", "kind": kind, "metadata": map[string]any{"resourceVersion": rv, "continue": next}, "items": values})
				}
				return capture(ctx, cmd)
			}
			result, err := a.cleanupSuiteNativeCandidate(cleanupContext(t), r)
			if stage == "two pages" {
				if err != nil || !result.AllGone || len(f.deletes) != 3 {
					t.Fatalf("complete paginated closure %+v %v", result, err)
				}
				found := false
				for _, x := range r.Resources() {
					found = found || x.UID == "job-page-uid"
				}
				if !found {
					t.Fatal("paginated owned Job not retained")
				}
				// Only known identity and ownership hash can survive the raw server page.
				if strings.Contains(string(mustJSON(t, r.Resources())), nativeCanary) {
					t.Fatal("payload retained")
				}
			} else if err == nil || result.AllGone || len(f.deletes) != 0 {
				t.Fatalf("incomplete enumeration used %+v %v deletes=%v pages=%d", result, err, f.deletes, pageCalls)
			}
		})
	}
}
