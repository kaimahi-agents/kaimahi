package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func candidateOrkaValues(t *testing.T) map[string]any {
	t.Helper()
	var values map[string]any
	if err := json.Unmarshal([]byte(pinnedOrkaHelmValues(t, nil)), &values); err != nil {
		t.Fatal(err)
	}
	values["controller"].(map[string]any)["image"] = map[string]any{
		"repository": "localhost/candidate/controller", "digest": "sha256:" + strings.Repeat("a", 64),
	}
	values["controller"].(map[string]any)["watchNamespace"] = OrkaNamespace
	workers := values["workers"].(map[string]any)
	for _, name := range []string{"ai", "general"} {
		workers[name].(map[string]any)["image"].(map[string]any)["repository"] = "localhost/candidate/" + name
	}
	values["publisher"].(map[string]any)["image"].(map[string]any)["repository"] = "localhost/candidate/publisher"
	return values
}

func candidateOrkaController(image, namespace string) map[string]any {
	controller := orkaDeployWithImage("chart-controller", image)
	metadata := controller["metadata"].(map[string]any)
	metadata["namespace"], metadata["uid"] = OrkaNamespace, "controller-1"
	spec := controller["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["containers"].([]any)[0].(map[string]any)["args"] = []string{"--watch-namespace=" + namespace}
	return controller
}

func candidateOrkaArchive(t *testing.T, mutate func(map[string]string)) []byte {
	t.Helper()
	defaults, err := yaml.Marshal(candidateOrkaValues(t))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"orka/Chart.yaml":                   "apiVersion: v2\nname: orka\nversion: 0.3.0-local.1\nappVersion: v0.3.0-local.1\n",
		"orka/values.yaml":                  string(defaults),
		"orka/crds/tasks.core.orka.ai.yaml": "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: tasks.core.orka.ai\n",
	}
	if mutate != nil {
		mutate(files)
	}
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := []byte(files[name])
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func candidateOrkaOptions(t *testing.T, chart []byte) OrkaOptions {
	t.Helper()
	name := filepath.Join(t.TempDir(), "orka.tgz")
	if err := os.WriteFile(name, chart, 0o644); err != nil {
		t.Fatal(err)
	}
	return OrkaOptions{Chart: name, ChartSHA256: digestOf(chart), Provider: "-"}
}

func TestOrkaLocalChartNoApplyUsesOnlyVerifiedLocalBytes(t *testing.T) {
	chart := candidateOrkaArchive(t, nil)
	f := newOrkaFixture(t, nil)
	f.app.orkaInstaller = "http://127.0.0.1:1/must-not-fetch"
	opt := candidateOrkaOptions(t, chart)
	opt.NoApply = true
	if err := f.app.OrkaInstall(opt); err != nil {
		t.Fatal(err)
	}
	if writes := f.writes(t); writes != "" {
		t.Fatalf("no-apply wrote resources: %s", writes)
	}
}

func TestOrkaLocalChartRefusesInvalidInputBeforeWrites(t *testing.T) {
	chart := candidateOrkaArchive(t, nil)
	for _, name := range []string{"missing digest", "wrong digest", "missing chart", "oversize", "symlink"} {
		t.Run(name, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			opt := candidateOrkaOptions(t, chart)
			switch name {
			case "missing digest":
				opt.ChartSHA256 = ""
			case "wrong digest":
				opt.ChartSHA256 = strings.Repeat("0", 64)
			case "missing chart":
				opt.Chart = ""
			case "oversize":
				if err := os.WriteFile(opt.Chart, make([]byte, (8<<20)+1), 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filepath.Join(filepath.Dir(opt.Chart), "link.tgz")
				if err := os.Symlink(opt.Chart, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				opt.Chart = link
			}
			if err := f.app.OrkaInstall(opt); err == nil {
				t.Fatal("invalid local chart accepted")
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("refused input wrote resources: %s", writes)
			}
		})
	}
}

func TestOrkaLocalChartRefusesUnpinnedOrAmbiguousDefaults(t *testing.T) {
	for _, name := range []string{"missing metadata", "missing defaults", "wrong name", "duplicate metadata key", "tagged controller", "tagged ACP", "helm value injection", "remote dependency", "outside tree", "invalid Task namespace"} {
		t.Run(name, func(t *testing.T) {
			chart := candidateOrkaArchive(t, func(files map[string]string) {
				switch name {
				case "missing metadata":
					delete(files, "orka/Chart.yaml")
				case "missing defaults":
					delete(files, "orka/values.yaml")
				case "wrong name":
					files["orka/Chart.yaml"] = strings.ReplaceAll(files["orka/Chart.yaml"], "name: orka", "name: other")
				case "duplicate metadata key":
					files["orka/Chart.yaml"] += "version: 0.3.1\n"
				case "tagged controller":
					files["orka/values.yaml"] = strings.ReplaceAll(files["orka/values.yaml"], "sha256:"+strings.Repeat("a", 64), "latest")
				case "tagged ACP":
					files["orka/values.yaml"] = strings.ReplaceAll(files["orka/values.yaml"], orkaOpencodeImage, "localhost/opencode:latest")
				case "helm value injection":
					files["orka/values.yaml"] = strings.ReplaceAll(files["orka/values.yaml"], "localhost/candidate/controller", "localhost/controller,controller.mode=other")
				case "remote dependency":
					files["orka/Chart.yaml"] += "dependencies:\n  - name: guardd\n    repository: https://example.invalid\n"
				case "outside tree":
					files["../escape"] = "not a chart member"
				case "invalid Task namespace":
					files["orka/values.yaml"] = strings.ReplaceAll(files["orka/values.yaml"], "watchNamespace: orka-system", "watchNamespace: '*'")
				}
			})
			f := newOrkaFixture(t, nil)
			if err := f.app.OrkaInstall(candidateOrkaOptions(t, chart)); err == nil {
				t.Fatal("invalid candidate accepted")
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("invalid candidate wrote resources: %s", writes)
			}
		})
	}
}

func TestOrkaLocalChartInstallsCandidatePinsAndArchiveIdentity(t *testing.T) {
	chart := candidateOrkaArchive(t, nil)
	f := newOrkaFixture(t, nil)
	opt := candidateOrkaOptions(t, chart)
	if err := f.app.OrkaInstall(opt); err != nil {
		t.Fatal(err)
	}
	calls := f.calls(t)
	if !strings.Contains(calls, "controller.image.repository=localhost/candidate/controller") ||
		!strings.Contains(calls, "controller.image.digest=sha256:"+strings.Repeat("a", 64)) ||
		!strings.Contains(calls, "kmxInstallerSHA256="+digestOf(chart)) {
		t.Fatalf("candidate identity/pins were not passed to Helm: %s", calls)
	}
	if !strings.Contains(f.applied(t), "name: tasks.core.orka.ai") {
		t.Fatal("candidate CRDs were not applied")
	}
}

func TestOrkaLocalChartDryRunDoesNotInstall(t *testing.T) {
	chart := candidateOrkaArchive(t, nil)
	f := newOrkaFixture(t, nil)
	opt := candidateOrkaOptions(t, chart)
	opt.DryRun = true
	if err := f.app.OrkaInstall(opt); err != nil {
		t.Fatal(err)
	}
	if writes := f.writes(t); writes != "" {
		t.Fatalf("dry-run wrote resources: %s", writes)
	}
	validated, err := os.ReadFile(f.stdin + ".validated")
	if err != nil || !strings.Contains(string(validated), "name: tasks.core.orka.ai") {
		t.Fatalf("candidate CRDs were not server-validated: %v", err)
	}
}

func TestOrkaLocalChartRepeatKeepsChartAndData(t *testing.T) {
	chart := candidateOrkaArchive(t, nil)
	f := newOrkaFixture(t, nil)
	values := candidateOrkaValues(t)
	values["kmxInstallerSHA256"] = digestOf(chart)
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_TEST_HELM_VALUES", string(raw))
	t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.3.0-local.1","app_version":"v0.3.0-local.1","status":"deployed"}]`)
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, candidateOrkaController(
		"localhost/candidate/controller@sha256:"+strings.Repeat("a", 64), OrkaNamespace)))
	if err := f.app.OrkaInstall(candidateOrkaOptions(t, chart)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.calls(t), "helm install ") || strings.Contains(f.applied(t), "kind: CustomResourceDefinition") {
		t.Fatal("exact candidate repeat rewrote the chart or CRDs")
	}
	if !strings.Contains(f.applied(t), "kind: ServiceAccount") {
		t.Fatal("repeat did not preserve ordinary result-reader setup")
	}
}

func TestOrkaLocalChartRepeatRequiresExactOwnedArchiveAndImages(t *testing.T) {
	chart := candidateOrkaArchive(t, nil)
	spec, err := localOrkaInstallSpec(chart, digestOf(chart))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"same", "different archive", "changed repository", "changed digest", "changed namespace", "live drift", "live watch drift", "missing UID", "terminating", "failed", "foreign", "default pin"} {
		t.Run(name, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			values := candidateOrkaValues(t)
			values["kmxInstallerSHA256"] = digestOf(chart)
			status := "deployed"
			image := spec.controllerImage()
			watchNamespace := spec.taskNamespace
			expected := spec
			switch name {
			case "different archive":
				values["kmxInstallerSHA256"] = strings.Repeat("b", 64)
			case "changed repository":
				values["workers"].(map[string]any)["ai"].(map[string]any)["image"].(map[string]any)["repository"] = "localhost/other"
			case "changed digest":
				values["controller"].(map[string]any)["image"].(map[string]any)["digest"] = orkaControllerDigest
			case "changed namespace":
				values["controller"].(map[string]any)["watchNamespace"] = "other-tasks"
			case "live drift":
				image = "localhost/other@sha256:" + strings.Repeat("c", 64)
			case "live watch drift":
				watchNamespace = "other-tasks"
			case "failed":
				status = "failed"
			case "foreign":
				delete(values, "labels")
			case "default pin":
				expected = publishedOrkaInstallSpec()
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KMX_TEST_HELM_VALUES", string(raw))
			t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.3.0-local.1","app_version":"v0.3.0-local.1","status":"`+status+`"}]`)
			controller := candidateOrkaController(image, watchNamespace)
			if name == "missing UID" {
				delete(controller["metadata"].(map[string]any), "uid")
			}
			if name == "terminating" {
				controller["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-25T19:00:00Z"
			}
			t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, controller))
			known, err := f.app.orkaInstallStateFor(expected)
			if name == "same" {
				if err != nil || !known {
					t.Fatalf("exact candidate not recognized: %v, %v", known, err)
				}
			} else if err == nil || known {
				t.Fatalf("mismatched candidate accepted: %v, %v", known, err)
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("ownership inspection wrote resources: %s", writes)
			}
		})
	}
}
