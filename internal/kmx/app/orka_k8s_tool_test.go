package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// These fixtures model the chart controller's configured account and the
// corresponding Kubernetes identity; no release-name inference is involved.
func workerController(name, chart, instance, managedBy, args string) string {
	return fmt.Sprintf(`{"metadata":{"name":%q,"labels":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller","helm.sh/chart":%q,"app.kubernetes.io/instance":%q,"app.kubernetes.io/managed-by":%q}},"spec":{"template":{"spec":{"containers":[{"name":"controller","args":%s},{"name":"sidecar","args":["--ai-worker-service-account-name=wrong-sidecar"]}]}}}}`, name, chart, instance, managedBy, args)
}

func workerAccount(name, namespace, chart, instance, trust string) string {
	return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":%q,"labels":{"orka.ai/worker":"true","orka.ai/worker-trust":%q,"helm.sh/chart":%q,"app.kubernetes.io/instance":%q,"app.kubernetes.io/name":"orka","app.kubernetes.io/managed-by":"Helm"}}}`, name, namespace, trust, chart, instance)
}

func TestOrkaAIWorkerAccount(t *testing.T) {
	const chart = "orka-0.2.0"
	for _, tt := range []struct {
		name, deployments, accounts, want, refusal string
	}{
		{"kmx override", `[ ` + workerController("orka-api-controller", chart, "orka-api", "Helm", `["--ai-worker-service-account-name=orka-api-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-api-ai-worker", OrkaNamespace, chart, "orka-api", "ai") + ` ]`, "orka-api-ai-worker", ""},
		{"stock Helm", `[ ` + workerController("orka-controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "ai") + ` ]`, "orka-ai-worker", ""},
		{"newer chart matching worker", `[ ` + workerController("orka-controller", "orka-0.2.1", "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, "orka-0.2.1", "orka", "ai") + ` ]`, "orka-ai-worker", ""},
		{"no controller", `[]`, `[]`, "", "no Orka controller"},
		{"ambiguous controllers", `[ ` + workerController("one", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + `,` + workerController("two", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[]`, "", "multiple Orka controller"},
		{"controller and worker chart mismatch", `[ ` + workerController("controller", "orka-0.1.3", "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "ai") + ` ]`, "", "chart"},
		{"missing chart labels", `[ ` + workerController("controller", "", "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, "", "orka", "ai") + ` ]`, "", "chart"},
		{"missing instance", `[ ` + workerController("controller", chart, "", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[]`, "", "instance"},
		{"wrong manager", `[ ` + workerController("controller", chart, "orka", "unknown", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[]`, "", "managed-by"},
		{"no flag", `[ ` + workerController("controller", chart, "orka", "Helm", `[]`) + ` ]`, `[]`, "", "--ai-worker-service-account-name"},
		{"empty flag", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name="]`) + ` ]`, `[]`, "", "--ai-worker-service-account-name"},
		{"invalid account name", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=INVALID NAME"]`) + ` ]`, `[]`, "", "invalid"},
		{"duplicate flag", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker","--ai-worker-service-account-name=other"]`) + ` ]`, `[]`, "", "multiple"},
		{"duplicate split flag", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker","--ai-worker-service-account-name","other"]`) + ` ]`, `[]`, "", "multiple"},
		{"unsupported split flag", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name","orka-ai-worker"]`) + ` ]`, `[]`, "", "unsupported split"},
		{"unsupported single-dash split flag", `[ ` + workerController("controller", chart, "orka", "Helm", `["-ai-worker-service-account-name","orka-ai-worker"]`) + ` ]`, `[]`, "", "unsupported split"},
		{"duplicate single-dash flag", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker","-ai-worker-service-account-name=other"]`) + ` ]`, `[]`, "", "multiple"},
		{"absent account", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[]`, "", "ServiceAccount"},
		{"wrong trust", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "controller") + ` ]`, "", "worker-trust"},
		{"wrong account instance", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, chart, "other", "ai") + ` ]`, "", "instance"},
		{"wrong account chart", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, "orka-0.1.3", "orka", "ai") + ` ]`, "", "chart"},
		{"wrong account namespace", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", "other", chart, "orka", "ai") + ` ]`, "", "namespace"},
		{"duplicate account", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "ai") + `,` + workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "ai") + ` ]`, "", "multiple"},
		{"duplicate account with wrong trust first", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "controller") + `,` + workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "ai") + ` ]`, "", "multiple"},
		{"wrong worker label", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + strings.Replace(workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "ai"), `"orka.ai/worker":"true"`, `"orka.ai/worker":"false"`, 1) + ` ]`, "", "worker=true"},
		{"wrong account app", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + strings.Replace(workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "ai"), `"app.kubernetes.io/name":"orka"`, `"app.kubernetes.io/name":"other"`, 1) + ` ]`, "", "app name"},
		{"wrong account manager", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `[ ` + strings.Replace(workerAccount("orka-ai-worker", OrkaNamespace, chart, "orka", "ai"), `"app.kubernetes.io/managed-by":"Helm"`, `"app.kubernetes.io/managed-by":"other"`, 1) + ` ]`, "", "managed-by"},
		{"malformed account list", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `{"items":{}}`, "", "malformed"},
		{"missing account items", `[ ` + workerController("controller", chart, "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`) + ` ]`, `{}`, "", "malformed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fakeTool(t, dir, "kubectl", fmt.Sprintf(`
case " $* " in
  *" get deploy -o json "*) printf '%%s' '{"items":%s}' ;;
  *" get serviceaccounts -o json "*) printf '%%s' '%s' ;;
  *) exit 1 ;;
esac`, tt.deployments, func() string {
				if strings.HasPrefix(tt.accounts, "[") {
					return `{"items":` + tt.accounts + `}`
				}
				return tt.accounts
			}()))
			t.Setenv("PATH", dir)
			a := &App{Cfg: &config.Config{KubeContext: "kind-demo"}, Run: &run.Runner{}}
			got, err := a.orkaAIWorkerAccount(context.Background())
			if tt.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tt.refusal) || got != "" {
					t.Fatalf("account=%q error=%v, want refusal mentioning %q", got, err, tt.refusal)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("account=%q error=%v, want %q", got, err, tt.want)
			}
		})
	}
}

// The fake boundary checks the exact SAR permission without touching a cluster.
func TestOrkaWorkerCanGetPolicy(t *testing.T) {
	for _, tt := range []struct {
		name, response, failure, want string
		denial                        bool
	}{
		{"allowed", `{"status":{"allowed":true}}`, "", "", false},
		{"denied", `{"status":{"allowed":false,"denied":true}}`, "", "denied", true},
		{"no opinion", `{"status":{"allowed":false,"denied":false}}`, "", "denied", true},
		{"denied flag omitted", `{"status":{"allowed":false}}`, "", "denied", true},
		{"conflicting", `{"status":{"allowed":true,"denied":true}}`, "", "indeterminate", false},
		{"evaluation error", `{"status":{"allowed":false,"denied":false,"evaluationError":"sensitive-evaluation"}}`, "", "indeterminate", false},
		{"evaluation error without allowed", `{"status":{"evaluationError":"sensitive-evaluation"}}`, "", "indeterminate", false},
		{"missing allowed", `{"status":{"denied":false}}`, "", "invalid", false},
		{"malformed", `{"status":"sensitive-response"}`, "", "invalid", false},
		{"missing status", `{}`, "", "invalid", false},
		{"kubectl failure", ``, "sensitive-admission", "cannot evaluate", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			stdin := filepath.Join(dir, "stdin")
			args := filepath.Join(dir, "args")
			fakeTool(t, dir, "kubectl", fmt.Sprintf(`printf '%%s\n' "$*" > %[1]q
/bin/cat > %[2]q
if [ -n %[4]q ]; then printf '%%s' %[4]q >&2; exit 1; fi
printf '%%s' %[3]q`, args, stdin, tt.response, tt.failure))
			t.Setenv("PATH", dir)
			a := &App{Cfg: &config.Config{KubeContext: "kind-demo"}, Run: &run.Runner{}}
			err := a.orkaWorkerCanGetPolicy(context.Background(), "orka-ai-worker", OrkaNamespace, quickstartK8sToolPolicy)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error=%v, want %q", err, tt.want)
			}
			var denial *policyPermissionDenied
			if errors.As(err, &denial) != tt.denial {
				t.Fatalf("unexpected typed denial for %s: %v", tt.name, err)
			}
			if err != nil {
				for _, forbidden := range []string{"sensitive-admission", "sensitive-evaluation", "sensitive-response"} {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("leaked raw response: %v", err)
					}
				}
				for _, want := range []string{"orka-ai-worker", "get", OrkaNamespace, quickstartK8sToolPolicy} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal lacks %s: %v", want, err)
					}
				}
			}
			gotArgs, _ := os.ReadFile(args)
			if string(gotArgs) != "--context kind-demo --request-timeout=10s create --raw /apis/authorization.k8s.io/v1/subjectaccessreviews -f -\n" {
				t.Fatalf("unscoped SAR args: %q", gotArgs)
			}
			var sent struct {
				APIVersion, Kind string
				Spec             struct {
					User               string
					Groups             []string
					ResourceAttributes struct{ Namespace, Group, Resource, Name, Verb string }
				}
			}
			body, _ := os.ReadFile(stdin)
			if json.Unmarshal(body, &sent) != nil || sent.APIVersion != "authorization.k8s.io/v1" || sent.Kind != "SubjectAccessReview" || sent.Spec.User != "system:serviceaccount:orka-system:orka-ai-worker" || !reflect.DeepEqual(sent.Spec.Groups, []string{"system:serviceaccounts", "system:serviceaccounts:orka-system", "system:authenticated"}) || sent.Spec.ResourceAttributes != (struct{ Namespace, Group, Resource, Name, Verb string }{OrkaNamespace, "core.orka.ai", "outboundaccesspolicies", quickstartK8sToolPolicy, "get"}) {
				t.Fatal("SAR did not target the exact worker and named get permission")
			}
		})
	}
}

func TestOrkaWorkerCanGetPolicyUsesTaskNamespace(t *testing.T) {
	for _, tc := range []struct {
		name, grantNamespace string
		allowed              bool
	}{
		{"task namespace grant", "health-team", true},
		{"release namespace grant only", OrkaNamespace, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			request := filepath.Join(dir, "review")
			fakeTool(t, dir, "kubectl", fmt.Sprintf(`/bin/cat > %[1]q
if /bin/grep -q '"user":"system:serviceaccount:%[2]s:orka-ai-worker"' %[1]q; then
  printf '%%s' '{"status":{"allowed":true}}'
else
  printf '%%s' '{"status":{"allowed":false}}'
fi`, request, tc.grantNamespace))
			t.Setenv("PATH", dir)
			a := &App{Cfg: &config.Config{KubeContext: "kind-demo"}, Run: &run.Runner{}}
			err := a.orkaWorkerCanGetPolicy(t.Context(), "orka-ai-worker", "health-team", "health-gateway")
			if tc.allowed && err != nil || !tc.allowed && (err == nil || !strings.Contains(err.Error(), "health-team/orka-ai-worker")) {
				t.Fatalf("task-namespace policy result: allowed=%t err=%v", tc.allowed, err)
			}
			var review struct {
				Spec struct {
					User               string   `json:"user"`
					Groups             []string `json:"groups"`
					ResourceAttributes struct {
						Namespace, Group, Resource, Name, Verb string
					} `json:"resourceAttributes"`
				} `json:"spec"`
			}
			body, readErr := os.ReadFile(request)
			if readErr != nil || json.Unmarshal(body, &review) != nil ||
				review.Spec.User != "system:serviceaccount:health-team:orka-ai-worker" ||
				!reflect.DeepEqual(review.Spec.Groups, []string{"system:serviceaccounts", "system:serviceaccounts:health-team", "system:authenticated"}) ||
				review.Spec.ResourceAttributes != (struct{ Namespace, Group, Resource, Name, Verb string }{"health-team", "core.orka.ai", "outboundaccesspolicies", "health-gateway", "get"}) {
				t.Fatal("review did not ask about the Task namespace worker's exact policy permission")
			}
		})
	}
}

func TestLiftToolPolicyUsesAgentNamespace(t *testing.T) {
	for _, tc := range []struct {
		grantNamespace string
		allowed        bool
	}{
		{"health-team", true},
		{OrkaNamespace, false},
	} {
		t.Run(tc.grantNamespace, func(t *testing.T) {
			dir := t.TempDir()
			request := filepath.Join(dir, "review")
			fakeTool(t, dir, "kubectl", fmt.Sprintf(`case " $* " in
  *" get deploy -o json "*) printf '%%s' '{"items":[%s]}' ;;
  *" get serviceaccounts -o json "*) printf '%%s' '{"items":[%s]}' ;;
  *" get tools.core.orka.ai health --ignore-not-found=true -o json "*) printf '%%s' '{"kind":"Tool","metadata":{"name":"health","namespace":"health-team","generation":1},"spec":{"http":{"outboundAccessPolicyRef":{"name":"health-gateway"}}},"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":1}]}}' ;;
  *" create --raw "*) /bin/cat > %[3]q
    if /bin/grep -q '"user":"system:serviceaccount:%[4]s:orka-ai-worker"' %[3]q; then printf '%%s' '{"status":{"allowed":true}}'; else printf '%%s' '{"status":{"allowed":false}}'; fi ;;
  *) exit 1 ;;
esac`, workerController("orka-controller", "orka-0.2.0", "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`), workerAccount("orka-ai-worker", OrkaNamespace, "orka-0.2.0", "orka", "ai"), request, tc.grantNamespace))
			t.Setenv("PATH", dir)
			a := &App{Cfg: &config.Config{KubeContext: "kind-demo"}, Run: &run.Runner{}}
			bundle := &scaffold.OrkaBundle{Agent: map[string]any{"spec": map[string]any{"tools": []any{map[string]any{"name": "health"}}}}}
			err := a.liftToolsAvailable(t.Context(), "health-team", bundle)
			if tc.allowed && err != nil || !tc.allowed && (err == nil || !strings.Contains(err.Error(), "health-team/orka-ai-worker")) {
				t.Fatalf("Tool in health-team, grant in %s: %v", tc.grantNamespace, err)
			}
			body, readErr := os.ReadFile(request)
			if readErr != nil || !bytes.Contains(body, []byte(`"user":"system:serviceaccount:health-team:orka-ai-worker"`)) {
				t.Fatal("Tool preflight did not review the health-team worker")
			}
		})
	}
}

func TestQuickstartExistingAgentToolAttachmentDoesNotCreateBundle(t *testing.T) {
	a, _, _, _, dir := orkaCreateFixture(t, "")
	root := t.TempDir()
	t.Chdir(root)
	body := []byte(`{"kind":"Agent","metadata":{"name":"sample","namespace":"orka-system","resourceVersion":"1"},"spec":{"tools":[{"name":"k8s-get-resources"}]}}`)
	if err := os.WriteFile(filepath.Join(dir, "sample-agents.core.orka.ai.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.attachQuickstartK8sTool("sample", "orka-system"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("agents", "sample")); !os.IsNotExist(err) {
		t.Fatalf("existing-agent attachment wrote an agent bundle: %v", err)
	}
}

func TestQuickstartK8sToolPatchPreservesExistingTools(t *testing.T) {
	raw := []byte(`{"metadata":{"resourceVersion":"123"},"spec":{"tools":[{"name":"web_fetch","enabled":false}],"systemPrompt":{"inline":"custom"}}}`)
	patch, err := quickstartK8sToolPatch(raw)
	if err != nil {
		t.Fatal(err)
	}
	var ops []struct {
		Op, Path string
		Value    json.RawMessage
	}
	if err := json.Unmarshal(patch, &ops); err != nil || len(ops) != 2 || ops[0].Op != "test" || ops[0].Path != "/metadata/resourceVersion" || ops[1].Path != "/spec/tools" {
		t.Fatalf("patch=%s err=%v", patch, err)
	}
	if !strings.Contains(string(ops[1].Value), `"enabled":false`) || !strings.Contains(string(ops[1].Value), quickstartK8sTool) {
		t.Fatalf("lost existing tool configuration: %s", patch)
	}
	for _, enabled := range []string{"true", "false"} {
		patch, err := quickstartK8sToolPatch([]byte(`{"metadata":{"resourceVersion":"123"},"spec":{"tools":[{"name":"k8s-get-resources","enabled":` + enabled + `}]}}`))
		if err != nil || patch != nil {
			t.Fatalf("existing tool changed: %s %v", patch, err)
		}
	}
}

func TestQuickstartK8sToolUsesExactGatewayPolicy(t *testing.T) {
	body, err := manifest("orka-k8s-tool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body, err = renderQuickstartK8sTool(body, "orka-ai-worker")
	if err != nil {
		t.Fatal(err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(body))
	var policyOK, toolOK, readerRoleOK, readerBindingOK bool
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		metadata, _ := document["metadata"].(map[string]any)
		spec, _ := document["spec"].(map[string]any)
		switch {
		case document["kind"] == "OutboundAccessPolicy" && metadata["name"] == quickstartK8sToolPolicy:
			gateway, _ := spec["gateway"].(map[string]any)
			serviceRef, _ := gateway["serviceRef"].(map[string]any)
			policyOK = gateway["scheme"] == "http" &&
				serviceRef["name"] == "kmx-k8s-tool" &&
				serviceRef["port"] == 8080
		case document["kind"] == "Tool" && metadata["name"] == quickstartK8sTool:
			http, _ := spec["http"].(map[string]any)
			policyRef, _ := http["outboundAccessPolicyRef"].(map[string]any)
			toolOK = http["url"] == quickstartK8sToolAuthority &&
				quickstartK8sToolAuthority == "https://1.1.1.1/resources" &&
				http["method"] == "POST" &&
				policyRef["name"] == quickstartK8sToolPolicy
		case document["kind"] == "Role" && metadata["name"] == "kmx-k8s-tool-policy-reader":
			rules, _ := document["rules"].([]any)
			if len(rules) != 1 {
				break
			}
			rule, _ := rules[0].(map[string]any)
			readerRoleOK = fmt.Sprint(rule["apiGroups"]) == "[core.orka.ai]" &&
				fmt.Sprint(rule["resources"]) == "[outboundaccesspolicies]" &&
				fmt.Sprint(rule["resourceNames"]) == "["+quickstartK8sToolPolicy+"]" &&
				fmt.Sprint(rule["verbs"]) == "[get]"
		case document["kind"] == "RoleBinding" && metadata["name"] == "kmx-k8s-tool-policy-reader":
			ref, _ := document["roleRef"].(map[string]any)
			subjects, _ := document["subjects"].([]any)
			if len(subjects) != 1 {
				break
			}
			subject, _ := subjects[0].(map[string]any)
			readerBindingOK = ref["kind"] == "Role" && ref["name"] == "kmx-k8s-tool-policy-reader" &&
				subject["kind"] == "ServiceAccount" && subject["name"] == "orka-ai-worker" &&
				subject["namespace"] == OrkaNamespace
		}
	}
	if !policyOK {
		t.Fatal("OutboundAccessPolicy does not route to the exact managed Tool Service")
	}
	if !toolOK {
		t.Fatal("Tool does not reference the exact managed gateway policy and authority")
	}
	if !readerRoleOK || !readerBindingOK {
		t.Fatalf("v0.2.0 AI worker cannot read only the managed gateway policy (role=%t binding=%t)", readerRoleOK, readerBindingOK)
	}
}

func quickstartDocuments(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	var documents []map[string]any
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err == io.EOF {
			return documents
		} else if err != nil {
			t.Fatal(err)
		}
		if document == nil {
			t.Fatal("empty YAML document")
		}
		documents = append(documents, document)
	}
}

func TestRenderQuickstartK8sToolChangesOnlyOneBindingSubject(t *testing.T) {
	body, err := manifest("orka-k8s-tool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, worker := range []string{"orka-ai-worker", "orka-api-ai-worker"} {
		t.Run(worker, func(t *testing.T) {
			output, err := renderQuickstartK8sTool(body, worker)
			if err != nil {
				t.Fatal(err)
			}
			original := quickstartDocuments(t, body)
			rendered := quickstartDocuments(t, output)
			if len(original) != len(rendered) || len(rendered) != 9 {
				t.Fatalf("document count before=%d after=%d, want 9", len(original), len(rendered))
			}
			bindings := 0
			for i, document := range original {
				metadata := document["metadata"].(map[string]any)
				if document["kind"] == "RoleBinding" && metadata["name"] == "kmx-k8s-tool-policy-reader" {
					bindings++
					subjects := rendered[i]["subjects"].([]any)
					if len(subjects) != 1 {
						t.Fatalf("RoleBinding subjects=%v, want one", subjects)
					}
					subject := subjects[0].(map[string]any)
					if subject["name"] != worker || subject["kind"] != "ServiceAccount" || subject["namespace"] != OrkaNamespace {
						t.Fatalf("unexpected RoleBinding subject: %v", subject)
					}
					// Compare decoded values, not bytes: only this subject may change.
					document["subjects"].([]any)[0].(map[string]any)["name"] = worker
				}
				if !reflect.DeepEqual(document, rendered[i]) {
					t.Fatalf("document %d changed outside the worker subject", i)
				}
			}
			if bindings != 1 || strings.Contains(string(output), "kmx-ai-worker-service-account-placeholder") {
				t.Fatalf("rendered %d RoleBindings or retained worker placeholder", bindings)
			}
		})
	}
}

func TestRenderQuickstartK8sToolRejectsUnsafeInputs(t *testing.T) {
	body, err := manifest("orka-k8s-tool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, worker string
		resources    []byte
	}{
		{"empty name", "", body},
		{"invalid name", "INVALID", body},
		{"yaml injection", "worker\nkind: Secret", body},
		{"missing placeholder", "orka-ai-worker", bytes.ReplaceAll(body, []byte("kmx-ai-worker-service-account-placeholder"), []byte("other-worker"))},
		{"duplicate placeholder", "orka-ai-worker", append(append([]byte{}, body...), []byte("\n# kmx-ai-worker-service-account-placeholder\n")...)},
		{"placeholder outside binding", "orka-ai-worker", bytes.Replace(bytes.Replace(body, []byte("name: kmx-ai-worker-service-account-placeholder"), []byte("name: orka-api-ai-worker"), 1), []byte("name: kmx-k8s-reader"), []byte("name: kmx-ai-worker-service-account-placeholder"), 1)},
		{"malformed YAML", "orka-ai-worker", []byte("name: kmx-ai-worker-service-account-placeholder\n: broken")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if result, err := renderQuickstartK8sTool(tt.resources, tt.worker); err == nil {
				t.Fatalf("accepted unsafe input; rendered %d bytes", len(result))
			}
		})
	}
}

func TestQuickstartK8sToolInstallDiscoversWorkerBeforeWrites(t *testing.T) {
	for _, tt := range []struct{ name, deployments, account, want string }{
		{"stock Helm worker", workerController("controller", "orka-0.2.0", "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`), workerAccount("orka-ai-worker", OrkaNamespace, "orka-0.2.0", "orka", "ai"), "orka-ai-worker"},
		{"absent controller", "", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			steps := filepath.Join(dir, "steps")
			applied := filepath.Join(dir, "applied")
			fakeTool(t, dir, "kubectl", fmt.Sprintf(`
case " $* " in
  *" get deploy -o json "*) printf 'deploy\n' >> %[1]q; printf '%%s' '{"items":[%[3]s]}' ;;
  *" get serviceaccounts -o json "*) printf 'accounts\n' >> %[1]q; printf '%%s' '{"items":[%[4]s]}' ;;
  *" apply -f - "*) printf 'apply\n' >> %[1]q; /bin/cat > %[2]q ;;
  *" rollout status "*) printf 'rollout\n' >> %[1]q ;;
  *" get outboundaccesspolicies.core.orka.ai "*) printf 'policy\n' >> %[1]q; printf '%%s' '{"metadata":{"generation":1},"status":{"conditions":[{"type":"Accepted","status":"True","observedGeneration":1}]}}' ;;
  *" get tools.core.orka.ai "*) printf 'tool\n' >> %[1]q; printf '%%s' '{"metadata":{"generation":1},"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":1}]}}' ;;
  *" create --raw /apis/authorization.k8s.io/v1/subjectaccessreviews -f - "*) printf 'sar\n' >> %[1]q; /bin/cat >/dev/null; printf '%%s' '{"status":{"allowed":true}}' ;;
  *) exit 1 ;;
esac`, steps, applied, tt.deployments, tt.account))
			t.Setenv("PATH", dir)
			a := &App{Cfg: &config.Config{KubeContext: "kind-demo"}, Run: &run.Runner{}, Err: io.Discard}
			err := a.installQuickstartK8sTool()
			if tt.want == "" {
				if err == nil {
					t.Fatal("missing controller allowed Tool install")
				}
				stepsBody, readErr := os.ReadFile(steps)
				if readErr != nil || string(stepsBody) != "deploy\n" {
					t.Fatalf("Tool resources applied before discovery: steps=%q error=%v", stepsBody, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			stepsBody, err := os.ReadFile(steps)
			if err != nil || string(stepsBody) != "deploy\naccounts\napply\napply\nrollout\npolicy\nsar\ntool\n" {
				t.Fatalf("wrong install order: steps=%q error=%v", stepsBody, err)
			}
			body, err := os.ReadFile(applied)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, doc := range quickstartDocuments(t, body) {
				if doc["kind"] == "RoleBinding" && doc["metadata"].(map[string]any)["name"] == "kmx-k8s-tool-policy-reader" {
					subjects := doc["subjects"].([]any)
					found = len(subjects) == 1 && subjects[0].(map[string]any)["name"] == tt.want
				}
			}
			if !found || strings.Contains(string(body), "kmx-tool-code-checksum") {
				t.Fatal("applied binding omitted discovered worker or code checksum was not rendered")
			}
		})
	}
}

func TestQuickstartK8sToolInstallMissingGrantReportsDenial(t *testing.T) {
	dir := t.TempDir()
	steps := filepath.Join(dir, "steps")
	fakeTool(t, dir, "kubectl", fmt.Sprintf(`case " $* " in
  *" get deploy -o json "*) printf '%%s' '{"items":[%s]}' ;;
  *" get serviceaccounts -o json "*) printf '%%s' '{"items":[%s]}' ;;
  *" apply -f - "*) printf 'apply\n' >> %[3]q; /bin/cat >/dev/null ;;
  *" rollout status "*) exit 0 ;;
  *" get outboundaccesspolicies.core.orka.ai "*) printf '%%s' '{"metadata":{"generation":1},"status":{"conditions":[{"type":"Accepted","status":"True","observedGeneration":1}]}}' ;;
  *" create --raw /apis/authorization.k8s.io/v1/subjectaccessreviews -f - "*) printf 'sar\n' >> %[3]q; /bin/cat >/dev/null; printf '%%s' '{"status":{"allowed":false,"denied":false}}' ;;
  *" get tools.core.orka.ai "*) printf 'tool\n' >> %[3]q; exit 1 ;;
  *) exit 1 ;;
esac`, workerController("controller", "orka-0.2.0", "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`), workerAccount("orka-ai-worker", OrkaNamespace, "orka-0.2.0", "orka", "ai"), steps))
	t.Setenv("PATH", dir)
	a := &App{Cfg: &config.Config{KubeContext: "kind-demo"}, Run: &run.Runner{}, Err: io.Discard}
	err := a.installQuickstartK8sTool()
	var denial *policyPermissionDenied
	if !errors.As(err, &denial) || denial.Worker != "orka-ai-worker" || denial.Namespace != OrkaNamespace || denial.Policy != quickstartK8sToolPolicy || strings.Contains(err.Error(), "indeterminate") {
		t.Fatalf("missing grant was not reported as a named permission denial: %v", err)
	}
	sequence, _ := os.ReadFile(steps)
	if string(sequence) != "apply\napply\nsar\n" {
		t.Fatalf("grant/SAR order or unauthorized success: %q", sequence)
	}
}

func TestWaitOrkaResourceConditionRejectsAStaleGeneration(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	fakeTool(t, dir, "kubectl", fmt.Sprintf(`
count=0
[ ! -f %[1]q ] || count=$(/bin/cat %[1]q)
count=$((count + 1))
printf '%%s' "$count" > %[1]q
if [ "$count" -eq 1 ]; then
  printf '%%s' '{"metadata":{"generation":2},"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":1}]}}'
else
  printf '%%s' '{"metadata":{"generation":2},"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":2}]}}'
fi
`, countFile))
	t.Setenv("PATH", dir)
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-demo"},
		Run: &run.Runner{},
	}

	if err := a.waitOrkaResourceCondition("tools.core.orka.ai", quickstartK8sTool, "Available"); err != nil {
		t.Fatal(err)
	}
	count, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "2" {
		t.Fatalf("status reads = %s, want 2; stale generation was accepted", count)
	}
}

func TestQuickstartToolDefaultAndCustomInstructions(t *testing.T) {
	opt := CreateOptions{Name: "demo", Description: "My agent", Namespace: OrkaNamespace, ProviderType: "openai", Model: "test", Secret: "key"}
	quickstartAgentTools(&opt)
	if err := finishCreateWizardOptions(&opt); err != nil {
		t.Fatal(err)
	}
	if opt.Tools != quickstartK8sTool || !strings.Contains(opt.InstructionText, "My agent") || !strings.Contains(opt.InstructionText, quickstartK8sInstructions) {
		t.Fatalf("default options=%+v", opt)
	}
	opt.Tools, opt.InstructionText = "web_fetch", "Custom instructions"
	quickstartAgentTools(&opt)
	if opt.Tools != "web_fetch" || opt.InstructionText != "Custom instructions" {
		t.Fatal("custom configuration replaced")
	}
}
