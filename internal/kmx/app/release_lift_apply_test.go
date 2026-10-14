package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func releaseApplyFixture(t *testing.T, absent string) (*App, *nativeReadFixture, ReleaseLiftOptions, string) {
	t.Helper()
	a, f, opt, native := releaseLiftFixture(t)
	dir := os.Getenv("KMX_RECONCILE_DIR")
	t.Setenv("KMX_RECONCILE_UID_SUFFIX", "-new")
	for _, kind := range []string{"Provider", "Agent"} {
		key := orkaPlural(kind) + "/" + native.Subject.Member
		if absent != kind && absent != "both" {
			seedReconcile(t, dir, f.objects[key])
		}
		delete(f.objects, key)
	}
	reads := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		if i := slices.Index(args, "get"); i >= 0 && (args[i+1] == "crd" || f.objects[args[i+1]+"/"+args[i+2]] != nil) {
			return reads(ctx, cmd)
		}
		return reconcileCaptureFixture(ctx, cmd)
	}
	return a, f, opt, dir
}

func TestApplyReleaseLiftCreateOrExactReuse(t *testing.T) {
	for _, absent := range []string{"", "Provider", "Agent", "both"} {
		t.Run(absent, func(t *testing.T) {
			a, _, opt, dir := releaseApplyFixture(t, absent)
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
			if err != nil {
				t.Fatal(err)
			}
			if result.State != "ready" || len(result.Resources) != 2 {
				t.Fatalf("incomplete placement: %+v", result)
			}
			for _, r := range result.Resources {
				want := "reused"
				if absent == "both" || absent == r.Kind {
					want = "created"
				}
				if r.Outcome != want || !r.Ready || r.UID == "" || r.SpecDigest == "" {
					t.Fatalf("wrong outcome: %+v", r)
				}
			}
			if err := a.VerifyReleaseLiftPlacement(t.Context(), result); err != nil {
				t.Fatal(err)
			}
			writes := persistentOrkaCalls(t, dir)
			want := 0
			if absent == "both" {
				want = 2
			} else if absent != "" {
				want = 1
			}
			if len(writes) != want {
				t.Fatalf("unexpected writes: %+v", writes)
			}
			for _, call := range writes {
				if !slices.Contains(call.Args, "create") || call.Document["kind"] == "Secret" {
					t.Fatal("not create-only")
				}
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), nativeCanary) || strings.Contains(string(raw), "systemPrompt") {
				t.Fatal("result leaked payload")
			}
			if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err == nil {
				t.Fatal("plan applied twice")
			}
		})
	}
}

func TestApplyReleaseLiftDenialBeforeAdmission(t *testing.T) {
	for _, tc := range []string{"digest", "subject drift", "guard denial", "guard drift", "evidence drift"} {
		t.Run(tc, func(t *testing.T) {
			a, f, opt, dir := releaseApplyFixture(t, "both")
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			digest := p.Summary().PlanDigest
			switch tc {
			case "digest":
				digest = "sha256:" + strings.Repeat("0", 64)
			case "subject drift":
				nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "changed"
			case "evidence drift":
				if err := os.WriteFile(opt.Evidence, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "guard denial", "guard drift":
				capture := a.captureCommand
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "config") && tc == "guard denial" {
						return errors.New("guard metadata unavailable")
					}
					err := capture(ctx, cmd)
					if err == nil && slices.Contains(cmd.Args, "config") {
						nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "changed"
					}
					return err
				}
			}
			result, err := a.ApplyReleaseLift(t.Context(), p, digest)
			if err == nil || result.State != "not-applied" {
				t.Fatalf("denial not truthful: %+v %v", result, err)
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("denial wrote")
			}
			for _, call := range orkaCalls(t, dir) {
				if slices.Contains(call.Args, "--dry-run=server") {
					t.Fatal("admission before gate/guard")
				}
			}
		})
	}
}

func TestApplyReleaseLiftPartialFailureDoesNotRetry(t *testing.T) {
	a, _, opt, dir := releaseApplyFixture(t, "both")
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_RECONCILE_FAIL_ONCE", "Agent")
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err == nil || result.State != "partial" || result.Resources[0].Outcome != "created" || !result.Resources[0].Ready || result.Resources[1].Outcome != "unknown" {
		t.Fatalf("partial lost: %+v %v", result, err)
	}
	if len(persistentOrkaCalls(t, dir)) != 2 {
		t.Fatal("write resent")
	}
	if _, err := a.PrepareReleaseLift(t.Context(), opt); err == nil {
		t.Fatal("recreation reused old evaluation UID")
	}
}
