package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	suiteevidence "github.com/agent-primitives/suite/evidence"
)

// Only the external workload observation changes; all real readers and guards run.
func restrictedRuntimeDeployment(t *testing.T, d map[string]any, arch string) {
	t.Helper()
	_, p := restrictedProfileFixture(t)
	d["metadata"].(map[string]any)["labels"].(map[string]any)["helm.sh/chart"] = "orka-" + p.Install.Chart.Version
	c := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	controller, worker := p.Install.Images["controller"], p.Install.Images["ai-worker"]
	c["image"] = controller.Repository + "@" + controller.Platforms[arch].ManifestDigest
	args := c["args"].([]any)
	for i, arg := range args {
		if strings.HasPrefix(arg.(string), "--ai-worker-image=") {
			args[i] = "--ai-worker-image=" + worker.Repository + "@" + worker.Platforms[arch].ManifestDigest
		}
	}
}

func restrictedNativeFixture(t *testing.T, arch string) (*App, *nativeReadFixture) {
	t.Helper()
	a, _, f := nativeFixture(t)
	restrictedRuntimeDeployment(t, nativeController(f), arch)
	f.objects["serviceaccount/orka-ai-worker"]["metadata"].(map[string]any)["labels"].(map[string]any)["helm.sh/chart"] = "orka-0.0.0-dev"
	return a, f
}

func TestOfflineRuntimeRecognitionNative(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			a, f := restrictedNativeFixture(t, arch)
			runtime, err := a.readSuiteNativeRuntime(t.Context(), OrkaNamespace)
			if err != nil || runtime.WorkerAccountUID != "worker-uid" || runtime.Helm.Chart != "orka-0.0.0-dev" {
				t.Fatalf("restricted runtime not recognized: %+v %v", runtime, err)
			}
			f.change = func(key string, _ int, obj map[string]any) error {
				if key == "deploy/-o" {
					nativeController(f)["metadata"].(map[string]any)["generation"] = 2
				}
				return nil
			}
			if _, err := a.readSuiteNativeRuntime(t.Context(), OrkaNamespace); err == nil || !strings.Contains(err.Error(), "not Ready for its current generation") {
				t.Fatalf("changed controller: %v", err)
			}
		})
	}
}

func TestOfflineRuntimeRecognitionGuards(t *testing.T) {
	for _, change := range []string{"mixed worker architecture", "online worker", "online controller", "online chart", "old profile", "mode", "namespace", "controller account", "worker account", "entrypoint", "duplicate flag", "not ready", "duplicate controller", "worker identity"} {
		t.Run(change, func(t *testing.T) {
			a, f := restrictedNativeFixture(t, "amd64")
			d, c := nativeController(f), nativeContainer(f)
			spec := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			args := c["args"].([]any)
			replace := func(prefix, value string) {
				for i, arg := range args {
					if strings.HasPrefix(arg.(string), prefix) {
						args[i] = prefix + value
					}
				}
			}
			want := "unsupported native controller"
			switch change {
			case "mixed worker architecture":
				_, p := restrictedProfileFixture(t)
				worker := p.Install.Images["ai-worker"]
				replace("--ai-worker-image=", worker.Repository+"@"+worker.Platforms["arm64"].ManifestDigest)
			case "online worker":
				replace("--ai-worker-image=", "ghcr.io/orka-agents/orka/ai-worker@"+orkaAIWorkerDigest)
			case "online controller":
				c["image"] = "ghcr.io/orka-agents/orka@" + orkaControllerDigest
			case "online chart":
				d["metadata"].(map[string]any)["labels"].(map[string]any)["helm.sh/chart"] = "orka-0.3.0"
			case "old profile":
				var p offlineProfile
				if err := json.Unmarshal(offlineProfileJSON, &p); err != nil {
					t.Fatal(err)
				}
				controller, worker := p.Install.Images["controller"], p.Install.Images["ai-worker"]
				c["image"] = controller.Repository + "@" + controller.Platforms["amd64"].ManifestDigest
				replace("--ai-worker-image=", worker.Repository+"@"+worker.Platforms["amd64"].ManifestDigest)
			case "mode":
				replace("--controller-mode=", "all")
			case "namespace":
				replace("--watch-namespace=", "other")
			case "controller account":
				spec["serviceAccountName"] = "bad/account"
			case "worker account":
				replace("--ai-worker-service-account-name=", "bad/account")
			case "entrypoint":
				c["command"] = []any{"/other"}
				want = "pinned controller entrypoint"
			case "duplicate flag":
				c["args"] = append(args, "--controller-mode=harness-v2")
				want = "one explicit controller-mode flag"
			case "not ready":
				d["status"].(map[string]any)["readyReplicas"] = 0
				want = "not Ready for its current generation"
			case "duplicate controller":
				f.objects["deploy/-o"]["items"] = []any{d, d}
				want = "multiple Orka controller Deployments"
			case "worker identity":
				f.objects["serviceaccount/orka-ai-worker"]["metadata"].(map[string]any)["labels"].(map[string]any)["orka.ai/worker-trust"] = "general"
				want = "lacks worker-trust=ai label"
			}
			if _, err := a.readSuiteNativeRuntime(t.Context(), OrkaNamespace); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: wanted %q, got %v", change, want, err)
			}
		})
	}
}

func TestOfflineRuntimeRecognitionInventoryAndDiscovery(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			a, pages := targetFixture(t)
			d := pages["/apis/apps/v1/namespaces/orka-system/deployments?limit=64"].(map[string]any)["items"].([]any)[0].(map[string]any)
			restrictedRuntimeDeployment(t, d, arch)
			pages["/api/v1/namespaces/orka-system/serviceaccounts/orka-ai-worker"].(map[string]any)["metadata"].(map[string]any)["labels"].(map[string]any)["helm.sh/chart"] = "orka-0.0.0-dev"
			got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: OrkaNamespace})
			if err != nil || !got.Complete || got.RuntimeVersion != restrictedSourceSHA || got.CatalogCommit != restrictedSourceSHA {
				t.Fatalf("inventory attribution: %+v %v", got, err)
			}
			names := []string{}
			for _, row := range got.Tools {
				if row.Kind == "builtin" {
					names = append(names, row.Name)
				}
			}
			if !slices.Equal(names, []string{"web_search", "code_exec", "file_read", "web_fetch", "file_write", "request_approval", "reply_in_conversation"}) {
				t.Fatal(names)
			}
			discovery, err := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: OrkaNamespace})
			if err != nil || !discovery.Complete || discovery.Target.Runtime.State != "available" || discovery.Target.Runtime.Version != restrictedSourceSHA {
				t.Fatalf("discovery attribution: %+v %v", discovery, err)
			}
		})
	}
}

func TestOfflineRuntimeRecognitionConnect(t *testing.T) {
	_, _, native := nativeFixture(t)
	d := nativeController(native)
	a, objects, calls := modelLifecycleFixture(t)
	restrictedRuntimeDeployment(t, d, "amd64")
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "/apis/apps/v1/namespaces/orka-system/deployments?limit=64") {
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "apps/v1", "kind": "DeploymentList", "metadata": map[string]any{}, "items": []any{d}})
		}
		return capture(ctx, cmd)
	}
	opt := OrkaModelConnectOptions{OperationID: "11111111-1111-4111-8111-111111111111", Namespace: OrkaNamespace, Route: "copilot", Provider: "copilot", Model: "gpt-4.1", CredentialGeneration: "22222222-2222-4222-8222-222222222222"}
	result, err := a.ConnectOrkaModel(opt)
	if err != nil || result.Connection == nil || result.Connection.State != "model_ready" || len(objects) != 4 {
		t.Fatalf("connect: %+v %v cause=%v", result, err, errors.Unwrap(err))
	}
	beforeObjects, _ := json.Marshal(objects)
	before := len(*calls)
	again, err := a.ConnectOrkaModel(opt)
	if err != nil || again.Connection == nil || again.Connection.ID != result.Connection.ID {
		t.Fatalf("reconnect: %+v %v", again, err)
	}
	for _, call := range (*calls)[before:] {
		if strings.Contains(call, " create ") || strings.Contains(call, " delete ") {
			t.Fatal("reconnect mutated: " + call)
		}
	}
	// Another individually approved tuple must not authorize reuse of this connection.
	restrictedRuntimeDeployment(t, d, "arm64")
	before = len(*calls)
	_, err = a.ConnectOrkaModel(opt)
	var modelErr *OrkaModelError
	if !errors.As(err, &modelErr) || modelErr.Code() != "execution_unknown" || modelErr.cause == nil || !strings.Contains(modelErr.cause.Error(), "retained model connection requires explicit disconnect") {
		t.Fatalf("changed runtime: expected retained-connection refusal, got %v cause=%v", err, errors.Unwrap(err))
	}
	afterObjects, _ := json.Marshal(objects)
	if !reflect.DeepEqual(beforeObjects, afterObjects) {
		t.Fatal("changed runtime mutated connection")
	}
	for _, call := range (*calls)[before:] {
		if strings.Contains(call, " create ") || strings.Contains(call, " delete ") {
			t.Fatal("changed runtime mutated: " + call)
		}
	}
}

func TestOfflineRuntimeRecognitionPublicExport(t *testing.T) {
	for _, pin := range []struct{ arch, controller, worker string }{
		{"amd64", "sha256:2b137a6210c1ba044f6569d2374169330645a44f730634517d675b30db94ed48", "sha256:2fdd6db33fb5111a3959029e39d1a6447453d6f254d414906d5cce0ba0d11a51"},
		{"arm64", "sha256:43f2b547e12497d92bb67486a587b0d5312558d1cf5f67811c61ab74f0fc3036", "sha256:eb1e0591f3545ed26f3da1d61e6968c1f5b40bb2b6e39483fafc7916158c148f"},
	} {
		t.Run(pin.arch, func(t *testing.T) {
			root, prepared, suite, placement, observations := passingFixture(t)
			a, _ := restrictedNativeFixture(t, pin.arch)
			native, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
			if err != nil {
				t.Fatal(err)
			}
			placement.Subject = native.Subject
			receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
			if err != nil {
				t.Fatalf("offline receipt rejected: %v", err)
			}
			t.Setenv("KMX_HOME", privateSuiteDir(t))
			store, err := openSuitePassStore(t.Context(), root, filepath.Join(os.Getenv("KMX_HOME"), "suites"), suite.Digest)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			publicCommitFixture(t, store, suite, raw)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "evidence.json")
			if err := (&App{}).ExportSuiteEvaluation(t.Context(), SuiteEvaluationExportOptions{Workspace: root, Member: suite.MemberID, Out: out}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var evidence suiteevidence.EvaluationEvidence
			if err := suitecore.Unmarshal(data, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.Execution.Runtime != (suitecore.EvidenceImplementation{ID: "orka", Version: restrictedSourceSHA, Digest: pin.controller}) || evidence.Execution.Harness != (suitecore.EvidenceImplementation{ID: "orka", Version: "harness-v2", Digest: pin.worker}) {
				t.Fatal("untruthful runtime evidence", evidence.Execution)
			}
			// Existing export performs shared VerifyEvaluation schema/expectation validation.
			t.Logf("schema-validated export: %s controller=%s worker=%s", pin.arch, pin.controller, pin.worker)
		})
	}
}

// Failure of the trusted embedded profile must not affect the independent online tuple.
func TestOfflineRuntimeRecognitionEmbeddedFailure(t *testing.T) {
	a, _, _ := nativeFixture(t)
	offline, _ := restrictedNativeFixture(t, "amd64")
	saved := offlineRestrictedProfileJSON
	offlineRestrictedProfileJSON = []byte(`{`)
	t.Cleanup(func() { offlineRestrictedProfileJSON = saved })
	if _, err := offline.readSuiteNativeRuntime(t.Context(), OrkaNamespace); err == nil {
		t.Fatal("malformed profile accepted")
	}
	if _, err := a.readSuiteNativeRuntime(t.Context(), OrkaNamespace); err != nil {
		t.Fatal(fmt.Errorf("online depends on offline profile: %w", err))
	}
}
