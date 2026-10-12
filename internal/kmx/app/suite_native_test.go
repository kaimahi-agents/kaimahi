package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Reuse the real reconcile protocol for admission and persistent pair writes.
// Only new read projections are supplied here; every actual command is captured.
type suiteNativeFixture struct {
	app                                           *App
	dir                                           string
	suite                                         *workspace.NativeSuite
	source, controller, account                   map[string]any
	clusterUID, namespaceUID, secretUID, secretRV string
	keyPresent                                    bool
	calls                                         []orkaCall
	after                                         func(orkaCall) error
}

func newSuiteNativeFixture(t *testing.T) *suiteNativeFixture {
	t.Helper()
	adapter, _, dir := reconcileFixtureMode(t, true)
	f := &suiteNativeFixture{app: adapter.app, dir: dir, clusterUID: "cluster-uid", namespaceUID: "namespace-uid", secretUID: "secret-uid", secretRV: "7", keyPresent: true}
	instructions := "  Answer exactly.\n\nPackaged whitespace.\n"
	root := t.TempDir()
	instructionFile := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructionFile, []byte(instructions), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "sample", Description: "Packaged description", Model: "packaged-model", InstructionsFile: instructionFile}); err != nil {
		t.Fatal(err)
	}
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	f.suite, err = workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	// Make live authoring differ; only NativeSuite's verified bytes may render.
	if err := os.WriteFile(filepath.Join(root, "agents/sample/instructions.md"), []byte("New live instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	decode := func(raw string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	f.source = decode(`{"kind":"Provider","metadata":{"name":"inference","namespace":"orka-system","uid":"inference-uid","generation":3,"resourceVersion":"9"},"spec":{"type":"openai","baseURL":"https://model.example.invalid/v1","defaultModel":"different-selected-model","secretRef":{"name":"target-secret","key":"api-key"}},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":3}]}}`)
	f.controller = decode(`{"kind":"Deployment","metadata":{"name":"nonstandard-controller","namespace":"orka-system","uid":"controller-uid","generation":4,"resourceVersion":"11","labels":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller","helm.sh/chart":"orka-0.2.0","app.kubernetes.io/instance":"orka","app.kubernetes.io/managed-by":"Helm"}},"spec":{"replicas":1,"template":{"spec":{"serviceAccountName":"orka-controller","containers":[{"name":"controller","image":"ghcr.io/orka-agents/orka@sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370","args":["--controller-mode=harness-v2","--watch-namespace=orka-system","--ai-worker-service-account-name=orka-ai-worker","--ai-worker-image=ghcr.io/orka-agents/orka/ai-worker@sha256:814d7149308e62730df59759c5137667cad04520c94cc50ff56e5028fdc8bcd4"],"env":[{"name":"PRIVATE_ENV","value":"PRIVATE_ENV_CANARY"}]}]}}},"status":{"observedGeneration":4,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}`)
	f.account = decode(`{"kind":"ServiceAccount","metadata":{"name":"orka-ai-worker","namespace":"orka-system","uid":"worker-uid","resourceVersion":"12","labels":{"orka.ai/worker":"true","orka.ai/worker-trust":"ai","helm.sh/chart":"orka-0.2.0","app.kubernetes.io/instance":"orka","app.kubernetes.io/name":"orka","app.kubernetes.io/managed-by":"Helm"}}}`)
	f.app.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		call := orkaCall{Args: append([]string(nil), cmd.Args[1:]...)}
		if slices.Contains(call.Args, "-f") {
			raw, _ := io.ReadAll(cmd.Stdin)
			if err := json.Unmarshal(raw, &call.Document); err != nil {
				return err
			}
			cmd.Stdin = bytes.NewReader(raw)
		}
		if i := slices.Index(call.Args, "-p"); i >= 0 {
			if err := json.Unmarshal([]byte(call.Args[i+1]), &call.Patch); err != nil {
				return err
			}
		}
		f.calls = append(f.calls, call)
		emit := func(v any) error { return json.NewEncoder(cmd.Stdout).Encode(v) }
		var err error
		i := slices.Index(call.Args, "get")
		if i >= 0 && len(call.Args) > i+2 {
			kind, name := call.Args[i+1], call.Args[i+2]
			switch {
			case kind == "deploy":
				err = emit(map[string]any{"items": []any{f.controller}})
			case kind == "serviceaccounts":
				err = emit(map[string]any{"items": []any{f.account}})
			case kind == "serviceaccount":
				projection := call.Args[slices.Index(call.Args, "-o")+1]
				parsed, e := template.New("account").Parse(strings.TrimPrefix(projection, "go-template="))
				if e != nil {
					return e
				}
				err = parsed.Execute(cmd.Stdout, f.account)
			case kind == "namespace":
				uid := f.namespaceUID
				if name == "kube-system" {
					uid = f.clusterUID
				}
				err = emit(map[string]any{"kind": "Namespace", "metadata": map[string]any{"name": name, "uid": uid}})
			case kind == "providers.core.orka.ai" && name == "inference":
				err = emit(f.source)
			case kind == "secret":
				output := call.Args[slices.Index(call.Args, "-o")+1]
				if output == "json" {
					return errors.New("suite requested Secret values")
				}
				if !strings.HasPrefix(output, "go-template=") {
					return fmt.Errorf("unrecognized Secret projection")
				}
				parsed, e := template.New("secret").Parse(strings.TrimPrefix(output, "go-template="))
				if e != nil {
					return e
				}
				data := map[string]string{}
				if f.keyPresent {
					data["api-key"] = "PRIVATE_SECRET_VALUE_CANARY"
				}
				err = parsed.Execute(cmd.Stdout, map[string]any{"kind": "Secret", "metadata": map[string]any{"name": name, "namespace": "orka-system", "uid": f.secretUID, "resourceVersion": f.secretRV, "annotations": map[string]string{"private": "PRIVATE_METADATA_CANARY"}}, "data": data})
			case kind == "crd":
				if slices.Contains(call.Args, "name") {
					_, err = io.WriteString(cmd.Stdout, "customresourcedefinition.apiextensions.k8s.io/"+name)
					break
				}
				plural, _, _ := strings.Cut(name, ".")
				raw, e := os.ReadFile(filepath.Join(os.Getenv("KMX_RECONCILE_FIXTURES"), "v0.2.0", plural+".yaml"))
				if e != nil {
					return e
				}
				_, err = cmd.Stdout.Write(raw)
			default:
				err = reconcileCaptureFixture(ctx, cmd)
			}
		} else if slices.Contains(call.Args, "rollout") {
			if !slices.Contains(call.Args, "deploy/"+nativeMeta(f.controller)["name"].(string)) {
				return errors.New("wrong runtime rollout")
			}
		} else {
			err = reconcileCaptureFixture(ctx, cmd)
		}
		if err == nil && f.after != nil {
			err = f.after(call)
		}
		return err
	}
	return f
}
func (f *suiteNativeFixture) prepare(t *testing.T) *suiteNativePlan {
	t.Helper()
	p, e := f.app.prepareSuiteNative(t.Context(), f.suite, "orka-system", "inference")
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f *suiteNativeFixture) writes(from int) []orkaCall {
	var out []orkaCall
	for _, c := range f.calls[from:] {
		if (slices.Contains(c.Args, "create") || slices.Contains(c.Args, "replace") || slices.Contains(c.Args, "patch") || slices.Contains(c.Args, "delete")) && !slices.Contains(c.Args, "--dry-run=server") {
			out = append(out, c)
		}
	}
	return out
}
func nativeMeta(m map[string]any) map[string]any { return m["metadata"].(map[string]any) }
func nativeSpec(m map[string]any) map[string]any { return m["spec"].(map[string]any) }
func nativeContainer(f *suiteNativeFixture) map[string]any {
	return nativeSpec(f.controller)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
}
func (f *suiteNativeFixture) stored(t *testing.T, kind string) map[string]any {
	t.Helper()
	raw, e := os.ReadFile(filepath.Join(f.dir, orkaPlural(kind)+".json"))
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if e = json.Unmarshal(raw, &m); e != nil {
		t.Fatal(e)
	}
	return m
}

// Replacing the packaged model/instructions with defaults or live authoring fails.
func TestSuiteNativeExactMappingAndReadOnlyInputs(t *testing.T) {
	f := newSuiteNativeFixture(t)
	sourceBefore, _ := json.Marshal(f.source)
	p := f.prepare(t)
	if p.Subject.SuiteDigest != f.suite.Digest || p.Subject.GraphDigest != f.suite.GraphDigest || p.Subject.SuiteDigest == p.Subject.GraphDigest {
		t.Fatal("S/G lost")
	}
	if len(p.Subject.BindingDigest) != 64 || len(p.Subject.RenderedDigest) != 64 || p.Subject.RequiredImages == nil || len(p.Subject.RequiredImages) != 0 {
		t.Fatalf("identities/images: %+v", p.Subject)
	}
	if p.bundle.Task != nil {
		t.Fatal("renderer made Task")
	}
	if nativeSpec(p.bundle.Provider)["defaultModel"] != "packaged-model" || nativeSpec(p.bundle.Agent)["systemPrompt"].(map[string]any)["inline"] != "  Answer exactly.\n\nPackaged whitespace.\n" {
		t.Fatal("packaged behavior replaced")
	}
	if nativeMeta(p.bundle.Agent)["annotations"].(map[string]any)["kaimahi.dev/description"] != "Packaged description" {
		t.Fatal("description lost")
	}
	for _, d := range []map[string]any{p.bundle.Provider, p.bundle.Agent} {
		if nativeMeta(d)["name"] != "sample" {
			t.Fatal("wrong pair name")
		}
		ann, _ := nativeMeta(d)["annotations"].(map[string]any)
		if orkaHasSuiteMarkers(ann) {
			t.Fatal("self-hashed ownership")
		}
	}
	if len(f.writes(0)) != 0 {
		t.Fatal("prepare persisted resources")
	}
	// Caller-owned loader bytes must not mutate the prepared plan.
	f.suite.Instructions[0] = 'X'
	placement, err := f.app.placeSuiteNative(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.verifySuiteNative(t.Context(), placement); err != nil {
		t.Fatal(err)
	}
	if len(placement.Resources) != 2 || placement.Resources[0].UID != "provider-uid" || placement.Resources[1].UID != "agent-uid" || !placement.Complete {
		t.Fatalf("placement: %+v", placement)
	}
	writes := f.writes(0)
	if len(writes) != 2 {
		t.Fatalf("writes: %+v", writes)
	}
	for _, c := range writes {
		if c.Document["kind"] != "Provider" && c.Document["kind"] != "Agent" {
			t.Fatalf("nonpair write: %+v", c)
		}
		if nativeMeta(c.Document)["name"] != "sample" {
			t.Fatal("selected source written")
		}
		ann := nativeMeta(c.Document)["annotations"].(map[string]any)
		if ann["kaimahi.dev/suite-digest"] != p.Subject.SuiteDigest || ann["kaimahi.dev/suite-binding-digest"] != p.Subject.BindingDigest || ann["kaimahi.dev/suite-rendered-digest"] != p.Subject.RenderedDigest {
			t.Fatal("planned markers lost")
		}
		for _, k := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker} {
			if _, ok := ann[k]; ok {
				t.Fatal("legacy markers")
			}
		}
	}
	sourceAfter, _ := json.Marshal(f.source)
	if !bytes.Equal(sourceBefore, sourceAfter) {
		t.Fatal("selected source mutated")
	}
	raw, err := json.Marshal(placement)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"PRIVATE_ENV_CANARY", "Packaged whitespace", "systemPrompt", "different-selected-model", "stringData", "credentials", "portableDigest", "PRIVATE_SECRET_VALUE_CANARY", "PRIVATE_METADATA_CANARY"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("subject leaked %s", forbidden)
		}
	}
	if !strings.Contains(string(raw), `"requiredImages":[]`) {
		t.Fatalf("missing explicit empty images: %s", raw)
	}
}

func TestSuiteNativeRefusesUnsupportedInputsBeforeWrites(t *testing.T) {
	for _, mode := range []string{"collision", "non-openai", "credential-endpoint", "credential-instructions", "credential-selector", "runtime-credential-instance", "blank-model", "blank-instructions", "missing-key", "stale-ready", "status-not-ready", "missing-provider-RV", "missing-secret-UID", "images", "null-images", "runtime-tag", "worker-tag", "runtime-mode", "runtime-watch", "runtime-ambiguous-flag", "runtime-split-flag", "runtime-positional", "runtime-missing-UID", "runtime-not-ready", "worker-UID", "worker-trust"} {
		t.Run(mode, func(t *testing.T) {
			f := newSuiteNativeFixture(t)
			selected := "inference"
			switch mode {
			case "collision":
				selected = "sample"
			case "non-openai":
				nativeSpec(f.source)["type"] = "anthropic"
			case "credential-endpoint":
				nativeSpec(f.source)["baseURL"] = "https://user:password@example.invalid/v1"
			case "credential-instructions":
				f.suite.Instructions = []byte("sk-" + strings.Repeat("A", 48))
			case "credential-selector":
				selected = "sk-" + strings.Repeat("a", 48)
			case "runtime-credential-instance":
				for _, object := range []map[string]any{f.controller, f.account} {
					nativeMeta(object)["labels"].(map[string]any)["app.kubernetes.io/instance"] = "sk-" + strings.Repeat("a", 48)
				}
			case "blank-model":
				f.suite.Model = " "
			case "blank-instructions":
				f.suite.Instructions = []byte(" \n")
			case "missing-key":
				f.keyPresent = false
			case "stale-ready":
				f.source["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = float64(2)
			case "status-not-ready":
				f.source["status"].(map[string]any)["ready"] = false
			case "missing-provider-RV":
				delete(nativeMeta(f.source), "resourceVersion")
			case "missing-secret-UID":
				f.secretUID = ""
			case "images":
				f.suite.RequiredImages = []string{"example/image"}
			case "null-images":
				f.suite.RequiredImages = nil
			case "runtime-tag":
				nativeContainer(f)["image"] = "ghcr.io/orka-agents/orka:v0.2.0"
			case "worker-tag":
				nativeContainer(f)["args"].([]any)[3] = "--ai-worker-image=ghcr.io/orka-agents/orka/ai-worker:v0.2.0"
			case "runtime-mode":
				nativeContainer(f)["args"].([]any)[0] = "--controller-mode=harness-v1"
			case "runtime-watch":
				nativeContainer(f)["args"].([]any)[1] = "--watch-namespace=elsewhere"
			case "runtime-ambiguous-flag":
				nativeContainer(f)["args"] = append(nativeContainer(f)["args"].([]any), "-ai-worker-image=other")
			case "runtime-split-flag":
				nativeContainer(f)["args"].([]any)[3] = "--ai-worker-image"
			case "runtime-positional":
				nativeContainer(f)["args"] = append([]any{"--"}, nativeContainer(f)["args"].([]any)...)
			case "runtime-missing-UID":
				delete(nativeMeta(f.controller), "uid")
			case "runtime-not-ready":
				f.controller["status"].(map[string]any)["updatedReplicas"] = float64(0)
			case "worker-UID":
				delete(nativeMeta(f.account), "uid")
			case "worker-trust":
				nativeMeta(f.account)["labels"].(map[string]any)["orka.ai/worker-trust"] = "vendor"
			}
			p, err := f.app.prepareSuiteNative(t.Context(), f.suite, "orka-system", selected)
			if err == nil || p != nil {
				t.Fatalf("accepted %s", mode)
			}
			want := map[string]string{
				"collision": "collides", "non-openai": "only a selected openai", "credential-endpoint": "invalid bindings", "credential-instructions": "shaped", "credential-selector": "credential-shaped", "runtime-credential-instance": "credential-shaped", "blank-model": "nonblank", "blank-instructions": "nonblank", "missing-key": "referenced key is missing", "stale-ready": "not Ready", "status-not-ready": "not Ready", "missing-provider-RV": "resourceVersion", "missing-secret-UID": "Secret metadata", "images": "empty requiredImages", "null-images": "empty requiredImages", "runtime-tag": "pinned Orka controller image", "worker-tag": "worker image", "runtime-mode": "runtime mode", "runtime-watch": "watch namespace", "runtime-ambiguous-flag": "ambiguous ai-worker-image", "runtime-split-flag": "unsupported or ambiguous ai-worker-image", "runtime-positional": "positional", "runtime-missing-UID": "chart controller identity", "runtime-not-ready": "not Ready", "worker-UID": "worker account", "worker-trust": "worker-trust",
			}[mode]
			if want == "" || !strings.Contains(err.Error(), want) {
				t.Fatalf("wrong refusal for %s: %v; want %q", mode, err, want)
			}
			if strings.Contains(err.Error(), "sk-"+strings.Repeat("a", 48)) {
				t.Fatal("credential-shaped metadata echoed")
			}
			if len(f.writes(0)) != 0 {
				t.Fatalf("refusal persisted: %+v", f.writes(0))
			}
		})
	}
}

// All relevant snapshot fields must invalidate an otherwise Ready placement.
func TestSuiteNativeVerificationRefusesLiveDrift(t *testing.T) {
	for _, mode := range []string{"provider-UID", "provider-generation", "provider-RV", "provider-type", "provider-endpoint", "provider-secret", "secret-UID", "secret-RV", "secret-key", "cluster-UID", "namespace-UID", "controller-UID", "controller-generation", "controller-image", "worker-image", "worker-UID", "worker-label", "owned-UID", "owned-generation", "owned-spec", "owned-markers", "owned-not-ready"} {
		t.Run(mode, func(t *testing.T) {
			f := newSuiteNativeFixture(t)
			p := f.prepare(t)
			placement, e := f.app.placeSuiteNative(t.Context(), p)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "provider-UID":
				nativeMeta(f.source)["uid"] = "replacement"
			case "provider-generation":
				nativeMeta(f.source)["generation"] = float64(4)
			case "provider-RV":
				nativeMeta(f.source)["resourceVersion"] = "new"
			case "provider-type":
				nativeSpec(f.source)["type"] = "anthropic"
			case "provider-endpoint":
				nativeSpec(f.source)["baseURL"] = "https://other.invalid/v1"
			case "provider-secret":
				nativeSpec(f.source)["secretRef"].(map[string]any)["name"] = "other-secret"
			case "secret-UID":
				f.secretUID = "new"
			case "secret-RV":
				f.secretRV = "8"
			case "secret-key":
				f.keyPresent = false
			case "cluster-UID":
				f.clusterUID = "new"
			case "namespace-UID":
				f.namespaceUID = "new"
			case "controller-UID":
				nativeMeta(f.controller)["uid"] = "new"
			case "controller-generation":
				nativeMeta(f.controller)["generation"] = float64(5)
			case "controller-image":
				nativeContainer(f)["image"] = "foreign/image"
			case "worker-image":
				nativeContainer(f)["args"].([]any)[3] = "--ai-worker-image=foreign/image"
			case "worker-UID":
				nativeMeta(f.account)["uid"] = "new"
			case "worker-label":
				nativeMeta(f.account)["labels"].(map[string]any)["app.kubernetes.io/instance"] = "new"
			case "owned-not-ready":
				if err := os.WriteFile(filepath.Join(f.dir, "provider-not-ready"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			default:
				m := f.stored(t, "Agent")
				switch mode {
				case "owned-UID":
					nativeMeta(m)["uid"] = "new"
				case "owned-generation":
					nativeMeta(m)["generation"] = float64(2)
				case "owned-spec":
					nativeSpec(m)["systemPrompt"] = map[string]any{"inline": "drift"}
				case "owned-markers":
					nativeMeta(m)["annotations"].(map[string]any)["kaimahi.dev/suite-digest"] = "sha256:" + strings.Repeat("f", 64)
				}
				seedReconcile(t, f.dir, m)
			}
			before := len(f.calls)
			if err := f.app.verifySuiteNative(t.Context(), placement); err == nil {
				t.Fatal("drift passed")
			}
			if len(f.writes(before)) != 0 {
				t.Fatal("verification mutated")
			}
		})
	}
}

func TestSuiteNativePairPreflightAndReconcile(t *testing.T) {
	for _, mode := range []string{"reuse", "revision", "spec-update", "foreign-agent", "mixed-agent", "unowned-agent"} {
		t.Run(mode, func(t *testing.T) {
			f := newSuiteNativeFixture(t)
			p := f.prepare(t)
			first, e := f.app.placeSuiteNative(t.Context(), p)
			if e != nil {
				t.Fatal(e)
			}
			if !first.Complete {
				t.Fatal("incomplete initial placement")
			}
			agent := f.stored(t, "Agent")
			switch mode {
			case "revision":
				f.suite.Digest = "sha256:" + strings.Repeat("f", 64)
			case "spec-update":
				f.suite.Instructions = []byte("Changed packaged instructions\n")
			case "foreign-agent":
				nativeMeta(agent)["annotations"].(map[string]any)["kaimahi.dev/suite"] = "other"
			case "mixed-agent":
				nativeMeta(agent)["annotations"].(map[string]any)[orkaBundleMarker] = "sample"
			case "unowned-agent":
				delete(nativeMeta(agent), "annotations")
			}
			if strings.HasSuffix(mode, "agent") {
				seedReconcile(t, f.dir, agent)
			}
			before := len(f.calls)
			next, e := f.app.prepareSuiteNative(t.Context(), f.suite, "orka-system", "inference")
			if strings.HasSuffix(mode, "agent") {
				if e == nil || next != nil {
					t.Fatal("foreign pair accepted")
				}
				if len(f.writes(before)) != 0 {
					t.Fatal("Provider written before Agent refusal")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			second, e := f.app.placeSuiteNative(t.Context(), next)
			if e != nil {
				t.Fatal(e)
			}
			if e = f.app.verifySuiteNative(t.Context(), second); e != nil {
				t.Fatal(e)
			}
			if mode == "reuse" && len(f.writes(before)) != 0 {
				t.Fatal("exact reuse wrote")
			}
			for _, r := range second.Resources {
				if r.UID != strings.ToLower(r.Kind)+"-uid" {
					t.Fatal("UID changed")
				}
				if mode == "revision" && r.Generation != 1 {
					t.Fatal("marker refresh changed generation")
				}
			}
			if mode == "spec-update" && second.Resources[1].Outcome != agentruntime.ResourceUpdated {
				t.Fatal("spec not updated")
			}
		})
	}
}

func TestSuiteNativeDriftBeforeMutationAndPartialOutcomes(t *testing.T) {
	for _, mode := range []string{"prewrite-secret", "prewrite-cluster", "second-prewrite-provider", "provider-ambiguous", "agent-ambiguous", "agent-admission", "agent-ready-regression", "pair-drift", "guard"} {
		t.Run(mode, func(t *testing.T) {
			f := newSuiteNativeFixture(t)
			p := f.prepare(t)
			switch mode {
			case "prewrite-secret":
				f.secretRV = "8"
			case "prewrite-cluster":
				f.clusterUID = "new"
			case "guard":
				t.Setenv("KMX_RECONCILE_REMOTE", "1")
			}
			writes := 0
			f.after = func(c orkaCall) error {
				persistent := (slices.Contains(c.Args, "create") || slices.Contains(c.Args, "replace")) && !slices.Contains(c.Args, "--dry-run=server")
				if persistent {
					writes++
					if mode == "second-prewrite-provider" && writes == 1 {
						nativeSpec(f.source)["baseURL"] = "https://drift.invalid/v1"
					}
					if mode == "provider-ambiguous" && writes == 1 || mode == "agent-ambiguous" && writes == 2 {
						return errors.New("lost response")
					}
				}
				if mode == "agent-admission" && slices.Contains(c.Args, "--dry-run=server") && c.Document["kind"] == "Agent" {
					return errors.New("refused admission")
				}
				if mode == "agent-ready-regression" && persistent && writes == 2 {
					if e := os.WriteFile(filepath.Join(f.dir, "provider-not-ready"), nil, 0600); e != nil {
						return e
					}
				}
				if mode == "pair-drift" && persistent && writes == 1 {
					m := f.stored(t, "Provider")
					nativeMeta(m)["uid"] = "replacement"
					seedReconcile(t, f.dir, m)
				}
				return nil
			}
			before := len(f.calls)
			placement, err := f.app.placeSuiteNative(t.Context(), p)
			if err == nil {
				t.Fatal("drift/ambiguous outcome passed")
			}
			if placement == nil || placement.Complete {
				t.Fatal("partial record lost or marked complete")
			}
			want := 0
			if mode == "provider-ambiguous" || mode == "second-prewrite-provider" || mode == "pair-drift" {
				want = 1
			}
			if mode == "agent-ambiguous" || mode == "agent-ready-regression" {
				want = 2
			}
			if len(f.writes(before)) != want {
				t.Fatalf("writes=%d want=%d", len(f.writes(before)), want)
			}
			if want > 0 && len(placement.Resources) == 0 {
				t.Fatal("partial resource outcomes lost")
			}
			if mode == "agent-ready-regression" {
				if placement.Resources[0].Observed == nil || placement.Resources[1].Observed == nil || placement.Resources[0].Observed.Ready {
					t.Fatal("failed final Ready check lost residual observations")
				}
			}
			if strings.HasSuffix(mode, "ambiguous") {
				last := placement.Resources[len(placement.Resources)-1]
				if !last.Ambiguous || last.Observed == nil || last.Observed.UID == "" {
					t.Fatalf("ambiguous residual not inspected: %+v", last)
				}
			}
			if e := f.app.verifySuiteNative(t.Context(), placement); e == nil {
				t.Fatal("partial placement verified")
			}
		})
	}
}

// Before/after identities must be serializable without retaining any live spec.
func TestSuiteNativeRetainsBeforePairSnapshots(t *testing.T) {
	f := newSuiteNativeFixture(t)
	p := f.prepare(t)
	if len(p.Before) != 2 || p.Before[0].Exists || p.Before[1].Exists {
		t.Fatalf("absent pair snapshot: %+v", p.Before)
	}
	placed, err := f.app.placeSuiteNative(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	next := f.prepare(t)
	for i, before := range next.Before {
		if !before.Exists || before.Identity == nil || before.Identity.UID != placed.Resources[i].UID || before.Identity.Generation != placed.Resources[i].Generation || before.Identity.SpecDigest != placed.Resources[i].SpecDigest || !before.Identity.Ready {
			t.Fatalf("existing pair before snapshot: %+v", before)
		}
	}
	reused, err := f.app.placeSuiteNative(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	if len(reused.Before) != 2 || !reused.Before[0].Exists {
		t.Fatal("placement lost before snapshot")
	}
	raw, err := json.Marshal(reused)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"before"`) || strings.Contains(string(raw), "systemPrompt") {
		t.Fatal("before snapshot is not value-free evidence input")
	}
}

// Status-only owned/controller RV changes are not spec drift, but source RV is pinned.
func TestSuiteNativeIgnoresStatusOnlyOwnedAndRuntimeRV(t *testing.T) {
	f := newSuiteNativeFixture(t)
	p := f.prepare(t)
	placement, e := f.app.placeSuiteNative(t.Context(), p)
	if e != nil {
		t.Fatal(e)
	}
	nativeMeta(f.controller)["resourceVersion"] = "status-update"
	for _, kind := range []string{"Provider", "Agent"} {
		m := f.stored(t, kind)
		nativeMeta(m)["resourceVersion"] = "status-update"
		seedReconcile(t, f.dir, m)
	}
	if err := f.app.verifySuiteNative(t.Context(), placement); err != nil {
		t.Fatal(err)
	}
}
