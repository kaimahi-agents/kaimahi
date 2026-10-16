package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Removing create-time metadata must break this test without affecting reuse.
func TestReleaseLiftProvenanceCreatesOnly(t *testing.T) {
	for _, absent := range []string{"", "Provider", "Agent", "both"} {
		t.Run(absent, func(t *testing.T) {
			a, _, opt, dir := releaseApplyFixture(t, absent)
			home := t.TempDir()
			t.Setenv("KMX_HOME", home)
			a.now = func() time.Time { return time.Date(2026, 10, 16, 3, 4, 5, 0, time.FixedZone("test", 3600)) }
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			before := p.Summary()
			result, err := a.ApplyReleaseLift(t.Context(), p, before.PlanDigest)
			if err != nil || result.State != "ready" {
				t.Fatalf("apply: %+v %v", result, err)
			}
			if !reflect.DeepEqual(before, p.Summary()) {
				t.Fatal("provenance changed reviewed plan")
			}
			var stamp string
			for _, call := range persistentOrkaCalls(t, dir) {
				ann := call.Document["metadata"].(map[string]any)["annotations"].(map[string]any)
				raw, _ := ann["kaimahi.dev/lift-record"].(string)
				if raw == "" {
					t.Fatal("new object has no cluster-readable Lift record")
				}
				if stamp != "" && stamp != raw {
					t.Fatal("created pair received different Lift records")
				}
				stamp = raw
				var record map[string]any
				if json.Unmarshal([]byte(raw), &record) != nil {
					t.Fatal("invalid Lift annotation")
				}
				if record["version"] != float64(1) || record["suiteDigest"] != before.SuiteDigest || record["receiptDigest"] != before.EvidenceDigest || record["receiptRunID"] != before.EvidenceRunID || record["attemptedAt"] != "2026-10-16T02:04:05Z" || record["gate"] != "same-subject" || record["evaluatedOnTarget"] != true {
					t.Fatalf("wrong provenance: %s", raw)
				}
				if _, actor := record["actor"]; actor {
					t.Fatal("invented actor")
				}
				source, _ := record["evaluation"].(map[string]any)
				if source["clusterUID"] != before.SourceTarget.ClusterUID || source["namespaceUID"] != before.SourceTarget.NamespaceUID {
					t.Fatal("source environment missing")
				}
				if strings.Contains(raw, nativeCanary) || strings.Contains(raw, "systemPrompt") || strings.Contains(raw, opt.Evidence) {
					t.Fatal("public annotation leaked private content/path")
				}
			}
			for _, kind := range []string{"Provider", "Agent"} {
				if absent == "both" || absent == kind {
					continue
				}
				got, err := a.orkaCapture(t.Context(), nil, "-n", before.Target.Namespace, "get", orkaPlural(kind), before.Member, "-o", "json")
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(got), "kaimahi.dev/lift-record") {
					t.Fatal("reused object was stamped")
				}
			}
			if absent == "" {
				entries, err := os.ReadDir(home)
				if err != nil || len(entries) != 0 {
					t.Fatalf("all-reuse wrote private state: %v %v", entries, err)
				}
			}
		})
	}
}

// A storage preflight failure must not follow a native create or admission.
func TestReleaseLiftProvenanceUnsafeStateBeforeEffects(t *testing.T) {
	a, _, opt, dir := releaseApplyFixture(t, "both")
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", bad)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err == nil || result.State != "not-applied" {
		t.Fatalf("unsafe state accepted: %+v %v", result, err)
	}
	for _, call := range orkaCalls(t, dir) {
		if slices.Contains(call.Args, "--dry-run=server") || slices.Contains(call.Args, "create") {
			t.Fatal("storage failure followed native effect")
		}
	}
}

func TestReleaseLiftProvenanceReadbackTampering(t *testing.T) {
	a, _, opt, dir := releaseApplyFixture(t, "both")
	t.Setenv("KMX_HOME", t.TempDir())
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if !slices.Contains(cmd.Args, "get") {
			return capture(ctx, cmd)
		}
		// Preserve the real capture writer; its bounded buffer is not bytes.Buffer.
		out := cmd.Stdout
		var buffer bytes.Buffer
		cmd.Stdout = &buffer
		err := capture(ctx, cmd)
		cmd.Stdout = out
		if err != nil {
			return err
		}
		raw := buffer.Bytes()
		var doc map[string]any
		if json.Unmarshal(raw, &doc) == nil {
			if meta, ok := doc["metadata"].(map[string]any); ok {
				if ann, ok := meta["annotations"].(map[string]any); ok {
					delete(ann, "kaimahi.dev/lift-record")
					raw, _ = json.Marshal(doc)
				}
			}
		}
		_, err = out.Write(raw)
		return err
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err == nil || result.State == "ready" {
		t.Fatalf("missing readback provenance granted success: %+v %v", result, err)
	}
	if len(persistentOrkaCalls(t, dir)) != 1 || result.Resources[1].Outcome != "not-attempted" {
		t.Fatal("Provider provenance drift allowed Agent creation")
	}
}
