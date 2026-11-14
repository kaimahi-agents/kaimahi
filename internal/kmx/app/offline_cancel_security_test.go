package app

import (
	"bytes"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Exercise the new chart defaults through the values kmx actually sends to Helm.
// This checks rendered grants, not admission execution or live RBAC drift.
func offlineAssertCancelSecurity(t *testing.T, raw []byte, args []string) {
	t.Helper()
	for _, flag := range []string{
		"--task-webhook-signing-secret=orka-api-task-webhook-signing",
		"--task-webhook-signing-namespace=orka-system-task-webhook-signing",
	} {
		count := 0
		for _, arg := range args {
			if strings.HasPrefix(arg, strings.SplitN(flag, "=", 2)[0]+"=") {
				if arg != flag {
					t.Fatal("unexpected signing flag", arg)
				}
				count++
			}
		}
		if count != 1 {
			t.Fatal("missing/duplicate signing flag", flag, count)
		}
	}
	type rule struct {
		APIGroups     []string `yaml:"apiGroups"`
		Resources     []string
		ResourceNames []string `yaml:"resourceNames"`
		Verbs         []string
	}
	type subject struct{ Kind, Name, Namespace string }
	found := map[string]int{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var obj struct {
			Kind     string
			Metadata struct {
				Name, Namespace     string
				Labels, Annotations map[string]string
			}
			Rules   []rule
			RoleRef struct {
				APIGroup   string `yaml:"apiGroup"`
				Kind, Name string
			} `yaml:"roleRef"`
			Subjects []subject
			Spec     struct {
				FailurePolicy     string   `yaml:"failurePolicy"`
				PolicyName        string   `yaml:"policyName"`
				ValidationActions []string `yaml:"validationActions"`
			}
		}
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch obj.Kind + "/" + obj.Metadata.Name {
		case "Namespace/orka-system-task-webhook-signing":
			for _, mode := range []string{"enforce", "warn", "audit"} {
				if obj.Metadata.Labels["pod-security.kubernetes.io/"+mode] != "restricted" {
					t.Fatal("signing namespace lost restricted policy", mode)
				}
			}
			if obj.Metadata.Annotations["helm.sh/resource-policy"] != "keep" {
				t.Fatal("signing namespace not retained")
			}
		case "Role/orka-api-controller-task-webhook-signing":
			want := []rule{{[]string{""}, []string{"secrets"}, []string{"orka-api-task-webhook-signing"}, []string{"get", "update"}}, {[]string{""}, []string{"secrets"}, nil, []string{"create"}}}
			if obj.Metadata.Namespace != "orka-system-task-webhook-signing" || !reflect.DeepEqual(obj.Rules, want) {
				t.Fatal("signing key grant is not isolated", obj.Rules)
			}
		case "RoleBinding/orka-api-controller-task-webhook-signing":
			if obj.Metadata.Namespace != "orka-system-task-webhook-signing" || obj.RoleRef.APIGroup != "rbac.authorization.k8s.io" || obj.RoleRef.Kind != "Role" || obj.RoleRef.Name != "orka-api-controller-task-webhook-signing" || !reflect.DeepEqual(obj.Subjects, []subject{{"ServiceAccount", "orka-api", "orka-system"}}) {
				t.Fatal("signing grant is not controller-only", obj)
			}
		case "Role/orka-api-client":
			cancel := 0
			for _, r := range obj.Rules {
				if slices.Contains(r.Resources, "tasks/cancel") {
					if obj.Metadata.Namespace != OrkaNamespace || !slices.Equal(r.APIGroups, []string{"core.orka.ai"}) || !slices.Equal(r.Verbs, []string{"update"}) {
						t.Fatal("incorrect Task cancel grant", r)
					}
					cancel++
				}
			}
			if cancel != 1 {
				t.Fatal("missing/duplicate Task cancel grant", cancel)
			}
		case "ValidatingAdmissionPolicy/orka-api-ai-worker-identity-protection":
			if obj.Spec.FailurePolicy != "Fail" {
				t.Fatal("AI identity policy does not fail closed")
			}
		case "ValidatingAdmissionPolicyBinding/orka-api-ai-worker-identity-protection":
			if obj.Spec.PolicyName != "orka-api-ai-worker-identity-protection" || !slices.Equal(obj.Spec.ValidationActions, []string{"Deny"}) {
				t.Fatal("AI identity policy not enforced")
			}
		default:
			continue
		}
		found[obj.Kind]++
	}
	for _, kind := range []string{"Namespace", "RoleBinding", "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "Role"} {
		want := 1
		if kind == "Role" {
			want = 2
		}
		if found[kind] != want {
			t.Fatal("missing/duplicate security resource", kind, found[kind])
		}
	}
}
