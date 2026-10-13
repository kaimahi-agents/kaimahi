package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"text/template"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"go.yaml.in/yaml/v3"
)

const nativeCanary = "private-content-canary-never-captured"

type nativeReadFixture struct {
	objects map[string]map[string]any
	calls   int
	change  func(string, int, map[string]any) error
}

func nativeFixture(t *testing.T, profiles ...workspace.RuntimeProfile) (*App, *workspace.NativeSuite, *nativeReadFixture) {
	t.Helper()
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "  Answer briefly.\n\nKeep whitespace.\n\n")
	root := filepath.Join(t.TempDir(), "workspace")
	opt := workspace.CreateOptions{Name: "greeter", Model: "packaged-model", Description: "Public description", InstructionsFile: input}
	if len(profiles) > 0 {
		opt.Runtime = profiles[0]
	}
	if err := workspace.Create(t.Context(), root, opt); err != nil {
		t.Fatal(err)
	}
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	adapter, _, _ := reconcileFixtureMode(t, true)
	a := adapter.app
	f := &nativeReadFixture{objects: map[string]map[string]any{}}
	for key, raw := range map[string]string{
		"namespace/kube-system":            `{"kind":"Namespace","metadata":{"name":"kube-system","uid":"cluster-uid"}}`,
		"namespace/orka-system":            `{"kind":"Namespace","metadata":{"name":"orka-system","uid":"namespace-uid"}}`,
		"providers.core.orka.ai/inference": `{"kind":"Provider","metadata":{"name":"inference","namespace":"orka-system","uid":"provider-uid","generation":1,"resourceVersion":"1"},"spec":{"type":"openai","defaultModel":"provider-default","baseURL":"https://model.example.invalid/v1","secretRef":{"name":"model-secret","key":"api-key"}},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}`,
		"secret/model-secret":              `{"kind":"Secret","metadata":{"name":"model-secret","namespace":"orka-system","uid":"secret-uid","resourceVersion":"1","annotations":{"canary":"` + nativeCanary + `"}},"data":{"api-key":"` + nativeCanary + `"}}`,
		"deploy/-o":                        fmt.Sprintf(`{"items":[{"kind":"Deployment","metadata":{"name":"orka-controller","namespace":"orka-system","uid":"controller-uid","generation":1,"resourceVersion":"1","labels":{"helm.sh/chart":"orka-0.2.0","app.kubernetes.io/component":"controller","app.kubernetes.io/name":"orka","app.kubernetes.io/instance":"orka","app.kubernetes.io/managed-by":"Helm"}},"spec":{"replicas":1,"template":{"spec":{"serviceAccountName":"orka-controller","containers":[{"name":"controller","image":"ghcr.io/orka-agents/orka@%s","args":["--controller-mode=harness-v2","--watch-namespace=orka-system","--ai-worker-image=ghcr.io/orka-agents/orka/ai-worker@%s","--ai-worker-service-account-name=orka-ai-worker","--metrics-bind-address=:8080"]}]}}},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}]}`, orkaControllerDigest, orkaAIWorkerDigest),
		"serviceaccount/orka-ai-worker":    `{"kind":"ServiceAccount","metadata":{"name":"orka-ai-worker","namespace":"orka-system","uid":"worker-uid","annotations":{"canary":"` + nativeCanary + `"},"labels":{"helm.sh/chart":"orka-0.2.0","app.kubernetes.io/name":"orka","app.kubernetes.io/instance":"orka","app.kubernetes.io/managed-by":"Helm","orka.ai/worker":"true","orka.ai/worker-trust":"ai"}}}`,
	} {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			t.Fatal(err)
		}
		f.objects[key] = obj
	}
	f.objects["serviceaccounts/-o"] = map[string]any{"items": []any{f.objects["serviceaccount/orka-ai-worker"]}}
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args[1:]
		f.calls++
		if !slices.Equal(args[:2], []string{"--context", "kind-test"}) || !slices.Contains(args, "--request-timeout=10s") {
			t.Fatalf("unpinned/unbounded read: %v", args)
		}
		for _, arg := range args {
			if slices.Contains([]string{"apply", "create", "delete", "patch", "replace", "edit", "label", "annotate", "set", "scale", "autoscale", "rollout", "run", "exec", "attach", "cp", "drain", "cordon", "uncordon", "taint", "auth"}, arg) || strings.Contains(strings.ToLower(arg), "subjectaccessreview") || strings.Contains(arg, "dry-run") || strings.Contains(arg, "--raw") {
				t.Fatalf("non-read call: %v", args)
			}
		}
		i := slices.Index(args, "get")
		if i < 0 {
			t.Fatalf("not a read: %v", args)
		}
		key := args[i+1] + "/" + args[i+2]
		obj := f.objects[key]
		output := args[slices.Index(args, "-o")+1]
		if strings.HasPrefix(output, "go-template=secret\n") {
			key += "/presence"
		}
		if f.change != nil {
			if err := f.change(key, f.calls, obj); err != nil {
				return err
			}
		}
		if args[i+1] == "crd" {
			return reconcileCaptureFixture(ctx, cmd)
		}
		if obj == nil {
			return nil
		}
		if strings.HasPrefix(output, "go-template=") {
			projection, err := template.New("projection").Parse(strings.TrimPrefix(output, "go-template="))
			if err != nil {
				t.Fatal(err)
			}
			var captured bytes.Buffer
			if err := projection.Execute(&captured, obj); err != nil {
				return err
			}
			if strings.Contains(captured.String(), nativeCanary) {
				t.Fatal("private value entered projected capture")
			}
			_, err = cmd.Stdout.Write(captured.Bytes())
			return err
		}
		if args[i+1] == "secret" || args[i+1] == "serviceaccount" {
			t.Fatal("private object captured without a fixed projection")
		}
		return json.NewEncoder(cmd.Stdout).Encode(obj)
	}
	return a, suite, f
}

func TestSuiteNativeRefusesUnsupportedRuntimeBeforeOrkaEffects(t *testing.T) {
	for _, profile := range []workspace.RuntimeProfile{{ID: "kagent", Version: "v0.10.3", Engine: "go"}, {}} {
		t.Run(profile.ID, func(t *testing.T) {
			a, suite, f := nativeFixture(t, workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"})
			suite.Runtime = profile
			prepared, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
			if err == nil || !strings.Contains(err.Error(), "Orka") || prepared != nil || f.calls != 0 {
				t.Fatalf("unsupported runtime reached Orka reads/render: %v, calls %d", err, f.calls)
			}
		})
	}
}

func TestSuiteNativeExactPackagedRender(t *testing.T) {
	a, suite, _ := nativeFixture(t)
	p, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	if p.bundle.Provider["spec"].(map[string]any)["defaultModel"] != suite.Model || p.bundle.Agent["spec"].(map[string]any)["systemPrompt"].(map[string]any)["inline"] != string(suite.Instructions) {
		t.Fatal("packaged behavior replaced by defaults")
	}
	if len(p.documents) != 3 || p.bundle.Task != nil {
		t.Fatal("unexpected execution payload")
	}
	var agent map[string]any
	if err := yaml.Unmarshal(p.documents[2], &agent); err != nil {
		t.Fatal(err)
	}
	if agent["spec"].(map[string]any)["systemPrompt"].(map[string]any)["inline"] != string(suite.Instructions) {
		t.Fatal("serialization changed instruction bytes")
	}
}

func nativePrepared(t *testing.T) (*App, *workspace.NativeSuite, *nativeReadFixture, *suiteNativePrepared) {
	t.Helper()
	a, suite, f := nativeFixture(t)
	p, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	return a, suite, f, p
}

func TestSuiteNativeIdentityDomainsAreDeterministic(t *testing.T) {
	a, suite, _, p := nativePrepared(t)
	if p.Subject.SuiteDigest != suite.Digest || p.Subject.GraphDigest != suite.GraphDigest || p.Subject.PayloadDigest != agentruntime.RenderedBundleDigest(p.documents) {
		t.Fatal("source/payload identity lost")
	}
	ids := []string{p.Subject.SuiteDigest, p.Subject.GraphDigest, p.Subject.BindingDigest, p.Subject.RenderedDigest, p.Subject.PayloadDigest}
	for i, id := range ids {
		if slices.Contains(ids[:i], id) {
			t.Fatal("identity domains aliased")
		}
	}
	again, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil || !reflect.DeepEqual(p, again) {
		t.Fatal("read-only render not deterministic", err)
	}
}

func TestSuiteNativeSubjectIsPayloadFree(t *testing.T) {
	_, suite, _, p := nativePrepared(t)
	raw, err := json.Marshal(p.Subject)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{nativeCanary, string(suite.Instructions), "expectContains", "annotations", "systemPrompt"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("subject contains payload/private metadata")
		}
	}
}

func nativeMetadata(object map[string]any) map[string]any { return object["metadata"].(map[string]any) }
func nativeController(f *nativeReadFixture) map[string]any {
	return f.objects["deploy/-o"]["items"].([]any)[0].(map[string]any)
}
func nativeContainer(f *nativeReadFixture) map[string]any {
	return nativeController(f)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
}

func TestSuiteNativePreconditionsRefuseBeforeReads(t *testing.T) {
	for _, refusal := range []string{"nil input", "nil config", "nil runner", "blank context", "credential context", "invalid namespace", "invalid provider", "source collision", "nil images", "member image", "blank model", "blank instructions"} {
		t.Run(refusal, func(t *testing.T) {
			a, suite, f := nativeFixture(t)
			namespace, provider := OrkaNamespace, "inference"
			switch refusal {
			case "nil input":
				suite = nil
			case "nil config":
				a.Cfg = nil
			case "nil runner":
				a.Run = nil
			case "blank context":
				a.Cfg.KubeContext = " "
			case "credential context":
				a.Cfg.KubeContext = "ghp_" + strings.Repeat("x", 36)
			case "invalid namespace":
				namespace = "bad/name"
			case "invalid provider":
				provider = ""
			case "source collision":
				provider = suite.MemberID
			case "nil images":
				suite.RequiredImages = nil
			case "member image":
				suite.RequiredImages = []string{"image"}
			case "blank model":
				suite.Model = " "
			case "blank instructions":
				suite.Instructions = []byte("\n")
			}
			p, err := a.prepareSuiteNative(t.Context(), suite, namespace, provider)
			if p != nil || err == nil || f.calls != 0 {
				t.Fatalf("precondition did not refuse before reads: %v; calls=%d", err, f.calls)
			}
		})
	}
}

func TestSuiteNativeBindingRefusals(t *testing.T) {
	for _, refusal := range []string{"cluster terminating", "namespace uid", "namespace terminating", "provider missing", "provider rv", "provider type", "provider ready", "provider stale", "provider secret key", "provider endpoint", "secret missing", "secret uid", "secret rv", "secret terminating", "key missing", "presence read error", "secret reread error", "schema missing"} {
		t.Run(refusal, func(t *testing.T) {
			a, suite, f := nativeFixture(t)
			provider, secret := f.objects["providers.core.orka.ai/inference"], f.objects["secret/model-secret"]
			switch refusal {
			case "cluster terminating":
				nativeMetadata(f.objects["namespace/kube-system"])["deletionTimestamp"] = "now"
			case "namespace uid":
				nativeMetadata(f.objects["namespace/orka-system"])["uid"] = ""
			case "namespace terminating":
				nativeMetadata(f.objects["namespace/orka-system"])["deletionTimestamp"] = "now"
			case "provider missing":
				delete(f.objects, "providers.core.orka.ai/inference")
			case "provider rv":
				delete(nativeMetadata(provider), "resourceVersion")
			case "provider type":
				provider["spec"].(map[string]any)["type"] = "anthropic"
			case "provider ready":
				provider["status"].(map[string]any)["ready"] = false
			case "provider stale":
				provider["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = float64(0)
			case "provider secret key":
				provider["spec"].(map[string]any)["secretRef"].(map[string]any)["key"] = ""
			case "provider endpoint":
				provider["spec"].(map[string]any)["baseURL"] = "https://model.example.invalid?api-key=" + nativeCanary
			case "secret missing":
				delete(f.objects, "secret/model-secret")
			case "secret uid":
				nativeMetadata(secret)["uid"] = ""
			case "secret rv":
				nativeMetadata(secret)["resourceVersion"] = ""
			case "secret terminating":
				nativeMetadata(secret)["deletionTimestamp"] = "now"
			case "key missing":
				secret["data"] = map[string]any{"other": nativeCanary}
			case "presence read error":
				f.change = func(key string, _ int, _ map[string]any) error {
					if key == "secret/model-secret/presence" {
						return fmt.Errorf("private API error %s", nativeCanary)
					}
					return nil
				}
			case "secret reread error":
				presence := false
				f.change = func(key string, _ int, _ map[string]any) error {
					if key == "secret/model-secret/presence" {
						presence = true
					} else if key == "secret/model-secret" && presence {
						return fmt.Errorf("private API error %s", nativeCanary)
					}
					return nil
				}
			case "schema missing":
				f.change = func(key string, _ int, _ map[string]any) error {
					if key == "crd/tasks.core.orka.ai" {
						return fmt.Errorf("missing")
					}
					return nil
				}
			}
			p, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
			if p != nil || err == nil || strings.Contains(err.Error(), nativeCanary) {
				t.Fatalf("unsafe binding refusal: %v", err)
			}
		})
	}
}

func TestSuiteNativeRuntimeRefusals(t *testing.T) {
	for _, refusal := range []string{"chart", "image", "entrypoint", "mode", "watch", "flag conflict", "worker image", "controller account", "stale readiness", "surplus old replica", "worker trust", "projected worker trust", "worker uid", "worker terminating"} {
		t.Run(refusal, func(t *testing.T) {
			a, suite, f := nativeFixture(t)
			controller, container := nativeController(f), nativeContainer(f)
			worker := f.objects["serviceaccount/orka-ai-worker"]
			args := container["args"].([]any)
			switch refusal {
			case "chart":
				nativeMetadata(controller)["labels"].(map[string]any)["helm.sh/chart"] = "orka-0.1.3"
			case "image":
				container["image"] = "foreign"
			case "entrypoint":
				container["command"] = []any{"other-entrypoint"}
			case "mode":
				args[0] = "--controller-mode=other"
			case "watch":
				args[1] = "--watch-namespace=other"
			case "flag conflict":
				container["args"] = append(args, "-controller-mode=harness-v2")
			case "worker image":
				args[2] = "--ai-worker-image=foreign"
			case "controller account":
				controller["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["serviceAccountName"] = ""
			case "stale readiness":
				controller["status"].(map[string]any)["observedGeneration"] = float64(0)
			case "surplus old replica":
				controller["status"].(map[string]any)["replicas"] = float64(2)
			case "worker trust":
				nativeMetadata(worker)["labels"].(map[string]any)["orka.ai/worker-trust"] = "code"
			case "projected worker trust":
				f.change = func(key string, _ int, _ map[string]any) error {
					if key == "serviceaccount/orka-ai-worker" {
						nativeMetadata(worker)["labels"].(map[string]any)["orka.ai/worker-trust"] = "code"
					}
					return nil
				}
			case "worker uid":
				nativeMetadata(worker)["uid"] = ""
			case "worker terminating":
				nativeMetadata(worker)["deletionTimestamp"] = "now"
			}
			p, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
			if p != nil || err == nil {
				t.Fatal("unsupported native runtime accepted")
			}
		})
	}
}

func TestSuiteNativeReobservationSemantics(t *testing.T) {
	for _, drift := range []string{"context", "cluster uid", "namespace uid", "provider rv", "provider endpoint", "secret rv", "controller uid", "controller account", "controller label", "worker uid", "controller rv", "ready status", "unrelated label"} {
		t.Run(drift, func(t *testing.T) {
			a, _, f, p := nativePrepared(t)
			allowed := false
			switch drift {
			case "context":
				a.Cfg.KubeContext = "other"
			case "cluster uid":
				nativeMetadata(f.objects["namespace/kube-system"])["uid"] = "changed"
			case "namespace uid":
				nativeMetadata(f.objects["namespace/orka-system"])["uid"] = "changed"
			case "provider rv":
				nativeMetadata(f.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "2"
			case "provider endpoint":
				f.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["baseURL"] = "https://other.example.invalid"
			case "secret rv":
				nativeMetadata(f.objects["secret/model-secret"])["resourceVersion"] = "2"
			case "controller uid":
				nativeMetadata(nativeController(f))["uid"] = "changed"
			case "controller account":
				nativeController(f)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["serviceAccountName"] = "other-controller"
			case "controller label":
				nativeMetadata(nativeController(f))["labels"].(map[string]any)["app.kubernetes.io/instance"] = "other"
			case "worker uid":
				nativeMetadata(f.objects["serviceaccount/orka-ai-worker"])["uid"] = "changed"
			case "controller rv":
				nativeMetadata(nativeController(f))["resourceVersion"] = "2"
				allowed = true
			case "ready status":
				nativeController(f)["status"].(map[string]any)["conditions"] = []any{map[string]any{"lastTransitionTime": "changed"}}
				allowed = true
			case "unrelated label":
				nativeMetadata(nativeController(f))["labels"].(map[string]any)["unrelated"] = "changed"
				allowed = true
			}
			if err := a.verifySuiteNativeInputs(t.Context(), p.Subject); (err == nil) != allowed {
				t.Fatalf("wrong drift semantics: %v", err)
			}
		})
	}
}

func TestSuiteNativeRuntimeIgnoresUnconsumedChurnDuringRead(t *testing.T) {
	for _, field := range []string{"resourceVersion", "conditions", "unrelated label"} {
		t.Run(field, func(t *testing.T) {
			a, suite, f := nativeFixture(t)
			f.change = func(key string, _ int, _ map[string]any) error {
				if key == "serviceaccount/orka-ai-worker" {
					switch field {
					case "resourceVersion":
						nativeMetadata(nativeController(f))[field] = "2"
					case "conditions":
						nativeController(f)["status"].(map[string]any)[field] = []any{map[string]any{"lastTransitionTime": "changed"}}
					case "unrelated label":
						nativeMetadata(nativeController(f))["labels"].(map[string]any)["unrelated"] = "changed"
					}
				}
				return nil
			}
			if _, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference"); err != nil {
				t.Fatal("unconsumed churn refused", err)
			}
		})
	}
}

func TestSuiteNativeProjectionErrorsDoNotExposePayload(t *testing.T) {
	for _, kind := range []string{"secret", "serviceaccount"} {
		t.Run(kind, func(t *testing.T) {
			a, suite, _ := nativeFixture(t)
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := cmd.Args[1:]
				i := slices.Index(args, "get")
				if i >= 0 && args[i+1] == kind {
					_, err := fmt.Fprintf(cmd.Stdout, `{"Unexpected":"%s"}`, nativeCanary)
					return err
				}
				return capture(ctx, cmd)
			}
			p, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
			if p != nil || err == nil || strings.Contains(err.Error(), nativeCanary) {
				t.Fatalf("unsafe projection error: %v", err)
			}
		})
	}
}

func TestSuiteNativeCanceledInputIsUnknown(t *testing.T) {
	a, suite, f := nativeFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p, err := a.prepareSuiteNative(ctx, suite, OrkaNamespace, "inference")
	if p != nil || err == nil || f.calls != 0 {
		t.Fatal("canceled preparation not refused")
	}
}

func TestSuiteNativeLegacyClusterIdentityContractIsUnchanged(t *testing.T) {
	a, _, f := nativeFixture(t)
	nativeMetadata(f.objects["namespace/kube-system"])["deletionTimestamp"] = "now"
	if uid, err := a.liftClusterUID(t.Context()); uid != "cluster-uid" || err != nil {
		t.Fatal("legacy cluster identity contract tightened", err)
	}
}

func TestSuiteNativeLegacyProviderDoesNotRequireRV(t *testing.T) {
	a, _, f := nativeFixture(t)
	delete(nativeMetadata(f.objects["providers.core.orka.ai/inference"]), "resourceVersion")
	if _, err := a.liftProviderBindings(t.Context(), OrkaNamespace, "inference"); err != nil {
		t.Fatal("legacy Provider contract tightened", err)
	}
}

func TestSuiteNativeSecretBracketRefusesChanges(t *testing.T) {
	for _, field := range []string{"uid", "resourceVersion"} {
		t.Run(field, func(t *testing.T) {
			a, suite, f := nativeFixture(t)
			f.change = func(key string, _ int, obj map[string]any) error {
				if key == "secret/model-secret/presence" {
					nativeMetadata(obj)[field] = "changed"
				}
				return nil
			}
			p, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
			if p != nil || err == nil || !strings.Contains(err.Error(), "changed during") {
				t.Fatalf("Secret change did not refuse: %v", err)
			}
		})
	}
}

func TestSuiteNativeRuntimeRefusesSnapshotSplicing(t *testing.T) {
	for _, field := range []string{"uid", "serviceAccountName", "readiness"} {
		t.Run(field, func(t *testing.T) {
			a, suite, f := nativeFixture(t)
			f.change = func(key string, _ int, _ map[string]any) error {
				if key == "serviceaccount/orka-ai-worker" {
					switch field {
					case "uid":
						nativeMetadata(nativeController(f))[field] = "replaced"
					case "serviceAccountName":
						nativeController(f)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)[field] = "other-controller"
					case "readiness":
						nativeController(f)["status"].(map[string]any)["readyReplicas"] = float64(0)
					}
				}
				return nil
			}
			p, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
			if p != nil || err == nil {
				t.Fatal("controller snapshot splicing accepted")
			}
		})
	}
}
