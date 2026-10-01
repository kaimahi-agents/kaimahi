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

const (
	KagentBindingsAPIVersion = PortableAPIVersion
	KagentBindingsKind       = "KagentBindings"
	// DefaultKagentSecretKey is the one default used by shorthand and
	// bindings encoders. Parsers and validators never synthesize it.
	DefaultKagentSecretKey = scaffold.DefaultKagentSecretKey
)

// KagentBindings contains only the creation target omitted from portable
// behavior. It references a Secret name/key and cannot represent a value.
type KagentBindings struct {
	APIVersion  string                    `yaml:"apiVersion"`
	Kind        string                    `yaml:"kind"`
	Namespace   string                    `yaml:"namespace"`
	ModelConfig KagentModelConfigBindings `yaml:"modelConfig"`
}

type KagentModelConfigBindings struct {
	Provider  string                  `yaml:"provider"`
	BaseURL   string                  `yaml:"baseURL,omitempty"`
	SecretRef KagentSecretRefBindings `yaml:"secretRef"`
}

type KagentSecretRefBindings struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

// ParseKagentBindings accepts exactly one closed, unambiguous target document.
// Parsed documents must state the effective Secret key; defaults belong only
// to callers or EncodeKagentBindings.
func ParseKagentBindings(data []byte) (KagentBindings, error) {
	if err := refusePortableSecretShape(string(data)); err != nil {
		return KagentBindings{}, err
	}
	if !utf8.Valid(data) {
		return KagentBindings{}, fmt.Errorf("Kagent bindings must be valid UTF-8")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return KagentBindings{}, fmt.Errorf("Kagent bindings must be valid YAML: %w", err)
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return KagentBindings{}, fmt.Errorf("Kagent bindings must contain exactly one YAML document")
	default:
		return KagentBindings{}, fmt.Errorf("Kagent bindings must contain exactly one YAML document: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return KagentBindings{}, fmt.Errorf("Kagent bindings must be one YAML mapping")
	}
	if err := refusePortableDecodedSecretShapes(root.Content[0]); err != nil {
		return KagentBindings{}, err
	}
	if err := rejectPortableKeyHazards(root.Content[0], ""); err != nil {
		return KagentBindings{}, fmt.Errorf("Kagent bindings: %w", err)
	}
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var bindings KagentBindings
	if err := strict.Decode(&bindings); err != nil {
		return KagentBindings{}, fmt.Errorf("Kagent bindings: %w", err)
	}
	if bindings.APIVersion != KagentBindingsAPIVersion || bindings.Kind != KagentBindingsKind {
		return KagentBindings{}, fmt.Errorf("Kagent bindings require apiVersion %q and kind %s", KagentBindingsAPIVersion, KagentBindingsKind)
	}
	if err := bindings.validate(); err != nil {
		return KagentBindings{}, fmt.Errorf("Kagent bindings: %w", err)
	}
	return bindings, nil
}

// EncodeKagentBindings deterministically writes a target document. An omitted
// Secret key receives the documented scaffold default before validation.
func EncodeKagentBindings(bindings KagentBindings) ([]byte, error) {
	if bindings.APIVersion == "" {
		bindings.APIVersion = KagentBindingsAPIVersion
	}
	if bindings.Kind == "" {
		bindings.Kind = KagentBindingsKind
	}
	if bindings.ModelConfig.SecretRef.Key == "" {
		bindings.ModelConfig.SecretRef.Key = DefaultKagentSecretKey
	}
	if err := bindings.validate(); err != nil {
		return nil, fmt.Errorf("Kagent bindings: %w", err)
	}
	encoded, err := yaml.Marshal(bindings)
	if err != nil {
		return nil, fmt.Errorf("encode Kagent bindings: %w", err)
	}
	return append([]byte("# Creation target only; other targets supply their own bindings.\n"), encoded...), nil
}

// ValidateKagentBindings validates an in-memory effective document. Unlike
// EncodeKagentBindings it applies no defaults; callers must state identity
// and the Secret key.
func ValidateKagentBindings(bindings KagentBindings) error { return bindings.validate() }

func (b KagentBindings) validate() error {
	fields := []struct{ path, value string }{
		{"apiVersion", b.APIVersion},
		{"kind", b.Kind},
		{"namespace", b.Namespace},
		{"modelConfig.provider", b.ModelConfig.Provider},
		{"modelConfig.baseURL", b.ModelConfig.BaseURL},
		{"modelConfig.secretRef.name", b.ModelConfig.SecretRef.Name},
		{"modelConfig.secretRef.key", b.ModelConfig.SecretRef.Key},
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
	if b.APIVersion != KagentBindingsAPIVersion {
		return fmt.Errorf("apiVersion must be %q", KagentBindingsAPIVersion)
	}
	if b.Kind != KagentBindingsKind {
		return fmt.Errorf("kind must be %s", KagentBindingsKind)
	}
	if err := scaffold.ValidateNamespace(b.Namespace); err != nil {
		return fmt.Errorf("namespace: %w", err)
	}
	if err := scaffold.ValidateKagentProvider(b.ModelConfig.Provider, "bound-model", b.ModelConfig.BaseURL); err != nil {
		return fmt.Errorf("modelConfig: %w", err)
	}
	if err := scaffold.ValidateObjectName(b.ModelConfig.SecretRef.Name); err != nil {
		return fmt.Errorf("modelConfig.secretRef.name: %w", err)
	}
	if b.ModelConfig.SecretRef.Key == "" {
		return fmt.Errorf("modelConfig.secretRef.key is required")
	}
	if err := scaffold.ValidateKagentSecretKey(b.ModelConfig.SecretRef.Key); err != nil {
		return fmt.Errorf("modelConfig.secretRef.key: %w", err)
	}
	return nil
}
