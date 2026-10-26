package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func enrollmentOrkaChart(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	return candidateOrkaArchive(t, func(files map[string]string) {
		files["orka/Chart.yaml"] += "dependencies:\n  - name: guardd\n    alias: enforcement\n    version: 0.1.0\n"
		files["orka/charts/guardd.tgz"] = "outer-verified bundled dependency fixture"
		values := candidateOrkaValues(t)
		values["controller"].(map[string]any)["watchNamespace"] = "qualification-tasks"
		values["enforcement"] = map[string]any{"enabled": false, "image": "localhost/guardd@sha256:" + strings.Repeat("b", 64)}
		if mutate != nil {
			mutate(values)
		}
		raw, err := yaml.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		files["orka/values.yaml"] = string(raw)
	})
}

func TestOrkaEnrollmentFreshInstallDelegatesExplicitSetting(t *testing.T) {
	for _, setting := range []string{"omitted", "true", "false"} {
		t.Run(setting, func(t *testing.T) {
			chart := enrollmentOrkaChart(t, nil)
			f := newOrkaFixture(t, nil)
			t.Setenv("KMX_TEST_NAMESPACE_JSON", `{"kind":"Namespace","metadata":{"name":"qualification-tasks","uid":"tasks-1"}}`)
			opt := candidateOrkaOptions(t, chart)
			if setting != "omitted" {
				enabled := setting == "true"
				opt.Enforcement = &enabled
			}
			if err := f.app.OrkaInstall(opt); err != nil {
				t.Fatal(err)
			}
			desired := "false"
			if setting == "true" {
				desired = "true"
			}
			if !strings.Contains(f.calls(t), "enforcement.enabled="+desired) ||
				!strings.Contains(f.calls(t), "--wait-for-jobs") ||
				!strings.Contains(f.calls(t), "enforcement.image=localhost/guardd@sha256:") {
				t.Fatal("fresh installer did not delegate the selected enrollment and pinned artifact to Helm")
			}
		})
	}
}

func TestOrkaEnrollmentPackagedDependencyLayouts(t *testing.T) {
	for _, layout := range []string{"generator", "versioned-archive", "helm-expanded", "missing", "ambiguous"} {
		t.Run(layout, func(t *testing.T) {
			chart := candidateOrkaArchive(t, func(files map[string]string) {
				files["orka/Chart.yaml"] += "dependencies:\n  - name: guardd\n    alias: enforcement\n    version: 0.1.0\n"
				switch layout {
				case "generator", "ambiguous":
					files["orka/charts/guardd.tgz"] = "verified dependency fixture"
				case "versioned-archive":
					files["orka/charts/guardd-0.1.0.tgz"] = "verified dependency fixture"
				}
				if layout == "helm-expanded" || layout == "ambiguous" {
					files["orka/charts/guardd/Chart.yaml"] = "apiVersion: v2\nname: guardd\nversion: 0.1.0\n"
					files["orka/charts/guardd/values.yaml"] = "enabled: false\n"
				}
			})
			spec, err := localOrkaInstallSpec(chart, digestOf(chart))
			valid := layout != "missing" && layout != "ambiguous"
			if (err == nil) != valid || (valid && !spec.enforcementBundled) {
				t.Fatalf("packaged dependency recognition: bundled=%t err=%v", spec.enforcementBundled, err)
			}
		})
	}
}

func TestOrkaEnrollmentRequiresExplicitSupportedInput(t *testing.T) {
	for _, name := range []string{"unbundled", "colocated", "missing-image", "invalid-image", "implicit-enabled"} {
		t.Run(name, func(t *testing.T) {
			chart := enrollmentOrkaChart(t, func(values map[string]any) {
				switch name {
				case "colocated":
					values["controller"].(map[string]any)["watchNamespace"] = OrkaNamespace
				case "missing-image":
					values["enforcement"].(map[string]any)["image"] = ""
				case "invalid-image":
					values["enforcement"].(map[string]any)["image"] = "localhost/guardd,other=bad"
				case "implicit-enabled":
					values["enforcement"].(map[string]any)["enabled"] = true
				}
			})
			if name == "unbundled" {
				chart = candidateOrkaArchive(t, nil)
			}
			f := newOrkaFixture(t, nil)
			opt := candidateOrkaOptions(t, chart)
			enabled := true
			if name != "implicit-enabled" {
				opt.Enforcement = &enabled
			}
			opt.NoApply = true
			if err := f.app.OrkaInstall(opt); err == nil || f.writes(t) != "" {
				t.Fatal("invalid or implicit enrollment was accepted or wrote state")
			}
		})
	}
}

func TestOrkaEnrollmentRepeatPreservesOrExplicitlyChangesOnlyEnrollment(t *testing.T) {
	for _, name := range []string{"omit-enabled", "same-enabled", "enable", "disable", "changed-image", "failed-change"} {
		t.Run(name, func(t *testing.T) {
			chart := enrollmentOrkaChart(t, nil)
			f := newOrkaFixture(t, nil)
			spec, err := localOrkaInstallSpec(chart, digestOf(chart))
			if err != nil {
				t.Fatal(err)
			}
			current, desired := true, true
			if name == "enable" {
				current = false
			}
			if name == "disable" {
				desired = false
			}
			opt := candidateOrkaOptions(t, chart)
			if name != "omit-enabled" {
				opt.Enforcement = &desired
			}
			values := candidateOrkaValues(t)
			values["controller"].(map[string]any)["watchNamespace"] = spec.taskNamespace
			values["kmxInstallerSHA256"] = digestOf(chart)
			values["enforcement"] = map[string]any{
				"enabled": current, "image": spec.enforcementImage,
				"existingInstallationUID": "retained-installation", "stateDirectory": "/retained/provider-state",
			}
			if name == "changed-image" {
				values["enforcement"].(map[string]any)["image"] = "localhost/other@sha256:" + strings.Repeat("c", 64)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KMX_TEST_HELM_VALUES", string(raw))
			values["enforcement"].(map[string]any)["enabled"] = desired
			raw, err = json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KMX_TEST_HELM_UPGRADED_VALUES", string(raw))
			t.Setenv("KMX_TEST_VALUES_AFTER", filepath.Join(f.dir, "upgraded-values.json"))
			t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.3.0-local.1","app_version":"v0.3.0-local.1","status":"deployed"}]`)
			t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, candidateOrkaController(spec.controllerImage(), spec.taskNamespace)))
			if name == "failed-change" {
				opt.Enforcement = new(bool)
				t.Setenv("KMX_TEST_HELM_UPGRADE", "failed")
			}
			spec.enrollment = opt.Enforcement
			err = f.app.applyOrkaChartFor(chart, spec)
			if name == "changed-image" || name == "failed-change" {
				if err == nil {
					t.Fatal("changed authority or failed enrollment accepted")
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := f.calls(t)
			changed := name == "enable" || name == "disable"
			if strings.Contains(calls, "helm upgrade ") != changed {
				t.Fatalf("unexpected enrollment mutation: %s", calls)
			}
			if changed {
				if !strings.Contains(calls, "--reuse-values --wait --wait-for-jobs") ||
					strings.Contains(calls, "helm install ") || strings.Contains(f.applied(t), "CustomResourceDefinition") {
					t.Fatal("enrollment changed more than the existing owned release setting")
				}
				after, err := os.ReadFile(os.Getenv("KMX_TEST_VALUES_AFTER"))
				if err != nil || !strings.Contains(string(after), "retained-installation") || !strings.Contains(string(after), "/retained/provider-state") {
					t.Fatal("retained installation identity was lost")
				}
			}
		})
	}
}
