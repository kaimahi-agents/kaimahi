package imagelift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func example(t *testing.T) (Environment, agentsuite.ImageDeployment, Plan) {
	t.Helper()
	env := Environment{APIVersion: "kaimahi.dev/lift/v1alpha1", Name: "hello-world", Context: "remote-aks", ClusterUID: "cluster-uid", Namespace: "agents", Adapter: agentsuite.ExecutionHTTPV1, Platform: agentsuite.Platform{OS: "linux", Architecture: "amd64"}, Inputs: map[string]InputBinding{"inference-key": {SecretRef: &agentsuite.SecretKeyRef{Name: "inference", Key: "api-key"}}}}
	digest := "sha256:" + strings.Repeat("a", 64)
	record := agentsuite.ImageDeployment{SchemaVersion: agentsuite.SpecVersion, MediaType: agentsuite.ImageDeploymentMediaType, SuiteReference: "registry.example/suite@" + digest, SuiteDigest: digest, Agent: "hello-world", Platform: env.Platform, CompositionDigest: digest, BuildProfile: "default", Execution: agentsuite.ExecutionContract{Kind: env.Adapter, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []agentsuite.ExecutionInput{{Name: "inference-key", Environment: "MODEL_API_KEY", Secret: true}}}}
	plan, err := Render("registry.example/agent@"+digest, record, env)
	if err != nil {
		t.Fatal(err)
	}
	return env, record, plan
}

func TestRenderPinsImageAndConsumesOnlyDeclaredInputs(t *testing.T) {
	env, record, plan := example(t)
	again, err := Render(plan.Image, record, env)
	if err != nil || again.Digest != plan.Digest {
		t.Fatalf("unstable plan: %v", err)
	}
	data, _ := json.Marshal(plan.Objects)
	for _, want := range []string{plan.Image, `"secretKeyRef":{"name":"inference","key":"api-key"}`, `"automountServiceAccountToken":false`, `"type":"ClusterIP"`, `"kubernetes.io/arch":"amd64"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in %s", want, data)
		}
	}
	if strings.Contains(string(data), "LoadBalancer") || strings.Contains(string(data), "envFrom") {
		t.Fatal("render broadened access")
	}
	value := "plaintext"
	env.Inputs["inference-key"] = InputBinding{Value: &value}
	if _, err := Render(plan.Image, record, env); err == nil {
		t.Fatal("secret literal accepted")
	}
	env.Inputs["extra"] = InputBinding{Value: &value}
	if _, err := Render(plan.Image, record, env); err == nil {
		t.Fatal("unused input accepted")
	}
}

func TestEnvironmentRejectsAmbiguousAndUnknownFields(t *testing.T) {
	env, _, _ := example(t)
	raw, _ := json.Marshal(env)
	if _, err := DecodeEnvironment(raw); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		strings.Replace(string(raw), `"name":"hello-world"`, `"name":"hello-world","name":"shadow"`, 1),
		strings.Replace(string(raw), `"name":"hello-world"`, `"name":"hello-world","unknown":true`, 1),
		strings.Replace(string(raw), `"name":"hello-world"`, `"Name":"hello-world"`, 1),
		strings.Replace(string(raw), `"inputs":`, `"inputs":null,"shadow":`, 1),
		string(raw) + `{}`,
	} {
		if _, err := DecodeEnvironment([]byte(data)); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

type fakeCluster struct {
	objects      map[string][]byte
	calls        [][]string
	writes       int
	wrongCluster bool
	unreadable   bool
	failService  bool
	failRollout  bool
}

func newFake() *fakeCluster { return &fakeCluster{objects: map[string][]byte{}} }

func (f *fakeCluster) Call(_ context.Context, input []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	text := strings.Join(args, " ")
	if strings.Contains(text, "get namespace kube-system") {
		uid := "cluster-uid"
		if f.wrongCluster {
			uid = "different"
		}
		return []byte(fmt.Sprintf(`{"kind":"Namespace","metadata":{"name":"kube-system","uid":%q}}`, uid)), nil
	}
	if strings.Contains(text, "get namespace agents") {
		return []byte(`{"kind":"Namespace","metadata":{"name":"agents"}}`), nil
	}
	if strings.Contains(text, "get nodes") {
		return []byte(`{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`), nil
	}
	if strings.Contains(text, "get secret") {
		return []byte("present"), nil
	}
	if strings.Contains(text, "rollout status") {
		if f.failRollout {
			return nil, errors.New("timeout")
		}
		return []byte("ready"), nil
	}
	if len(input) == 0 {
		if f.unreadable {
			return nil, errors.New("forbidden")
		}
		return f.objects[args[3]], nil
	}
	var object map[string]any
	if err := json.Unmarshal(input, &object); err != nil {
		return nil, err
	}
	kind := object["kind"].(string)
	if strings.Contains(text, "--dry-run=server") {
		return input, nil
	}
	f.writes++
	if f.failService && kind == "Service" {
		return nil, errors.New("connection lost")
	}
	meta := object["metadata"].(map[string]any)
	if _, exists := meta["uid"]; !exists {
		meta["uid"] = kind + "-uid"
	}
	meta["resourceVersion"] = fmt.Sprint(f.writes)
	meta["generation"] = 1
	if kind == "Deployment" {
		object["status"] = map[string]any{"observedGeneration": 1, "updatedReplicas": 1, "availableReplicas": 1}
	}
	if kind == "Service" {
		object["spec"].(map[string]any)["clusterIP"] = "10.0.0.10"
	}
	raw, err := json.Marshal(object)
	f.objects[kind] = raw
	return raw, err
}

func TestDeployCreatesAndUpdatesOwnedResources(t *testing.T) {
	_, _, plan := example(t)
	cluster := newFake()
	confirmed := 0
	for range 2 {
		receipt, err := Deploy(t.Context(), cluster, plan, func() error { confirmed++; return nil })
		if err != nil || !receipt.Ready || len(receipt.Resources) != 2 {
			t.Fatalf("receipt=%+v error=%v", receipt, err)
		}
	}
	if cluster.writes != 4 || confirmed != 2 {
		t.Fatalf("writes=%d confirmed=%d", cluster.writes, confirmed)
	}
	if !strings.Contains(string(cluster.objects["Service"]), `"clusterIP":"10.0.0.10"`) {
		t.Fatal("lost allocated service identity")
	}
}

func TestInspectAndRefusalsNeverWrite(t *testing.T) {
	_, _, plan := example(t)
	for _, name := range []string{"plan", "wrong cluster", "unreadable", "foreign", "denied confirmation"} {
		t.Run(name, func(t *testing.T) {
			cluster := newFake()
			if name == "wrong cluster" {
				cluster.wrongCluster = true
			}
			if name == "unreadable" {
				cluster.unreadable = true
			}
			if name == "foreign" {
				cluster.objects["Deployment"] = []byte(`{"kind":"Deployment","metadata":{"name":"hello-world","namespace":"agents","uid":"foreign","resourceVersion":"1"}}`)
			}
			var err error
			if name == "plan" {
				err = Inspect(t.Context(), cluster, plan)
			} else {
				_, err = Deploy(t.Context(), cluster, plan, func() error {
					if name == "denied confirmation" {
						return errors.New("not confirmed")
					}
					return nil
				})
			}
			if (name == "plan") != (err == nil) {
				t.Fatalf("error=%v", err)
			}
			if cluster.writes != 0 {
				t.Fatalf("wrote %d resources", cluster.writes)
			}
		})
	}
}

func TestDeployReportsPartialAndUnknownOutcomes(t *testing.T) {
	_, _, plan := example(t)
	cluster := newFake()
	cluster.failService = true
	receipt, err := Deploy(t.Context(), cluster, plan, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "outcome unknown") || receipt.Ready || len(receipt.Resources) != 1 || cluster.writes != 2 {
		t.Fatalf("receipt=%+v error=%v writes=%d", receipt, err, cluster.writes)
	}
}

func TestDeployRefusesResourceReplacementAfterConfirmation(t *testing.T) {
	_, _, plan := example(t)
	cluster := newFake()
	_, err := Deploy(t.Context(), cluster, plan, func() error {
		cluster.objects["Deployment"] = []byte(`{"kind":"Deployment","metadata":{"name":"hello-world","namespace":"agents","uid":"new","resourceVersion":"1","labels":{"kaimahi.dev/image-deployment":"hello-world"}}}`)
		return nil
	})
	if err == nil || cluster.writes != 0 {
		t.Fatalf("error=%v writes=%d", err, cluster.writes)
	}
}

func TestDeployDoesNotReportReadinessAfterRolloutFailure(t *testing.T) {
	_, _, plan := example(t)
	cluster := newFake()
	cluster.failRollout = true
	receipt, err := Deploy(t.Context(), cluster, plan, func() error { return nil })
	if err == nil || receipt.Ready || len(receipt.Resources) != 2 {
		t.Fatalf("receipt=%+v error=%v", receipt, err)
	}
}

func TestEnvironmentChangeChangesPlanIdentity(t *testing.T) {
	env, record, plan := example(t)
	env.Inputs["inference-key"] = InputBinding{SecretRef: &agentsuite.SecretKeyRef{Name: "production-model", Key: "api-key"}}
	changed, err := Render(plan.Image, record, env)
	if err != nil || changed.Digest == plan.Digest {
		t.Fatalf("environment change lost from identity: %v", err)
	}
}
