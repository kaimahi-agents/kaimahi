package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlowBindingFileBoundaries(t *testing.T) {
	for _, mode := range []string{"oversize", "symlink", "decoded-key", "binding-gate", "wrong-suite"} {
		t.Run(mode, func(t *testing.T) {
			a, inv, f, opt := flowBindingFixture(t)
			raw := readFlowFile(t, opt.Bindings)
			switch mode {
			case "oversize":
				raw += strings.Repeat(" ", 64<<10)
			case "symlink":
				target := filepath.Join(t.TempDir(), "binding.yaml")
				writeFile(t, target, raw)
				if err := os.Remove(opt.Bindings); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, opt.Bindings); err != nil {
					t.Skip(err)
				}
			case "decoded-key":
				raw = strings.Replace(raw, "runtime: orka", "runtime: \"orka\\n"+strings.Repeat("a1b2", 8)+"\"", 1)
			case "binding-gate":
				raw = strings.Replace(raw, "steps: []", "steps: [{flow: release, step: review}]", 1)
			case "wrong-suite":
				raw = strings.Replace(raw, inv.Digest, "sha256:"+strings.Repeat("f", 64), 1)
			}
			if mode != "symlink" {
				writeFile(t, opt.Bindings, raw)
			}
			if _, err := a.prepareSuiteFlow(t.Context(), inv, opt); err == nil || !errors.Is(err, errSuiteFlow) || f.calls != 0 {
				t.Fatal("invalid file reached live reads", err, f.calls)
			}
		})
	}
}

func TestFlowBindingRuntimeChurnAndReplacement(t *testing.T) {
	a, inv, f, opt := flowBindingFixture(t)
	s, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	deploy := f.objects["deploy/-o"]["items"].([]any)[0].(map[string]any)
	nativeMetadata(deploy)["generation"] = float64(2)
	deploy["spec"].(map[string]any)["replicas"] = float64(2)
	status := deploy["status"].(map[string]any)
	for _, key := range []string{"observedGeneration", "replicas", "updatedReplicas", "readyReplicas", "availableReplicas"} {
		status[key] = float64(2)
	}
	if s.revalidate(t.Context(), a) == nil {
		t.Fatal("runtime freshness churn not fenced")
	}
	next, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	if next.bindingDigest != s.bindingDigest {
		t.Fatal("scale/restart churn changed semantic B")
	}
	nativeMetadata(deploy)["uid"] = "new-controller"
	next, err = a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	if next.bindingDigest == s.bindingDigest {
		t.Fatal("runtime replacement absent from B")
	}
}

func TestFlowBindingRejectsSplicedMemberSnapshot(t *testing.T) {
	a, inv, f, opt := flowBindingFixture(t)
	hits := 0
	f.change = func(key string, _ int, obj map[string]any) error {
		if key == "providers.core.orka.ai/inference" {
			hits++
			if hits == 3 {
				nativeMetadata(obj)["resourceVersion"] = "2"
			}
		}
		return nil
	}
	if _, err := a.prepareSuiteFlow(t.Context(), inv, opt); err == nil {
		t.Fatal("mixed-time member snapshots accepted")
	}
}
