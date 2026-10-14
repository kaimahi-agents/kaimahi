package app

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func mustReleaseSuite(t *testing.T, opt ReleaseLiftOptions) *workspace.NativeSuite {
	t.Helper()
	_, digest, _ := strings.Cut(opt.Reference, "@")
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), opt.FromLayout, digest)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.LoadArtifact(t.Context(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	return suite
}

func TestReleaseLiftExplicitPromotionGate(t *testing.T) {
	for _, tc := range []string{"same subject", "same with flag", "changed no flag", "promote", "missing model", "different model", "model case", "different type", "different runtime", "model observation drift", "existing destination"} {
		t.Run(tc, func(t *testing.T) {
			a, f, opt, source := releaseLiftFixture(t)
			selected := f.objects["providers.core.orka.ai/inference"]
			if tc != "same subject" && tc != "same with flag" {
				f.objects["namespace/kube-system"]["metadata"].(map[string]any)["uid"] = "production-cluster"
				f.objects["namespace/orka-system"]["metadata"].(map[string]any)["uid"] = "production-namespace"
				selected["metadata"].(map[string]any)["uid"] = "production-inference"
				selected["spec"].(map[string]any)["defaultModel"] = source.Subject.Model
				for _, kind := range []string{"Provider", "Agent"} {
					delete(f.objects, orkaPlural(kind)+"/"+source.Subject.Member)
				}
			}
			opt.Promote = tc != "same subject" && tc != "changed no flag"
			switch tc {
			case "missing model":
				delete(selected["spec"].(map[string]any), "defaultModel")
			case "different model":
				selected["spec"].(map[string]any)["defaultModel"] = "another-model"
			case "model case":
				selected["spec"].(map[string]any)["defaultModel"] = strings.ToUpper(source.Subject.Model)
			case "different type":
				selected["spec"].(map[string]any)["type"] = "azure-openai"
			case "different runtime":
				nativeContainer(f)["image"] = "another-runtime"
			case "model observation drift":
				reads := a.captureCommand
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					err := reads(ctx, cmd)
					if err == nil && strings.Contains(strings.Join(cmd.Args, " "), "DefaultModel") {
						selected["metadata"].(map[string]any)["resourceVersion"] = "2"
					}
					return err
				}
			case "existing destination":
				destination, err := a.prepareSuiteNative(t.Context(), mustReleaseSuite(t, opt), OrkaNamespace, "inference")
				if err != nil {
					t.Fatal(err)
				}
				f.objects[orkaPlural("Agent")+"/"+source.Subject.Member] = nativeOwnedDoc(t, destination, "Agent")
			}
			plan, err := a.PrepareReleaseLift(t.Context(), opt)
			success := tc == "same subject" || tc == "same with flag" || tc == "promote"
			if !success {
				if !errors.Is(err, ErrReleaseLift) {
					t.Fatalf("promotion incorrectly allowed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s := plan.Summary()
			want := "same-subject"
			if tc == "promote" {
				want = "promotion"
			}
			if s.Gate != want || s.SourceTarget != releaseLiftTarget(source.Subject.Target) || s.Promote != opt.Promote {
				t.Fatalf("wrong promotion identities: %+v", s)
			}
			if tc == "promote" && s.ServingModel != source.Subject.Model {
				t.Fatal("serving declaration not bound to review")
			}
			if !reflect.DeepEqual(plan.receipt.Subject, source.Subject) {
				t.Fatal("promotion rewrote source evidence")
			}
		})
	}
}
