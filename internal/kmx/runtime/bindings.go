package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// OrkaBindings describes only the target where this agent was created. A
// different deployment target supplies its own bindings at render time.
type OrkaBindings struct {
	APIVersion string               `yaml:"apiVersion"`
	Kind       string               `yaml:"kind"`
	Namespace  string               `yaml:"namespace"`
	Provider   OrkaProviderBindings `yaml:"provider"`
}

type OrkaProviderBindings struct {
	Type      string                `yaml:"type"`
	BaseURL   string                `yaml:"baseURL,omitempty"`
	SecretRef OrkaSecretRefBindings `yaml:"secretRef"`
}

type OrkaSecretRefBindings struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key,omitempty"`
}

// ParseOrkaBindings accepts exactly one closed, unambiguous document. All
// credential scans precede any parsing/validation error that might quote data.
func ParseOrkaBindings(data []byte) (OrkaBindings, error) {
	if err := refusePortableSecretShape(string(data)); err != nil {
		return OrkaBindings{}, err
	}
	if !utf8.Valid(data) {
		return OrkaBindings{}, fmt.Errorf("Orka bindings must be valid UTF-8")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return OrkaBindings{}, fmt.Errorf("Orka bindings must be valid YAML: %w", err)
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return OrkaBindings{}, fmt.Errorf("Orka bindings must contain exactly one YAML document")
	default:
		return OrkaBindings{}, fmt.Errorf("Orka bindings must contain exactly one YAML document: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return OrkaBindings{}, fmt.Errorf("Orka bindings must be one YAML mapping")
	}
	if err := refusePortableDecodedSecretShapes(root.Content[0]); err != nil {
		return OrkaBindings{}, err
	}
	if err := rejectPortableKeyHazards(root.Content[0], ""); err != nil {
		return OrkaBindings{}, fmt.Errorf("Orka bindings: %w", err)
	}
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var bindings OrkaBindings
	if err := strict.Decode(&bindings); err != nil {
		return OrkaBindings{}, fmt.Errorf("Orka bindings: %w", err)
	}
	if bindings.APIVersion != PortableAPIVersion || bindings.Kind != "OrkaBindings" {
		return OrkaBindings{}, fmt.Errorf("Orka bindings require apiVersion %q and kind OrkaBindings", PortableAPIVersion)
	}
	if err := bindings.validate(); err != nil {
		return OrkaBindings{}, fmt.Errorf("Orka bindings: %w", err)
	}
	return bindings, nil
}

// EncodeOrkaBindings writes a deterministic creation-target document. The
// comment explicitly prevents mistaking these references for portable policy.
func EncodeOrkaBindings(bindings OrkaBindings) ([]byte, error) {
	if bindings.APIVersion == "" {
		bindings.APIVersion = PortableAPIVersion
	}
	if bindings.Kind == "" {
		bindings.Kind = "OrkaBindings"
	}
	if err := bindings.validate(); err != nil {
		return nil, fmt.Errorf("Orka bindings: %w", err)
	}
	encoded, err := yaml.Marshal(bindings)
	if err != nil {
		return nil, fmt.Errorf("encode Orka bindings: %w", err)
	}
	return append([]byte("# Creation target only; other targets supply their own bindings.\n"), encoded...), nil
}

// ValidateOrkaBindings checks explicit render input as well as parsed files.
func ValidateOrkaBindings(bindings OrkaBindings) error {
	return bindings.validate()
}

func (b OrkaBindings) validate() error {
	fields := []struct{ path, value string }{
		{"apiVersion", b.APIVersion}, {"kind", b.Kind}, {"namespace", b.Namespace},
		{"provider.type", b.Provider.Type}, {"provider.baseURL", b.Provider.BaseURL},
		{"provider.secretRef.name", b.Provider.SecretRef.Name}, {"provider.secretRef.key", b.Provider.SecretRef.Key},
	}
	for _, field := range fields {
		if !utf8.ValidString(field.value) {
			return fmt.Errorf("%s must be valid UTF-8", field.path)
		}
	}
	for _, field := range fields {
		if err := refusePortableSecretShape(field.value); err != nil {
			return err
		}
	}
	if b.APIVersion != "" && b.APIVersion != PortableAPIVersion {
		return fmt.Errorf("apiVersion must be %q", PortableAPIVersion)
	}
	if b.Kind != "" && b.Kind != "OrkaBindings" {
		return fmt.Errorf("kind must be OrkaBindings")
	}
	if err := scaffold.ValidateNamespace(b.Namespace); err != nil {
		return fmt.Errorf("namespace: %w", err)
	}
	if err := scaffold.ValidateOrkaProvider(b.Provider.Type, "bound-model", b.Provider.BaseURL); err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	if err := scaffold.ValidateObjectName(b.Provider.SecretRef.Name); err != nil {
		return fmt.Errorf("provider.secretRef.name: %w", err)
	}
	if b.Provider.SecretRef.Key != "" {
		if err := scaffold.ValidateOrkaSecretKey(b.Provider.SecretRef.Key); err != nil {
			return fmt.Errorf("provider.secretRef.key: %w", err)
		}
	}
	return nil
}
