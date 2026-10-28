package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func splitCandidateChart(t *testing.T, namespace string) []byte {
	t.Helper()
	return candidateOrkaArchive(t, func(files map[string]string) {
		values := candidateOrkaValues(t)
		values["controller"].(map[string]any)["watchNamespace"] = namespace
		body, err := yaml.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		files["orka/values.yaml"] = string(body)
	})
}

func TestOrkaSplitSetupWritesOnlyTaskLocalPrincipalsAndProvider(t *testing.T) {
	const namespace = "candidate-tasks"
	chart := splitCandidateChart(t, namespace)
	f := newOrkaFixture(t, nil)
	dir := t.TempDir()
	t.Setenv("KMX_TEST_APPLIES", dir)
	t.Setenv("KMX_TEST_NAMESPACE_JSON", `{"kind":"Namespace","metadata":{"name":"candidate-tasks","uid":"tasks-1"}}`)
	opt := candidateOrkaOptions(t, chart)
	opt.Provider = "local"
	opt.ModelURL = "http://model.example.invalid/v1"
	if err := f.app.OrkaInstall(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.calls(t), "controller.watchNamespace="+namespace) {
		t.Fatal("Helm did not receive the frozen Task namespace")
	}
	if !strings.Contains(f.calls(t), "-n "+namespace+" get "+orkaProviderKind+" local ") {
		t.Fatal("Provider lookup was not Task-local")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool)
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(body))
		for {
			var doc struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Namespace string `yaml:"namespace"`
				} `yaml:"metadata"`
				Subjects []struct {
					Namespace string `yaml:"namespace"`
				} `yaml:"subjects"`
			}
			if err := decoder.Decode(&doc); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if doc.Kind == "CustomResourceDefinition" {
				continue
			}
			if doc.Metadata.Namespace != namespace {
				t.Fatalf("%s was written outside the Task namespace: %+v", doc.Kind, doc)
			}
			for _, subject := range doc.Subjects {
				if subject.Namespace != namespace {
					t.Fatal("result RoleBinding refers to a management principal")
				}
			}
			found[doc.Kind] = true
		}
	}
	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "Secret", "Provider"} {
		if !found[kind] {
			t.Errorf("missing Task-local %s", kind)
		}
	}
}

func TestOrkaSplitSetupRefusesMissingOrInvalidNamespaceBeforeWrites(t *testing.T) {
	for _, raw := range []string{
		"", "not-json", `{"kind":"Namespace","metadata":{"name":"other","uid":"1"}}`,
		`{"kind":"Namespace","metadata":{"name":"candidate-tasks"}}`,
		`{"kind":"Namespace","metadata":{"name":"candidate-tasks","uid":"1","deletionTimestamp":"2026-10-25T19:00:00Z"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			chart := splitCandidateChart(t, "candidate-tasks")
			f := newOrkaFixture(t, nil)
			t.Setenv("KMX_TEST_NAMESPACE_JSON", raw)
			if err := f.app.OrkaInstall(candidateOrkaOptions(t, chart)); err == nil {
				t.Fatal("unavailable Task namespace was accepted")
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("unavailable namespace caused writes: %s", writes)
			}
		})
	}
}

func TestOrkaSplitSetupNoApplyRequiresNoNamespaceAccess(t *testing.T) {
	chart := splitCandidateChart(t, "candidate-tasks")
	f := newOrkaFixture(t, nil)
	opt := candidateOrkaOptions(t, chart)
	opt.NoApply = true
	if err := f.app.OrkaInstall(opt); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.calls(t), "get namespace ") {
		t.Fatal("no-apply inspected cluster namespace state")
	}
	if writes := f.writes(t); writes != "" {
		t.Fatalf("no-apply wrote resources: %s", writes)
	}
}
