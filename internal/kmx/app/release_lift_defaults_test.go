package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestReleaseLiftPinnedCRDDefaultsPreserveClosedSpec(t *testing.T) {
	_, _, _, p := releaseLiftFixture(t)
	for _, desired := range []map[string]any{p.bundle.Provider, p.bundle.Agent} {
		kind := desired["kind"].(string)
		raw, err := os.ReadFile(filepath.Join("..", "orkaschema", "fixtures", "v0.2.0", strings.ToLower(kind)+"s.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var crd map[string]any
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			t.Fatal(err)
		}
		version := crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)
		root := version["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
		schema := root["properties"].(map[string]any)["spec"].(map[string]any)
		admitted, err := cloneOrkaDoc(desired)
		if err != nil {
			t.Fatal(err)
		}
		applyFixtureDefaults(t, admitted["spec"].(map[string]any), schema)
		if !reflect.DeepEqual(admitted["spec"], desired["spec"]) {
			t.Fatalf("%s closed spec gains pinned CRD defaults", kind)
		}
		addOrkaMarker(admitted, suiteNativeOwner(p.Subject).markers())
		body, err := json.Marshal(admitted)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := releaseLiftAdmitted(p.Subject, desired, body, false); err != nil {
			t.Fatalf("normal defaulting refused: %v", err)
		}
	}
}

// Fixture admission walks defaults only below present object parents. A missing
// optional parent is not materialized unless that parent itself has a default.
func applyFixtureDefaults(t *testing.T, object, schema map[string]any) {
	t.Helper()
	properties, _ := schema["properties"].(map[string]any)
	for name, raw := range properties {
		property := raw.(map[string]any)
		value, present := object[name]
		if !present {
			if def, ok := property["default"]; ok {
				body, err := json.Marshal(def)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(body, &value); err != nil {
					t.Fatal(err)
				}
				object[name], present = value, true
			}
		}
		if present {
			if child, ok := value.(map[string]any); ok {
				applyFixtureDefaults(t, child, property)
			}
		}
	}
}
