package scaffold

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

// OrkaRateLimit contains only explicitly requested limits. Nil fields are omitted.
type OrkaRateLimit struct {
	RequestsPerMinute *int32
	TokensPerMinute   *int64
}

// OrkaSpec describes a native Orka agent, never a credential or application image.
type OrkaSpec struct {
	Name, Namespace, Description, ProviderType, Model, BaseURL, SecretName, SecretKey, Instructions string
	Tools, Skills                                                                                   []string
	AgentRateLimit, ProviderRateLimit                                                               *OrkaRateLimit
	TaskPrompt                                                                                      string
}

// OrkaBundle is a review artifact, not a safely bulk-applied transaction.
// The Secret is a value-free prerequisite and must never be sent as a write.
type OrkaBundle struct {
	Secret, Provider, Agent, Task map[string]any
}

const orkaAPIVersion = "core.orka.ai/v1alpha1"

var orkaSecretKeyRE = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// GenerateOrka constructs the whole bundle without cluster or schema I/O.
// Callers validate custom resources against their selected orkaschema.Validator
// before emission; Validate enforces the stronger, schema-independent invariant.
func GenerateOrka(spec OrkaSpec) (*OrkaBundle, error) {
	inputs := []string{spec.Name, spec.Namespace, spec.Description, spec.ProviderType, spec.Model,
		spec.BaseURL, spec.SecretName, spec.SecretKey, spec.Instructions, spec.TaskPrompt}
	inputs = append(inputs, spec.Tools...)
	inputs = append(inputs, spec.Skills...)
	for _, input := range inputs {
		if err := refuseOrkaKeyShapes(input); err != nil {
			return nil, err
		}
	}
	if err := ValidateName(spec.Name); err != nil {
		return nil, err
	}
	if err := ValidateNamespace(spec.Namespace); err != nil {
		return nil, fmt.Errorf("an explicit Orka namespace is required: %w", err)
	}
	if err := validateOrkaProvider(spec.ProviderType, spec.Model, spec.BaseURL); err != nil {
		return nil, err
	}
	if err := ValidateObjectName(spec.SecretName); err != nil {
		return nil, fmt.Errorf("Secret name: %w", err)
	}
	if spec.SecretKey == "" {
		spec.SecretKey = "api-key"
	}
	if err := validateOrkaSecretKey(spec.SecretKey); err != nil {
		return nil, err
	}
	for _, input := range []string{spec.Description, spec.Model, spec.BaseURL} {
		if _, err := quote(input); err != nil {
			return nil, err
		}
	}
	for field, refs := range map[string][]string{"tools": spec.Tools, "skills": spec.Skills} {
		for _, ref := range refs {
			if !identifierRE.MatchString(ref) {
				return nil, fmt.Errorf("Orka %s must be explicit names, not server:tool syntax or YAML", field)
			}
		}
	}
	if strings.TrimSpace(spec.Instructions) == "" {
		spec.Instructions = fmt.Sprintf("You are %s, a declarative Orka agent running on Kubernetes. Answer briefly and in plain text, and say plainly when you do not know something.", spec.Name)
	}
	for _, text := range []string{spec.Instructions, spec.TaskPrompt} {
		// Retain the shared control-character policy, but leave the original
		// string intact: yaml.Marshal preserves its whitespace and newlines.
		if _, err := blockScalar(text, 2); err != nil {
			return nil, err
		}
	}
	if spec.TaskPrompt != "" && strings.TrimSpace(spec.TaskPrompt) == "" {
		return nil, fmt.Errorf("Orka Task prompt must not be blank")
	}
	providerSpec := map[string]any{
		"type": spec.ProviderType, "defaultModel": spec.Model,
		"secretRef": map[string]any{"name": spec.SecretName, "key": spec.SecretKey},
	}
	if spec.BaseURL != "" {
		providerSpec["baseURL"] = spec.BaseURL
	}
	agentSpec := map[string]any{
		"providerRef":  map[string]any{"name": spec.Name, "namespace": spec.Namespace},
		"systemPrompt": map[string]any{"inline": spec.Instructions},
	}
	for field, refs := range map[string][]string{"tools": spec.Tools, "skills": spec.Skills} {
		if len(refs) == 0 {
			continue
		}
		items := make([]any, 0, len(refs))
		for _, ref := range refs {
			items = append(items, map[string]any{"name": ref})
		}
		agentSpec[field] = items
	}
	for _, entry := range []struct {
		kind   string
		spec   map[string]any
		limits *OrkaRateLimit
	}{{"Agent", agentSpec, spec.AgentRateLimit}, {"Provider", providerSpec, spec.ProviderRateLimit}} {
		limits, err := orkaLimits(entry.limits)
		if err != nil {
			return nil, fmt.Errorf("%s.spec.rateLimit: %w", entry.kind, err)
		}
		if len(limits) > 0 {
			entry.spec["rateLimit"] = limits
		}
	}
	b := &OrkaBundle{
		Secret:   orkaResource("v1", "Secret", spec.SecretName, spec.Namespace),
		Provider: orkaResource(orkaAPIVersion, "Provider", spec.Name, spec.Namespace),
		Agent:    orkaResource(orkaAPIVersion, "Agent", spec.Name, spec.Namespace),
	}
	b.Provider["spec"], b.Agent["spec"] = providerSpec, agentSpec
	if spec.Description != "" {
		b.Agent["metadata"].(map[string]any)["annotations"] = map[string]any{"kaimahi.dev/description": spec.Description}
	}
	if spec.TaskPrompt != "" {
		var suffix [16]byte
		if _, err := io.ReadFull(rand.Reader, suffix[:]); err != nil {
			return nil, fmt.Errorf("generate random Orka Task name: %w", err)
		}
		prefix := strings.TrimRight(spec.Name[:min(len(spec.Name), 30)], "-")
		b.Task = orkaResource(orkaAPIVersion, "Task", prefix+"-"+hex.EncodeToString(suffix[:]), spec.Namespace)
		b.Task["spec"] = map[string]any{
			"type": "ai", "prompt": spec.TaskPrompt,
			"agentRef": map[string]any{"name": spec.Name, "namespace": spec.Namespace},
		}
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

func orkaResource(apiVersion, kind, name, namespace string) map[string]any {
	return map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": namespace},
	}
}

func validateOrkaProvider(providerType, model, baseURL string) error {
	switch providerType {
	case "openai", "anthropic":
	case "azure-openai":
		return fmt.Errorf("azure-openai requires separate deployment/version fields that are not scaffolded; use openai or anthropic")
	default:
		return fmt.Errorf("an explicit Orka Provider type is required: openai or anthropic")
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("an explicit Orka Provider model identifier is required")
	}
	if baseURL == "" {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(baseURL, "#") {
		// The URL itself may contain credentials; never echo it, including
		// through net/url's parse error.
		return fmt.Errorf("Orka Provider baseURL must be an absolute HTTP(S) URL with a host and no credentials, query, or fragment")
	}
	return nil
}

func validateOrkaSecretKey(key string) error {
	if len(key) > 253 || !orkaSecretKeyRE.MatchString(key) {
		return fmt.Errorf("Secret key must be 1–253 letters, digits, dashes, underscores or dots")
	}
	return nil
}

func orkaLimits(limits *OrkaRateLimit) (map[string]any, error) {
	out := map[string]any{}
	if limits == nil {
		return out, nil
	}
	if limits.RequestsPerMinute != nil {
		if *limits.RequestsPerMinute <= 0 {
			return nil, fmt.Errorf("requestsPerMinute must be positive")
		}
		out["requestsPerMinute"] = *limits.RequestsPerMinute
	}
	if limits.TokensPerMinute != nil {
		if *limits.TokensPerMinute <= 0 {
			return nil, fmt.Errorf("tokensPerMinute must be positive")
		}
		out["tokensPerMinute"] = *limits.TokensPerMinute
	}
	return out, nil
}

// Validate checks bundle identity, explicit same-namespace references and the
// value-free Secret shape independently of the weaker upstream CRD invariant.
func (b *OrkaBundle) Validate() error {
	if b == nil || b.Secret == nil || b.Provider == nil || b.Agent == nil {
		return fmt.Errorf("an Orka bundle requires a value-free Secret, a Provider and its referencing Agent")
	}
	// Normalize for scanning so nested typed maps cannot hide raw strings
	// from the scan by changing their Go representation or YAML quoting.
	data, err := json.Marshal(b.Documents())
	if err != nil {
		return fmt.Errorf("Orka bundle is not JSON-compatible: %w", err)
	}
	var values any
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("decode Orka bundle for credential scan: %w", err)
	}
	if err := scanOrkaValues(values); err != nil {
		return err
	}
	names := make(map[string]string)
	var namespace string
	for _, entry := range []struct {
		kind string
		doc  map[string]any
	}{{"Secret", b.Secret}, {"Provider", b.Provider}, {"Agent", b.Agent}, {"Task", b.Task}} {
		if entry.kind == "Task" && entry.doc == nil {
			continue
		}
		version := orkaAPIVersion
		if entry.kind == "Secret" {
			version = "v1"
		}
		if entry.doc["kind"] != entry.kind || entry.doc["apiVersion"] != version {
			return fmt.Errorf("%s kind or apiVersion does not match the Orka bundle", entry.kind)
		}
		metadata, _ := entry.doc["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		ns, _ := metadata["namespace"].(string)
		if err := ValidateObjectName(name); err != nil {
			return fmt.Errorf("%s.metadata.name: %w", entry.kind, err)
		}
		if err := ValidateNamespace(ns); err != nil {
			return fmt.Errorf("%s.metadata.namespace: %w", entry.kind, err)
		}
		if namespace != "" && namespace != ns {
			return fmt.Errorf("%s.metadata.namespace must match the bundle namespace", entry.kind)
		}
		namespace, names[entry.kind] = ns, name
	}
	for key := range b.Secret {
		if key != "apiVersion" && key != "kind" && key != "metadata" {
			return fmt.Errorf("Secret must be a value-free, metadata-only prerequisite (no data or stringData)")
		}
	}
	for key := range b.Secret["metadata"].(map[string]any) {
		if key != "name" && key != "namespace" {
			return fmt.Errorf("Secret skeleton metadata may contain only name and namespace")
		}
	}
	if names["Provider"] != names["Agent"] {
		return fmt.Errorf("Provider.metadata.name must match Agent.metadata.name")
	}
	providerSpec, _ := b.Provider["spec"].(map[string]any)
	providerType, _ := providerSpec["type"].(string)
	model, _ := providerSpec["defaultModel"].(string)
	baseURL, _ := providerSpec["baseURL"].(string)
	if err := validateOrkaProvider(providerType, model, baseURL); err != nil {
		return err
	}
	secretRef, _ := providerSpec["secretRef"].(map[string]any)
	if secretRef["name"] != names["Secret"] {
		return fmt.Errorf("Provider.spec.secretRef.name must reference the bundle Secret")
	}
	secretKey, _ := secretRef["key"].(string)
	if err := validateOrkaSecretKey(secretKey); err != nil {
		return fmt.Errorf("Provider.spec.secretRef.key: %w", err)
	}
	for _, entry := range []struct {
		kind, field, target string
		doc                 map[string]any
	}{{"Agent", "providerRef", "Provider", b.Agent}, {"Task", "agentRef", "Agent", b.Task}} {
		if entry.doc == nil {
			continue
		}
		spec, _ := entry.doc["spec"].(map[string]any)
		ref, _ := spec[entry.field].(map[string]any)
		if ref["name"] != names[entry.target] || ref["namespace"] != namespace {
			return fmt.Errorf("%s.spec.%s must reference the bundle %s by explicit name and namespace", entry.kind, entry.field, entry.target)
		}
	}
	return nil
}

// Documents returns review order. The Secret must be provisioned separately;
// Provider and Agent each need readiness checks before creating the next kind.
func (b *OrkaBundle) Documents() []map[string]any {
	docs := []map[string]any{b.Secret, b.Provider, b.Agent}
	if b.Task != nil {
		docs = append(docs, b.Task)
	}
	return docs
}

// YAML renders the already-selected schema provenance as comments. It never
// mutates the bundle or regenerates the optional Task's fixed name.
func (b *OrkaBundle) YAML(provenance string) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	if err := refuseOrkaKeyShapes(provenance); err != nil {
		return "", err
	}
	if _, err := blockScalar(provenance, 2); err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("# Orka bundle, scaffolded by kmx. Schema source:\n")
	for _, line := range strings.Split(provenance, "\n") {
		out.WriteString("# " + line + "\n")
	}
	out.WriteString(`# Local schema validation does not evaluate CEL or server admission.
# Use the explicit namespace that the Orka controller watches.
# Do not bulk-apply this bundle. The Secret skeleton is a named prerequisite,
# not a credential: provision its referenced key separately; never write this skeleton.
# Create Provider only, wait for current-generation Ready; then create Agent
# and wait for current-generation Ready; only then create the optional Task.
# Schema support for rate limits is not proof of runtime enforcement.
`)
	for _, doc := range b.Documents() {
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			return "", fmt.Errorf("render Orka bundle: %w", err)
		}
		out.WriteString("---\n")
		out.Write(encoded)
	}
	result := out.String()
	if err := refuseOrkaKeyShapes(result); err != nil {
		return "", err
	}
	return result, nil
}

func refuseOrkaKeyShapes(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("Orka input must be valid UTF-8")
	}
	if shape := secretshapes.Match(value); shape != nil {
		return fmt.Errorf("refusing Orka manifest containing something shaped like %s; reference a separately provisioned Secret, never a credential value", shape.What)
	}
	return nil
}

func scanOrkaValues(value any) error {
	switch value := value.(type) {
	case string:
		return refuseOrkaKeyShapes(value)
	case map[string]any:
		for key, item := range value {
			if err := refuseOrkaKeyShapes(key); err != nil {
				return err
			}
			if err := scanOrkaValues(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := scanOrkaValues(item); err != nil {
				return err
			}
		}
	}
	return nil
}
