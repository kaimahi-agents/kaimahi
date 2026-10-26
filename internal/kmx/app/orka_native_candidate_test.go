package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestOrkaNativeCandidateWorkerUsesWatchedTaskNamespace(t *testing.T) {
	for _, name := range []string{"matching", "management account only", "watch mismatch", "foreign trust"} {
		t.Run(name, func(t *testing.T) {
			a, _, f := nativeFixture(t)
			const namespace = "candidate-tasks"
			controller := nativeController(f)
			args := controller["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["args"].([]any)
			for i, arg := range args {
				if strings.HasPrefix(arg.(string), "--watch-namespace=") && name != "watch mismatch" {
					args[i] = "--watch-namespace=" + namespace
				}
			}
			metadata := nativeMetadata(f.objects["serviceaccount/orka-ai-worker"])
			if name != "management account only" {
				metadata["namespace"] = namespace
			}
			if name == "foreign trust" {
				metadata["labels"].(map[string]any)["orka.ai/worker-trust"] = "other"
			}
			original := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "serviceaccounts") {
					i := slices.Index(cmd.Args, "-n")
					if i < 0 || cmd.Args[i+1] != namespace {
						t.Fatal("worker inspected in the management namespace")
					}
				}
				return original(ctx, cmd)
			}
			worker, err := a.orkaAIWorkerAccountIn(t.Context(), namespace)
			if name == "matching" {
				if err != nil || worker != "orka-ai-worker" {
					t.Fatalf("watched Task worker refused: %q %v", worker, err)
				}
			} else if err == nil {
				t.Fatal("wrong-namespace, unwatched or untrusted worker accepted")
			}
		})
	}
}

func nativeCandidateFixture(t *testing.T) (orkaDeployment, orkaInstallSpec) {
	t.Helper()
	chart := candidateOrkaArchive(t, nil)
	spec, err := localOrkaInstallSpec(chart, digestOf(chart))
	if err != nil {
		t.Fatal(err)
	}
	_, _, f := nativeFixture(t)
	raw, err := json.Marshal(nativeController(f))
	if err != nil {
		t.Fatal(err)
	}
	var controller orkaDeployment
	if err := json.Unmarshal(raw, &controller); err != nil {
		t.Fatal(err)
	}
	controller.Metadata.Labels["helm.sh/chart"] = "orka-" + spec.chartVersion
	container := &controller.Spec.Template.Spec.Containers[0]
	container.Image = spec.controllerImage()
	for i, arg := range container.Args {
		if strings.HasPrefix(arg, "--ai-worker-image=") {
			container.Args[i] = "--ai-worker-image=" + spec.images["workers.ai.image.repository"] + "@" + spec.images["workers.ai.image.digest"]
		}
	}
	return controller, spec
}

func TestOrkaNativeCandidateControllerRequiresExactSelection(t *testing.T) {
	for _, name := range []string{"matching", "no selection", "controller drift", "worker drift", "chart drift", "watch drift", "entrypoint drift", "wrong destination"} {
		t.Run(name, func(t *testing.T) {
			controller, spec := nativeCandidateFixture(t)
			selected := &spec
			namespace := spec.taskNamespace
			container := &controller.Spec.Template.Spec.Containers[0]
			switch name {
			case "no selection":
				selected = nil
			case "controller drift":
				container.Image = "localhost/other/controller@sha256:" + strings.Repeat("b", 64)
			case "worker drift":
				container.Args = append(container.Args, "--ai-worker-image=localhost/other/worker@sha256:"+strings.Repeat("b", 64))
			case "chart drift":
				controller.Metadata.Labels["helm.sh/chart"] = "orka-0.3.0-local.other"
			case "watch drift":
				container.Args = append(container.Args, "--watch-namespace=other")
			case "entrypoint drift":
				container.Command = []string{"/bin/sh"}
			case "wrong destination":
				namespace = "other"
			}
			runtime, err := suiteNativeControllerRuntimeFor(controller, namespace, selected)
			if name != "matching" {
				if err == nil {
					t.Fatal("unselected or changed runtime accepted")
				}
				return
			}
			if err != nil || !suiteNativePinnedRuntime(runtime, namespace) || runtime.CandidateChartDigest != "sha256:"+spec.archiveDigest || runtime.CandidateVersion != strings.TrimPrefix(spec.appVersion, "v") {
				t.Fatalf("exact selected candidate refused or misidentified: %#v, %v", runtime, err)
			}
		})
	}
}

func TestOrkaNativeCandidateProofSeparatesArchivesAndExportsActualImages(t *testing.T) {
	controller, spec := nativeCandidateFixture(t)
	runtime, err := suiteNativeControllerRuntimeFor(controller, spec.taskNamespace, &spec)
	if err != nil {
		t.Fatal(err)
	}
	other := runtime
	other.CandidateChartDigest = "sha256:" + strings.Repeat("b", 64)
	if sameReleaseRuntimeProfile(runtime, other) {
		t.Fatal("different selected archives treated as the same runtime")
	}
	r := &suitePassingReceipt{ExecutionMode: "native-orka", Subject: suiteNativeSubject{Runtime: runtime, Target: suiteNativeTarget{Namespace: spec.taskNamespace}}}
	exported, harness, err := suitePublicRuntime(r)
	if err != nil || exported.Version != runtime.CandidateVersion || !strings.HasSuffix(spec.controllerImage(), "@"+exported.Digest) || harness.Digest != spec.images["workers.ai.image.digest"] {
		t.Fatalf("candidate evidence mislabeled: %#v %#v %v", exported, harness, err)
	}
	r.Subject.Runtime.CandidateVersion = ""
	if _, _, err := suitePublicRuntime(r); err == nil {
		t.Fatal("incomplete candidate identity exported")
	}
	_, _, _, pinned := nativePrepared(t)
	raw, err := json.Marshal(pinned.Subject.Runtime)
	if err != nil || strings.Contains(string(raw), "Candidate") {
		t.Fatalf("default receipt acquired candidate fields: %s %v", raw, err)
	}
}

func TestOrkaNativeCandidateSelectionRequiresExactOwnedInstallation(t *testing.T) {
	for _, name := range []string{"matching", "archive changed", "namespace changed", "not installed", "missing pair"} {
		t.Run(name, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			chart := candidateOrkaArchive(t, nil)
			opt := candidateOrkaOptions(t, chart)
			spec, err := localOrkaInstallSpec(chart, digestOf(chart))
			if err != nil {
				t.Fatal(err)
			}
			values := candidateOrkaValues(t)
			values["kmxInstallerSHA256"] = digestOf(chart)
			if name == "archive changed" {
				values["kmxInstallerSHA256"] = strings.Repeat("b", 64)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KMX_TEST_HELM_VALUES", string(raw))
			t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.3.0-local.1","app_version":"v0.3.0-local.1","status":"deployed"}]`)
			t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, candidateOrkaController(spec.controllerImage(), spec.taskNamespace)))
			namespace := spec.taskNamespace
			switch name {
			case "namespace changed":
				namespace = "other"
			case "not installed":
				t.Setenv("KMX_TEST_HELM_LIST", "[]")
			case "missing pair":
				opt.ChartSHA256 = ""
			}
			err = f.app.selectOrkaNativeCandidate(t.Context(), opt.Chart, opt.ChartSHA256, namespace)
			if name == "matching" {
				if err != nil || f.app.orkaNativeCandidate == nil || f.app.orkaNativeCandidate.archiveDigest != digestOf(chart) {
					t.Fatalf("exact installation selection failed: %v", err)
				}
			} else if err == nil || f.app.orkaNativeCandidate != nil {
				t.Fatal("unowned, missing, or mismatched selection accepted")
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("runtime selection mutated installation: %s", writes)
			}
		})
	}
}
