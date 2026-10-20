package azure_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	kmxruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"go.yaml.in/yaml/v3"
)

func modelField(t *testing.T, node *yaml.Node, keys ...string) *yaml.Node {
	t.Helper()
	if node.Kind == yaml.DocumentNode {
		node = node.Content[0]
	}
	for _, key := range keys {
		var next *yaml.Node
		switch node.Kind {
		case yaml.MappingNode:
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == key {
					next = node.Content[i+1]
					break
				}
			}
		case yaml.SequenceNode:
			index, err := strconv.Atoi(key)
			if err == nil && index >= 0 && index < len(node.Content) {
				next = node.Content[index]
			}
		}
		if next == nil {
			t.Fatalf("missing template field %s", strings.Join(keys, "."))
		}
		node = next
	}
	return node
}

func modelDocuments(t *testing.T, path string) map[string]*yaml.Node {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]*yaml.Node)
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		kind := modelField(t, &node, "kind").Value
		if result[kind] != nil {
			t.Fatalf("duplicate kind %s in %s", kind, path)
		}
		result[kind] = &node
	}
	return result
}

func TestCPUModelTemplate(t *testing.T) {
	resources := modelDocuments(t, "aikit-cpu.yaml")
	if len(resources) != 3 {
		t.Fatalf("workload resource count = %d, want 3", len(resources))
	}
	for _, kind := range []string{"ConfigMap", "Deployment", "Service"} {
		node := resources[kind]
		if node == nil {
			t.Fatalf("missing %s", kind)
		}
		if got := modelField(t, node, "metadata", "namespace").Value; got != "kmx-inference" {
			t.Errorf("%s namespace = %q", kind, got)
		}
		if got := modelField(t, node, "metadata", "name").Value; got != "aikit-cpu" {
			t.Errorf("%s name = %q", kind, got)
		}
	}
	namespace := modelDocuments(t, "aikit-cpu-namespace.yaml")
	if len(namespace) != 1 || namespace["Namespace"] == nil {
		t.Fatal("namespace must be a separate single-resource template")
	}
	if got := modelField(t, namespace["Namespace"], "metadata", "name").Value; got != "kmx-inference" {
		t.Fatalf("namespace name = %q", got)
	}

	deployment := resources["Deployment"]
	pod := modelField(t, deployment, "spec", "template", "spec")
	container := modelField(t, pod, "containers", "0")
	for _, tc := range []struct {
		node *yaml.Node
		path []string
		want string
	}{
		{namespace["Namespace"], []string{"apiVersion"}, "v1"},
		{resources["ConfigMap"], []string{"apiVersion"}, "v1"},
		{deployment, []string{"apiVersion"}, "apps/v1"},
		{deployment, []string{"spec", "replicas"}, "1"},
		{deployment, []string{"spec", "strategy", "type"}, "Recreate"},
		{pod, []string{"automountServiceAccountToken"}, "false"},
		{pod, []string{"nodeSelector", "kubernetes.io/os"}, "linux"},
		{pod, []string{"nodeSelector", "kubernetes.io/arch"}, "amd64"},
		{container, []string{"args", "0"}, "--config-file=/etc/aikit/config.yaml"},
		{container, []string{"env", "0", "name"}, "LOCALAI_FORCE_META_BACKEND_CAPABILITY"},
		{container, []string{"env", "0", "value"}, "cpu"},
		{container, []string{"startupProbe", "httpGet", "path"}, "/readyz"},
		{container, []string{"startupProbe", "httpGet", "port"}, "http"},
		{container, []string{"startupProbe", "periodSeconds"}, "5"},
		{container, []string{"startupProbe", "failureThreshold"}, "120"},
		{container, []string{"readinessProbe", "httpGet", "path"}, "/readyz"},
		{container, []string{"readinessProbe", "httpGet", "port"}, "http"},
		{container, []string{"ports", "0", "name"}, "http"},
		{container, []string{"ports", "0", "containerPort"}, "8080"},
		{container, []string{"volumeMounts", "0", "name"}, "config"},
		{container, []string{"volumeMounts", "0", "mountPath"}, "/etc/aikit"},
		{container, []string{"volumeMounts", "0", "readOnly"}, "true"},
		{pod, []string{"volumes", "0", "name"}, "config"},
		{pod, []string{"volumes", "0", "configMap", "name"}, "aikit-cpu"},
		{deployment, []string{"spec", "selector", "matchLabels", "app.kubernetes.io/name"}, "aikit-cpu"},
		{deployment, []string{"spec", "template", "metadata", "labels", "app.kubernetes.io/name"}, "aikit-cpu"},
		{resources["Service"], []string{"apiVersion"}, "v1"},
		{resources["Service"], []string{"spec", "type"}, "ClusterIP"},
		{resources["Service"], []string{"spec", "selector", "app.kubernetes.io/name"}, "aikit-cpu"},
		{resources["Service"], []string{"spec", "ports", "0", "port"}, "8080"},
		{resources["Service"], []string{"spec", "ports", "0", "targetPort"}, "http"},
	} {
		if got := modelField(t, tc.node, tc.path...).Value; got != tc.want {
			t.Errorf("%s = %q, want %q", strings.Join(tc.path, "."), got, tc.want)
		}
	}
	if len(modelField(t, pod, "containers").Content) != 1 {
		t.Fatal("expected exactly one model container")
	}
	for kind, want := range map[string]map[string]string{
		"requests": {"cpu": "1", "memory": "3Gi", "ephemeral-storage": "1Gi"},
		"limits":   {"cpu": "3", "memory": "4Gi", "ephemeral-storage": "4Gi"},
	} {
		var got map[string]string
		if err := modelField(t, container, "resources", kind).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("resources.%s = %v, want %v", kind, got, want)
		}
	}

	fixture := modelDocuments(t, filepath.Join("..", "..", "..", "scripts", "ci", "native-suite-model.yaml"))
	wantImage := modelField(t, fixture["Deployment"], "spec", "template", "spec", "containers", "0", "image").Value
	if got := modelField(t, container, "image").Value; got != wantImage || !strings.Contains(got, "@sha256:") {
		t.Errorf("model image = %q, want existing digest pin %q", got, wantImage)
	}
	var gotConfig, wantConfig []map[string]any
	if err := yaml.Unmarshal([]byte(modelField(t, resources["ConfigMap"], "data", "config.yaml").Value), &gotConfig); err != nil {
		t.Fatal(err)
	}
	fixtureConfig, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "ci", "eval-loop-model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(fixtureConfig, &wantConfig); err != nil {
		t.Fatal(err)
	}
	if len(wantConfig) != 1 {
		t.Fatal("expected exactly one pinned CI model")
	}
	wantConfig[0]["threads"] = 3
	if !reflect.DeepEqual(gotConfig, wantConfig) {
		t.Errorf("model configuration must match the pinned CI preset with only threads changed to 3")
	}
}

func TestREADMEEvaluationFormat(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	const fence = "```yaml\nid: release-facts\n"
	start := bytes.Index(data, []byte(fence))
	if start < 0 {
		t.Fatal("release-facts YAML block not found")
	}
	start += len("```yaml\n")
	end := bytes.Index(data[start:], []byte("\n```"))
	if end < 0 {
		t.Fatal("release-facts YAML block is not closed")
	}
	caseDoc, err := kmxruntime.ParseEvaluationCase(data[start : start+end])
	if err != nil {
		t.Fatalf("release-facts YAML: %v", err)
	}
	var pattern string
	for _, assertion := range caseDoc.Assertions {
		if assertion.ID == "limitation-unresolved" {
			if pattern != "" {
				t.Fatal("duplicate limitation-unresolved assertion")
			}
			pattern = assertion.Pattern
		}
	}
	if pattern == "" {
		t.Fatal("missing limitation-unresolved assertion")
	}
	format, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("limitation-unresolved pattern: %v", err)
	}
	for _, test := range []struct {
		name   string
		answer string
		want   bool
	}{
		{"one limitation", "## Changes\n- Added: retry.\n- Fixed: search.\n\n## Known limitations\n- Unresolved: large CSV exports time out.\n", true},
		{"multiple limitations", "# Changes\n- Added: retry.\n\n# Known limitations\n- Unresolved: large CSV exports time out.\n\n- Unresolved: another limitation.\n", true},
		{"unprefixed limitation", "## Changes\n- Added: retry.\n\n## Known limitations\n- Large CSV exports time out.\n", false},
		{"mixed limitation prefixes", "## Changes\n- Added: retry.\n\n## Known limitations\n- Unresolved: one.\n- Another limitation.\n", false},
		{"prefix only under changes", "## Changes\n- Unresolved: retry issue.\n- Fixed: search.\n\n## Known limitations\n- Unresolved: large CSV exports time out.\n", false},
		{"wrong prefix case", "## Changes\n- Added: retry.\n\n## Known limitations\n- unresolved: large CSV exports time out.\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := format.MatchString(test.answer); got != test.want {
				t.Errorf("format match = %v, want %v", got, test.want)
			}
		})
	}
}
