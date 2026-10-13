package app

import (
	"slices"
	"testing"
)

func TestApplyReleaseLiftPromotionAndDeclarationDrift(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "promote", true: "declaration drift"}[drift], func(t *testing.T) {
			a, f, opt, dir := releaseApplyFixture(t, "both")
			nativeMetadata(f.objects["namespace/kube-system"])["uid"] = "production-cluster"
			nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "production-namespace"
			provider := f.objects["providers.core.orka.ai/inference"]
			nativeMetadata(provider)["uid"] = "production-inference"
			provider["spec"].(map[string]any)["defaultModel"] = "test-model"
			opt.Promote = true
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			if p.Summary().Gate != "promotion" {
				t.Fatal("promotion relationship not exercised")
			}
			if drift {
				provider["spec"].(map[string]any)["defaultModel"] = "other-model"
			}
			result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
			if drift {
				if err == nil || result.State != "not-applied" {
					t.Fatalf("declaration change accepted: %+v %v", result, err)
				}
				for _, call := range orkaCalls(t, dir) {
					if slices.Contains(call.Args, "create") {
						t.Fatal("declaration drift entered admission/write")
					}
				}
				return
			}
			if err != nil || result.State != "ready" {
				t.Fatalf("promotion apply: %+v %v", result, err)
			}
			if len(persistentOrkaCalls(t, dir)) != 2 || result.Resources[0].Outcome != "created" || result.Resources[1].Outcome != "created" {
				t.Fatal("promotion did not create both")
			}
			if err := a.VerifyReleaseLiftPlacement(t.Context(), result); err != nil {
				t.Fatal(err)
			}
			if _, err := a.PrepareReleaseLift(t.Context(), opt); err == nil {
				t.Fatal("promotion result silently authorized future reuse")
			}
		})
	}
}
