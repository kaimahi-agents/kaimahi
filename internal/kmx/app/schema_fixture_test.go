package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

func loadFixtureCRDs() (map[string][]byte, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("cannot locate pinned Orka CRD fixtures")
	}
	crds := make(map[string][]byte, 3)
	for _, kind := range []string{"Agent", "Provider", "Task"} {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../orkaschema/fixtures/v0.1.3", strings.ToLower(kind)+"s.yaml"))
		if err != nil {
			return nil, err
		}
		crds[kind] = raw
	}
	return crds, nil
}

func readFixtureCRDs(t *testing.T) map[string][]byte {
	t.Helper()
	crds, err := loadFixtureCRDs()
	if err != nil {
		t.Fatal(err)
	}
	return crds
}

var fixtureOrkaSchemas struct {
	sync.Once
	compile func(map[string][]byte) (*orkaschema.Validator, error)
	err     error
}

func fixtureOrkaSchemaCompiler(t *testing.T) func(map[string][]byte) (*orkaschema.Validator, error) {
	t.Helper()
	fixtureOrkaSchemas.Do(func() {
		// These private source bytes are never caller-owned or returned. Only
		// the compiled read-only validator is shared between fixture Apps.
		canonical, err := loadFixtureCRDs()
		if err != nil {
			fixtureOrkaSchemas.err = err
			return
		}
		validator, err := orkaschema.Installed(canonical)
		if err != nil {
			fixtureOrkaSchemas.err = err
			return
		}
		fixtureOrkaSchemas.compile = func(crds map[string][]byte) (*orkaschema.Validator, error) {
			if len(crds) == len(canonical) {
				identical := true
				for kind, raw := range canonical {
					if !bytes.Equal(crds[kind], raw) {
						identical = false
						break
					}
				}
				if identical {
					return validator, nil
				}
			}
			return orkaschema.Installed(crds)
		}
	})
	if fixtureOrkaSchemas.err != nil {
		t.Fatal(fixtureOrkaSchemas.err)
	}
	return fixtureOrkaSchemas.compile
}

func TestFixtureOrkaSchemaReuse(t *testing.T) {
	compile := fixtureOrkaSchemaCompiler(t)
	// Even the first caller may supply different bytes; it must not seed
	// the canonical cache from arbitrary observed input.
	changed := readFixtureCRDs(t)
	changed["Agent"] = append(changed["Agent"], []byte("\n# caller-owned comment\n")...)
	independent, err := compile(changed)
	if err != nil {
		t.Fatal(err)
	}
	firstInputs := readFixtureCRDs(t)
	first, err := compile(firstInputs)
	if err != nil {
		t.Fatal(err)
	}
	// Caller-owned slices must never become the cache's canonical bytes.
	firstInputs["Agent"][0] ^= 1
	second, err := fixtureOrkaSchemaCompiler(t)(readFixtureCRDs(t))
	if err != nil {
		t.Fatal(err)
	}
	if first == independent {
		t.Fatal("noncanonical first input seeded the shared schema")
	}
	if first != second {
		t.Fatal("identical pinned CRDs did not reuse the compiled validator")
	}
	if _, err := compile(firstInputs); err == nil {
		t.Fatal("caller mutation incorrectly reused the canonical schema")
	}
}

func TestFixtureOrkaSchemaFallback(t *testing.T) {
	compile := fixtureOrkaSchemaCompiler(t)
	cached, err := compile(readFixtureCRDs(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "malformed", "modified", "extra"} {
		t.Run(mode, func(t *testing.T) {
			crds := readFixtureCRDs(t)
			switch mode {
			case "missing":
				delete(crds, "Task")
			case "malformed":
				crds["Agent"] = []byte("spec: [")
			case "modified":
				var crd map[string]any
				if err := yaml.Unmarshal(crds["Agent"], &crd); err != nil {
					t.Fatal(err)
				}
				version := crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)
				schema := version["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
				properties := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)
				properties["systemPrompt"].(map[string]any)["properties"].(map[string]any)["inline"].(map[string]any)["type"] = "boolean"
				crds["Agent"], err = yaml.Marshal(crd)
				if err != nil {
					t.Fatal(err)
				}
			case "extra":
				crds["Other"] = []byte("spec: [")
			}
			validator, err := compile(crds)
			if mode == "missing" || mode == "malformed" {
				if err == nil || validator != nil {
					t.Fatalf("invalid installed CRDs accepted: %v", err)
				}
				return
			}
			if err != nil || validator == cached {
				t.Fatalf("noncanonical CRDs did not compile independently: %v", err)
			}
			bundle := fixtureSchemaBundle(t)
			// Installed ignores extra keys, but must still check real resources.
			if mode == "extra" {
				bundle.Agent["spec"].(map[string]any)["systemPrompt"].(map[string]any)["inline"] = false
			}
			if err := validator.Validate(bundle.Agent); err == nil || !strings.Contains(err.Error(), "spec.systemPrompt.inline") {
				t.Fatalf("changed schema or invalid resource bypassed: %v", err)
			}
		})
	}
}

func fixtureSchemaBundle(t *testing.T) *scaffold.OrkaBundle {
	t.Helper()
	bundle, err := scaffold.GenerateOrka(scaffold.OrkaSpec{Name: "sample", Namespace: OrkaNamespace, Model: "test", Instructions: "Answer briefly.", ProviderType: "openai", SecretName: "model-key", TaskPrompt: "Say hello"})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestOrkaSchemaDefaultRecompilesObservedCRDs(t *testing.T) {
	a := &App{}
	crds := readFixtureCRDs(t)
	first, err := a.installedOrkaSchema(crds)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.installedOrkaSchema(crds)
	if err != nil || first == second {
		t.Fatalf("default compilation reused schema: %v", err)
	}
	delete(crds, "Task")
	if validator, err := a.installedOrkaSchema(crds); err == nil || validator != nil {
		t.Fatalf("default compilation bypassed changed observed CRDs: %v", err)
	}
}

func TestFixtureOrkaSchemaConsumers(t *testing.T) {
	a, suite, _ := nativeFixture(t)
	compile := fixtureOrkaSchemaCompiler(t)
	cached, err := compile(readFixtureCRDs(t))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	observe := func(crds map[string][]byte) (*orkaschema.Validator, error) {
		calls++
		validator, err := compile(crds)
		if err != nil || validator != cached {
			t.Fatalf("fixture consumer did not use canonical schema: %v", err)
		}
		return validator, err
	}
	a.compileOrkaSchema = observe
	prepared, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil || prepared == nil || calls != 1 {
		t.Fatalf("native preparation bypassed fixture compiler: calls=%d err=%v", calls, err)
	}
	adapter, _ := reconcileFixtureMode(t, true)
	adapter.app.compileOrkaSchema = observe
	checks, err := adapter.app.planOrkaPlacement(t.Context(), orkaPlacementIdentity{suite: suiteNativeOwner(prepared.Subject)}, prepared.bundle, OrkaNamespace)
	if err != nil || len(checks) != 2 || calls != 2 {
		t.Fatalf("reconcile planning bypassed fixture compiler: calls=%d checks=%d err=%v", calls, len(checks), err)
	}
}

func TestFixtureOrkaSchemaConcurrentValidation(t *testing.T) {
	validator, err := fixtureOrkaSchemaCompiler(t)(readFixtureCRDs(t))
	if err != nil {
		t.Fatal(err)
	}
	bundle := fixtureSchemaBundle(t)
	invalid := fixtureSchemaBundle(t).Agent
	invalid["spec"].(map[string]any)["systemPrompt"].(map[string]any)["inline"] = false
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 4 {
				for _, doc := range []map[string]any{bundle.Agent, bundle.Provider, bundle.Task} {
					if err := validator.Validate(doc); err != nil {
						t.Errorf("valid resource refused: %v", err)
					}
				}
				if err := validator.Validate(invalid); err == nil {
					t.Error("invalid resource accepted by shared validator")
				}
			}
		})
	}
	workers.Wait()
}
