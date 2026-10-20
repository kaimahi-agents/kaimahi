package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	AgentSessionsBindingAPIVersion = PortableAPIVersion
	AgentSessionsBindingKind       = "AgentSessionsBinding"
	// AgentSessionsModelProvider is the only provider an AgentSessions host
	// serves: an OpenAI-compatible endpoint configured on the host.
	AgentSessionsModelProvider = "openai"
	// maxAgentSessionsStorageLocation leaves room under substrate's 1024-byte
	// storage_location limit for the "/<template>/" a render appends (a template
	// name is at most 63 bytes) when the location has no trailing slash.
	maxAgentSessionsStorageLocation = 1024 - 65
	// maxAgentSessionsWorkerLabels and maxAgentSessionsQuantity are substrate's
	// Selector.match_labels and Limits.quantity bounds.
	maxAgentSessionsWorkerLabels = 10
	maxAgentSessionsQuantity     = 32
)

// AgentSessionsBinding is the per-environment half of an AgentSessions
// deployment. The suite pins the exact model; this document says where that
// model is served and where sessions are placed. It references a Secret
// name/key for the model credential and cannot represent a value.
type AgentSessionsBinding struct {
	APIVersion     string                             `yaml:"apiVersion" json:"apiVersion"`
	Kind           string                             `yaml:"kind" json:"kind"`
	Target         AgentSessionsTargetBinding         `yaml:"target" json:"target"`
	Placement      AgentSessionsPlacementBinding      `yaml:"placement" json:"placement"`
	Atespace       string                             `yaml:"atespace" json:"atespace"`
	Sandbox        AgentSessionsSandboxBinding        `yaml:"sandbox" json:"sandbox"`
	Snapshot       AgentSessionsSnapshotBinding       `yaml:"snapshot" json:"snapshot"`
	Resources      AgentSessionsResourcesBinding      `yaml:"resources" json:"resources"`
	WorkerSelector AgentSessionsWorkerSelectorBinding `yaml:"workerSelector,omitempty" json:"workerSelector,omitzero"`
	Model          AgentSessionsModelBinding          `yaml:"model" json:"model"`
}

type AgentSessionsTargetBinding struct {
	Platform string `yaml:"platform" json:"platform"`
	ID       string `yaml:"id" json:"id"`
}

type AgentSessionsPlacementBinding struct {
	Backend string `yaml:"backend" json:"backend"`
}

type AgentSessionsSandboxBinding struct {
	Class      string `yaml:"class" json:"class"`
	ConfigName string `yaml:"configName" json:"configName"`
}

type AgentSessionsSnapshotBinding struct {
	Fidelity        string `yaml:"fidelity" json:"fidelity"`
	StorageLocation string `yaml:"storageLocation" json:"storageLocation"`
}

// AgentSessionsResourcesBinding is the actor's compute size. Substrate books
// both limits against worker capacity and boots a micro-VM at that size, so
// both are required.
type AgentSessionsResourcesBinding struct {
	CPU    string `yaml:"cpu" json:"cpu"`
	Memory string `yaml:"memory" json:"memory"`
}

type AgentSessionsWorkerSelectorBinding struct {
	MatchLabels map[string]string `yaml:"matchLabels,omitempty" json:"matchLabels,omitempty"`
}

// AgentSessionsModelBinding mirrors KagentModelConfigBindings plus the served
// model name, which must equal the suite's pinned model.
type AgentSessionsModelBinding struct {
	Name      string                        `yaml:"name" json:"name"`
	Provider  string                        `yaml:"provider" json:"provider"`
	BaseURL   string                        `yaml:"baseURL,omitempty" json:"baseURL,omitempty"`
	SecretRef AgentSessionsSecretRefBinding `yaml:"secretRef" json:"secretRef"`
}

// AgentSessionsSecretRefBinding names the host model credential by Secret
// name and key. It cannot represent a value.
type AgentSessionsSecretRefBinding struct {
	Name string `yaml:"name" json:"name"`
	Key  string `yaml:"key" json:"key"`
}

// ErrAgentSessionsModelMismatch classifies a binding that serves a model other
// than the one the suite pins.
var ErrAgentSessionsModelMismatch = errors.New("AgentSessions binding model does not match the suite's pinned model")

// AgentSessionsModelMismatchError names both models. Model identifiers are
// validated single-line identities, not credentials.
type AgentSessionsModelMismatchError struct{ Suite, Binding string }

func (e *AgentSessionsModelMismatchError) Error() string {
	return fmt.Sprintf("AgentSessions binding serves model %q but the suite pins %q; a different model needs a new suite version and a fresh evaluation", e.Binding, e.Suite)
}

func (e *AgentSessionsModelMismatchError) Is(target error) bool {
	return target == ErrAgentSessionsModelMismatch
}

// CheckAgentSessionsBindingModel refuses a binding whose served model is not
// exactly the suite's pinned model. No case folding, prefix or version
// matching applies.
func CheckAgentSessionsBindingModel(b AgentSessionsBinding, suiteModel string) error {
	if b.Model.Name != suiteModel {
		return &AgentSessionsModelMismatchError{Suite: suiteModel, Binding: b.Model.Name}
	}
	return nil
}

// ParseAgentSessionsBinding accepts exactly one closed, unambiguous document.
// Parsed documents must state every required field; no defaults apply.
func ParseAgentSessionsBinding(data []byte) (AgentSessionsBinding, error) {
	if err := refusePortableSecretShape(string(data)); err != nil {
		return AgentSessionsBinding{}, err
	}
	if !utf8.Valid(data) {
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding must be valid UTF-8")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding must be valid YAML: %w", err)
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding must contain exactly one YAML document")
	default:
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding must contain exactly one YAML document: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding must be one YAML mapping")
	}
	if err := refusePortableDecodedSecretShapes(root.Content[0]); err != nil {
		return AgentSessionsBinding{}, err
	}
	if err := rejectPortableKeyHazards(root.Content[0], ""); err != nil {
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding: %w", err)
	}
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var binding AgentSessionsBinding
	if err := strict.Decode(&binding); err != nil {
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding: %w", err)
	}
	if binding.APIVersion != AgentSessionsBindingAPIVersion || binding.Kind != AgentSessionsBindingKind {
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding requires apiVersion %q and kind %s", AgentSessionsBindingAPIVersion, AgentSessionsBindingKind)
	}
	if err := binding.validate(); err != nil {
		return AgentSessionsBinding{}, fmt.Errorf("AgentSessions binding: %w", err)
	}
	return binding.normalized(), nil
}

// EncodeAgentSessionsBinding deterministically writes a binding document. Only
// the document identity is defaulted.
func EncodeAgentSessionsBinding(binding AgentSessionsBinding) ([]byte, error) {
	if binding.APIVersion == "" {
		binding.APIVersion = AgentSessionsBindingAPIVersion
	}
	if binding.Kind == "" {
		binding.Kind = AgentSessionsBindingKind
	}
	if err := binding.validate(); err != nil {
		return nil, fmt.Errorf("AgentSessions binding: %w", err)
	}
	encoded, err := yaml.Marshal(binding.normalized())
	if err != nil {
		return nil, fmt.Errorf("encode AgentSessions binding: %w", err)
	}
	return append([]byte("# Environment binding only; the suite pins the model, this file says where it is served.\n"), encoded...), nil
}

// ValidateAgentSessionsBinding validates an in-memory effective document. It
// applies no defaults.
func ValidateAgentSessionsBinding(binding AgentSessionsBinding) error { return binding.validate() }

// AgentSessionsBindingDigest is "sha256:" plus the SHA-256 of the RFC 8785
// (JCS) form of the binding's JSON mapping. It covers decoded content only,
// so YAML comments, key order and the JSON or YAML spelling do not change it.
// An empty workerSelector.matchLabels is the same binding as an omitted one.
func AgentSessionsBindingDigest(binding AgentSessionsBinding) (string, error) {
	if err := binding.validate(); err != nil {
		return "", fmt.Errorf("AgentSessions binding: %w", err)
	}
	raw, err := json.Marshal(binding.normalized())
	if err != nil {
		return "", fmt.Errorf("encode AgentSessions binding: %w", err)
	}
	return agentsuite.Digest(raw)
}

var agentSessionsURIScheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

// normalized gives equivalent spellings one form: an empty matchLabels adds no
// label constraint, so it is the omitted selector.
func (b AgentSessionsBinding) normalized() AgentSessionsBinding {
	if len(b.WorkerSelector.MatchLabels) == 0 {
		b.WorkerSelector.MatchLabels = nil
	}
	return b
}

func (b AgentSessionsBinding) validate() error {
	fields := []struct{ path, value string }{
		{"apiVersion", b.APIVersion},
		{"kind", b.Kind},
		{"target.platform", b.Target.Platform},
		{"target.id", b.Target.ID},
		{"placement.backend", b.Placement.Backend},
		{"atespace", b.Atespace},
		{"sandbox.class", b.Sandbox.Class},
		{"sandbox.configName", b.Sandbox.ConfigName},
		{"snapshot.fidelity", b.Snapshot.Fidelity},
		{"snapshot.storageLocation", b.Snapshot.StorageLocation},
		{"resources.cpu", b.Resources.CPU},
		{"resources.memory", b.Resources.Memory},
		{"model.name", b.Model.Name},
		{"model.provider", b.Model.Provider},
		{"model.baseURL", b.Model.BaseURL},
		{"model.secretRef.name", b.Model.SecretRef.Name},
		{"model.secretRef.key", b.Model.SecretRef.Key},
	}
	labels := make([]string, 0, len(b.WorkerSelector.MatchLabels))
	for key := range b.WorkerSelector.MatchLabels {
		labels = append(labels, key)
	}
	slices.Sort(labels)
	for _, key := range labels {
		fields = append(fields, struct{ path, value string }{"workerSelector.matchLabels key", key}, struct{ path, value string }{"workerSelector.matchLabels value", b.WorkerSelector.MatchLabels[key]})
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
	if b.APIVersion != AgentSessionsBindingAPIVersion {
		return fmt.Errorf("apiVersion must be %q", AgentSessionsBindingAPIVersion)
	}
	if b.Kind != AgentSessionsBindingKind {
		return fmt.Errorf("kind must be %s", AgentSessionsBindingKind)
	}
	if b.Target.Platform != "kubernetes" {
		return fmt.Errorf("target.platform must be kubernetes")
	}
	if err := scaffold.ValidateObjectName(b.Target.ID); err != nil {
		return fmt.Errorf("target.id: %w", err)
	}
	if b.Placement.Backend != "substrate" {
		return fmt.Errorf("placement.backend must be substrate")
	}
	if err := scaffold.ValidateNamespace(b.Atespace); err != nil {
		return fmt.Errorf("atespace: %w", err)
	}
	if b.Sandbox.Class != "microvm" && b.Sandbox.Class != "gvisor" {
		return fmt.Errorf("sandbox.class must be microvm or gvisor")
	}
	if err := scaffold.ValidateObjectName(b.Sandbox.ConfigName); err != nil {
		return fmt.Errorf("sandbox.configName: %w", err)
	}
	switch b.Snapshot.Fidelity {
	case "MEMORY", "VOLUMES":
	case "ROOTFS":
		// Substrate serves root filesystem snapshots on the micro-VM class only.
		if b.Sandbox.Class != "microvm" {
			return fmt.Errorf("snapshot.fidelity ROOTFS requires sandbox.class microvm")
		}
	default:
		return fmt.Errorf("snapshot.fidelity must be MEMORY, ROOTFS or VOLUMES")
	}
	if err := validateAgentSessionsStorageLocation(b.Snapshot.StorageLocation); err != nil {
		return err
	}
	for _, r := range []struct {
		path, value string
		max         resource.Quantity
		bound       string
	}{
		// Substrate's cpuLimitMax and memoryLimitMax.
		{"resources.cpu", b.Resources.CPU, resource.MustParse("999.999"), "999.999 cores"},
		{"resources.memory", b.Resources.Memory, resource.MustParse("9223372036854775807"), "9223372036854775807 bytes"},
	} {
		if r.value == "" {
			return fmt.Errorf("%s is required", r.path)
		}
		q, err := resource.ParseQuantity(r.value)
		if len(r.value) > maxAgentSessionsQuantity || err != nil || q.Sign() <= 0 || q.Cmp(r.max) > 0 {
			return fmt.Errorf("%s must be a positive Kubernetes quantity of at most %s", r.path, r.bound)
		}
	}
	if len(labels) > maxAgentSessionsWorkerLabels {
		return fmt.Errorf("workerSelector.matchLabels has more than %d labels", maxAgentSessionsWorkerLabels)
	}
	for _, key := range labels {
		if errs := validation.IsQualifiedName(key); len(errs) != 0 {
			return fmt.Errorf("workerSelector.matchLabels key is not a qualified label name: %s", strings.Join(errs, "; "))
		}
		if errs := validation.IsValidLabelValue(b.WorkerSelector.MatchLabels[key]); len(errs) != 0 {
			return fmt.Errorf("workerSelector.matchLabels value is not a label value: %s", strings.Join(errs, "; "))
		}
	}
	if strings.TrimSpace(b.Model.Name) == "" || strings.TrimSpace(b.Model.Name) != b.Model.Name || scaffold.ValidateSingleLineText(b.Model.Name) != nil || strings.ContainsAny(b.Model.Name, " \t") {
		return fmt.Errorf("model.name must be one exact model identifier without whitespace")
	}
	if b.Model.Provider != AgentSessionsModelProvider {
		return fmt.Errorf("model.provider must be %s (an OpenAI-compatible endpoint)", AgentSessionsModelProvider)
	}
	if b.Model.BaseURL != "" {
		u, err := url.Parse(b.Model.BaseURL)
		if scaffold.ValidateSingleLineText(b.Model.BaseURL) != nil || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(b.Model.BaseURL, "#") {
			return fmt.Errorf("model.baseURL must be an absolute HTTP(S) URL with a host and no credentials, query, or fragment")
		}
	}
	if err := scaffold.ValidateObjectName(b.Model.SecretRef.Name); err != nil {
		return fmt.Errorf("model.secretRef.name: %w", err)
	}
	if b.Model.SecretRef.Key == "" {
		return fmt.Errorf("model.secretRef.key is required")
	}
	if err := scaffold.ValidateKagentSecretKey(b.Model.SecretRef.Key); err != nil {
		return fmt.Errorf("model.secretRef.key: %w", err)
	}
	return nil
}

// validateAgentSessionsStorageLocation accepts an object-storage base URI
// (for example gs://bucket/prefix/) and never echoes it. Object keys are not
// filesystem paths, and some storage backends trim dots and spaces from
// segments or treat '\' as a separator, so empty segments, segments of only
// '.' and ' ', and '\\' are refused rather than resolved, as substrate's
// snapshot location validation does; one trailing slash is allowed.
func validateAgentSessionsStorageLocation(location string) error {
	const msg = "snapshot.storageLocation must be an object-storage URI with a scheme and bucket, no credentials, query or fragment, no '\\', and no empty path segments or segments of only '.' and ' '"
	if location == "" {
		return fmt.Errorf("snapshot.storageLocation is required")
	}
	if len(location) > maxAgentSessionsStorageLocation || scaffold.ValidateSingleLineText(location) != nil || strings.ContainsAny(location, " \t") {
		return fmt.Errorf("%s", msg)
	}
	u, err := url.Parse(location)
	if err != nil || !agentSessionsURIScheme.MatchString(u.Scheme) || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(location, "#") {
		return fmt.Errorf("%s", msg)
	}
	// u.Path is decoded, so percent-encoded dots, spaces and '\' are refused too.
	if strings.Contains(u.Path, `\`) {
		return fmt.Errorf("%s", msg)
	}
	if u.Path != "" && u.Path != "/" {
		for _, segment := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), "/") {
			if strings.Trim(segment, ". ") == "" {
				return fmt.Errorf("%s", msg)
			}
		}
	}
	return nil
}
