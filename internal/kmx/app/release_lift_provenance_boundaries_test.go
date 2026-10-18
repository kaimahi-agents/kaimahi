package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestReleaseLiftProvenanceRejectsStrippedMetadata(t *testing.T) {
	for _, runtime := range []string{"orka", "kagent"} {
		for _, phase := range []string{"admission", "create"} {
			t.Run(runtime+"/"+phase, func(t *testing.T) {
				var a *App
				var opt ReleaseLiftOptions
				if runtime == "orka" {
					a, _, opt, _ = releaseApplyFixture(t, "both")
				} else {
					a, opt, _, _ = releaseKagentFixture(t)
				}
				t.Setenv("KMX_HOME", t.TempDir())
				p, err := a.PrepareReleaseLift(t.Context(), opt)
				if err != nil {
					t.Fatal(err)
				}
				capture, stripped := a.captureCommand, 0
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if !slices.Contains(cmd.Args, "create") || (phase == "admission") != slices.Contains(cmd.Args, "--dry-run=server") {
						return capture(ctx, cmd)
					}
					out := cmd.Stdout
					var buffer bytes.Buffer
					cmd.Stdout = &buffer
					err := capture(ctx, cmd)
					cmd.Stdout = out
					if err != nil {
						return err
					}
					var doc map[string]any
					raw := buffer.Bytes()
					if json.Unmarshal(raw, &doc) == nil {
						meta, _ := doc["metadata"].(map[string]any)
						ann, _ := meta["annotations"].(map[string]any)
						if _, ok := ann["kaimahi.dev/lift-record"]; ok {
							delete(ann, "kaimahi.dev/lift-record")
							stripped++
							raw, _ = json.Marshal(doc)
						}
					}
					_, err = out.Write(raw)
					return err
				}
				result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
				if stripped == 0 {
					t.Fatal("boundary did not exercise stamped metadata")
				}
				if err == nil || result.State == "ready" {
					t.Fatalf("stripped metadata granted success: %s %v", result.State, err)
				}
				if phase == "create" && result.Resources[0].UID == "" {
					t.Fatal("lost acknowledged partial identity")
				}
			})
		}
	}
}

func TestReleaseLiftProvenanceCheckpointFailureStopsNextCreate(t *testing.T) {
	a, _, opt, dir := releaseApplyFixture(t, "both")
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	injected := false
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		pairCreate := nativePairCreate(cmd)
		err := capture(ctx, cmd)
		if err == nil && pairCreate && !slices.Contains(cmd.Args, "--dry-run=server") {
			entries, e := os.ReadDir(filepath.Join(home, "release-lifts"))
			if e != nil {
				return e
			}
			for _, entry := range entries {
				if entry.IsDir() {
					injected = true
					return os.WriteFile(filepath.Join(home, "release-lifts", entry.Name(), "1.json"), []byte("occupied"), 0600)
				}
			}
		}
		return err
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if !injected || !errors.Is(err, errReleaseLiftProvenance) || result.State != "partial" || result.ProvenanceState != "unknown" || result.Resources[0].UID == "" || result.Resources[1].Outcome != "not-attempted" {
		t.Fatalf("checkpoint failure lost partial facts: %+v %v", result, err)
	}
	if len(persistentOrkaCalls(t, dir)) != 1 {
		t.Fatal("persistence failure allowed another create/retry")
	}
}

func TestWorkspaceLiftProvenanceStorageFailureBeforeExecution(t *testing.T) {
	a, opt, candidates, _, _, registryWrites := workspaceLiftFixture(t)
	if err := os.MkdirAll(os.Getenv("KMX_HOME"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("KMX_HOME"), "release-lifts"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	result, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || result.Eval.Status != "not-attempted" || len(candidates.creates) != 0 || *registryWrites != 0 {
		t.Fatalf("storage refusal followed execution/publication: %+v %v", result, err)
	}
}

func TestWorkspaceLiftProvenanceSkipDoesNotOpenStore(t *testing.T) {
	a, opt, _, _, _, _ := workspaceLiftFixture(t)
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.LiftWorkspace(t.Context(), p)
	closeErr := p.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("cold: %v %v", err, closeErr)
	}
	// An invalid provenance store must not affect existing GET-only skip authority.
	if err := os.Chmod(filepath.Join(os.Getenv("KMX_HOME"), "release-lifts"), 0777); err != nil {
		t.Fatal(err)
	}
	p, err = PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	result, err := a.LiftWorkspace(t.Context(), p)
	if err != nil || result.Lift.Status != "skipped" {
		t.Fatalf("skip opened/repaired provenance store: %+v %v", result, err)
	}
}
