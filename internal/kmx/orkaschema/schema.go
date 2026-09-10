// Package orkaschema validates whole Orka custom resources against installed
// CRDs or immutable embedded CRDs. It performs no network or cluster I/O and
// does not evaluate Kubernetes CEL, defaulting, admission or reconciliation.
package orkaschema

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"go.yaml.in/yaml/v3"
)

//go:embed fixtures/*/*.yaml
var fixtures embed.FS

var pins = map[string]string{
	"v0.1.3": "b07d42c0b9e52fe511b434827a342b4720f5d422",
	"main":   "7c4753c2c68a510112ea2bb25b60a406d9c45686",
}

var resourceKinds = []string{"Agent", "Provider", "Task"}

// Validator uses the same compiled schemas and unknown-field policy for both
// offline and installed CRDs. Secret skeletons are checked by scaffold, not here.
type Validator struct {
	schemas    map[string]*jsonschema.Schema
	provenance string
}

// Offline selects a bundled snapshot; an empty target means v0.1.3. "main" is
// the immutable commit recorded below, never a request to fetch a moving branch.
func Offline(target string) (*Validator, error) {
	if target == "" {
		target = "v0.1.3"
	}
	pin, ok := pins[target]
	if !ok {
		return nil, fmt.Errorf("unknown offline Orka schema target; choose v0.1.3 or main")
	}
	crds := make(map[string][]byte)
	for _, resource := range resourceKinds {
		data, err := fixtures.ReadFile("fixtures/" + target + "/" + strings.ToLower(resource) + "s.yaml")
		if err != nil {
			return nil, fmt.Errorf("load bundled %s CRD: %w", resource, err)
		}
		crds[resource] = data
	}
	v, err := Installed(crds)
	if err != nil {
		return nil, err
	}
	v.provenance = "Orka " + target + " at " + pin + " (embedded CRDs; https://github.com/orka-agents/orka/tree/" + pin + "/config/crd/bases)"
	return v, nil
}

// Installed compiles the served v1alpha1 schemas from all three named CRDs.
// Missing, malformed, wrong-group or unserved CRDs are refusals, not fallbacks.
func Installed(crds map[string][]byte) (*Validator, error) {
	v := &Validator{schemas: make(map[string]*jsonschema.Schema), provenance: "installed Orka CRDs, served core.orka.ai/v1alpha1"}
	for _, resource := range resourceKinds {
		data, ok := crds[resource]
		if !ok || len(data) == 0 {
			return nil, fmt.Errorf("%s CRD is missing; install/read its served core.orka.ai/v1alpha1 schema", resource)
		}
		schema, err := servedSchema(data, resource)
		if err != nil {
			return nil, fmt.Errorf("%s CRD: %w", resource, err)
		}
		if err := normalizeSchema(schema); err != nil {
			return nil, fmt.Errorf("%s CRD schema: %w", resource, err)
		}
		value, err := jsonValue(schema)
		if err != nil {
			return nil, fmt.Errorf("%s CRD schema is not JSON-compatible: %w", resource, err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft7)
		// Compiler defaults permit file reads. Disable ALL external resolution,
		// including file URLs, even for installed schemas and remote $refs.
		compiler.UseLoader(nil)
		compiler.AssertFormat()
		location := "https://schemas.orka.invalid/" + resource + ".json"
		if err := compiler.AddResource(location, value); err != nil {
			return nil, fmt.Errorf("register %s CRD schema: %w", resource, err)
		}
		compiled, err := compiler.Compile(location)
		if err != nil {
			return nil, fmt.Errorf("compile %s CRD schema: %w", resource, err)
		}
		v.schemas[resource] = compiled
	}
	return v, nil
}

func servedSchema(data []byte, resource string) (map[string]any, error) {
	var crd struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Group string `yaml:"group"`
			Scope string `yaml:"scope"`
			Names struct {
				Kind   string `yaml:"kind"`
				Plural string `yaml:"plural"`
			} `yaml:"names"`
			Versions []struct {
				Name   string `yaml:"name"`
				Served bool   `yaml:"served"`
				Schema struct {
					OpenAPI map[string]any `yaml:"openAPIV3Schema"`
				} `yaml:"schema"`
			} `yaml:"versions"`
		} `yaml:"spec"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&crd); err != nil {
		return nil, fmt.Errorf("decode CRD: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("expected exactly one CRD document")
	}
	plural := strings.ToLower(resource) + "s"
	if crd.APIVersion != "apiextensions.k8s.io/v1" || crd.Kind != "CustomResourceDefinition" ||
		crd.Spec.Group != "core.orka.ai" || crd.Spec.Scope != "Namespaced" ||
		crd.Spec.Names.Kind != resource || crd.Spec.Names.Plural != plural || crd.Metadata.Name != plural+".core.orka.ai" {
		return nil, fmt.Errorf("expected the namespaced %s.core.orka.ai apiextensions.k8s.io/v1 CRD", plural)
	}
	var selected map[string]any
	for _, version := range crd.Spec.Versions {
		if version.Name != "v1alpha1" {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("duplicate v1alpha1 schema")
		}
		if !version.Served {
			return nil, fmt.Errorf("v1alpha1 is not served")
		}
		selected = version.Schema.OpenAPI
		properties, _ := selected["properties"].(map[string]any)
		if selected["type"] != "object" || len(properties) == 0 {
			return nil, fmt.Errorf("v1alpha1 needs a nonempty object openAPIV3Schema")
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("served v1alpha1 openAPIV3Schema is missing")
	}
	return selected, nil
}

// normalizeSchema adapts OpenAPI schema nodes, not arbitrary maps such as
// defaults or examples. Explicit properties are closed unless the CRD allows
// additional fields; Kubernetes' implicit metadata object stays open.
func normalizeSchema(schema map[string]any) error {
	for _, keyword := range []string{"properties", "patternProperties", "definitions", "$defs"} {
		children, _ := schema[keyword].(map[string]any)
		for _, child := range children {
			if node, ok := child.(map[string]any); ok {
				if err := normalizeSchema(node); err != nil {
					return err
				}
			}
		}
	}
	for _, keyword := range []string{"items", "additionalProperties", "additionalItems", "not", "if", "then", "else", "allOf", "anyOf", "oneOf"} {
		switch child := schema[keyword].(type) {
		case map[string]any:
			if err := normalizeSchema(child); err != nil {
				return err
			}
		case []any:
			for _, item := range child {
				if node, ok := item.(map[string]any); ok {
					if err := normalizeSchema(node); err != nil {
						return err
					}
				}
			}
		}
	}
	if _, hasProperties := schema["properties"].(map[string]any); hasProperties {
		if _, explicit := schema["additionalProperties"]; !explicit && schema["x-kubernetes-preserve-unknown-fields"] != true {
			schema["additionalProperties"] = false
		}
	}
	if schema["nullable"] == true {
		if typ, ok := schema["type"].(string); ok {
			schema["type"] = []any{typ, "null"}
		}
	}
	// Kubernetes numeric formats are not standard JSON Schema formats. Add
	// bounds as an intersection to retain any stricter upstream constraints.
	var bounds map[string]any
	switch schema["format"] {
	case "int32":
		bounds = map[string]any{"minimum": json.Number("-2147483648"), "maximum": json.Number("2147483647")}
	case "int64":
		bounds = map[string]any{"minimum": json.Number("-9223372036854775808"), "maximum": json.Number("9223372036854775807")}
	}
	if bounds != nil {
		allOf, ok := schema["allOf"].([]any)
		if _, exists := schema["allOf"]; exists && !ok {
			return fmt.Errorf("allOf must be an array")
		}
		schema["allOf"] = append(allOf, bounds)
	}
	return nil
}

// Validate checks the whole document, including all nested requested fields.
// It refuses Secret and other kinds rather than claiming they were CRD-checked.
func (v *Validator) Validate(document map[string]any) error {
	resource, _ := document["kind"].(string)
	compiled := v.schemas[resource]
	if compiled == nil {
		return fmt.Errorf("kind must be one of the validated Orka custom resources: Agent, Provider, Task (Secret is checked separately)")
	}
	if document["apiVersion"] != "core.orka.ai/v1alpha1" {
		return fmt.Errorf("%s.apiVersion must be core.orka.ai/v1alpha1", resource)
	}
	value, err := jsonValue(document)
	if err != nil {
		return fmt.Errorf("%s is not a JSON-compatible resource: %w", resource, err)
	}
	if err := compiled.Validate(value); err != nil {
		var validation *jsonschema.ValidationError
		if !errors.As(err, &validation) {
			return fmt.Errorf("validate %s: %w", resource, err)
		}
		messages := validationMessages(validation)
		slices.Sort(messages)
		return fmt.Errorf("%s: %s", resource, strings.Join(slices.Compact(messages), "; "))
	}
	return nil
}

func jsonValue(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	// UseNumber preserves int64 limits across the Go/YAML/JSON boundary.
	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

func validationMessages(err *jsonschema.ValidationError) []string {
	if len(err.Causes) > 0 {
		var messages []string
		for _, cause := range err.Causes {
			messages = append(messages, validationMessages(cause)...)
		}
		return messages
	}
	path := strings.Join(err.InstanceLocation, ".")
	if path == "" {
		path = "document"
	}
	switch failure := err.ErrorKind.(type) {
	case *kind.AdditionalProperties:
		messages := make([]string, 0, len(failure.Properties))
		for _, property := range failure.Properties {
			messages = append(messages, path+"."+property+": field is not supported by the selected schema")
		}
		return messages
	case *kind.Required:
		messages := make([]string, 0, len(failure.Missing))
		for _, property := range failure.Missing {
			messages = append(messages, path+"."+property+": required field is missing")
		}
		return messages
	default:
		// Report location and constraint without echoing arbitrary document
		// values (enum and pattern errors can otherwise disclose inputs).
		return []string{path + ": schema constraint " + strings.Join(err.ErrorKind.KeywordPath(), ".") + " failed"}
	}
}

// Provenance identifies where the schemas came from, not a runtime guarantee.
func (v *Validator) Provenance() string { return v.provenance }
