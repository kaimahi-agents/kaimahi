package agentsessions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	agentsuite "github.com/agent-primitives/suite"
	"github.com/distribution/reference"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// The harness image is the pinned AgentSessions harnessnode serving the
// built-in chat harness over harnesswire. These are its fixed contracts.
const (
	// HarnessPort is where harnessnode serves harnesswire; the atenet-router
	// forwards only to port 80.
	HarnessPort = 80
	// ReadyzPort serves the HTTP /readyz the ActorTemplate wakeup probe reads.
	ReadyzPort = 8081
	// RenderedHarnessKind selects the chat harness inside harnessnode. Its
	// descriptor id is also "chat".
	RenderedHarnessKind = "chat"
	wakeupTimeoutSecond = 120
	// maxImageReference is substrate's Container.image bound.
	maxImageReference = 512
)

// SubstrateAPICommit is the agent-substrate/substrate main commit whose ateapi
// ActorTemplate decoding (protojson) and CreateActorTemplate validation the
// rendered template is checked against by
// scripts/validate-agentsessions-render.sh.
const SubstrateAPICommit = "f4f7b806255ffdc824391b857dbbf329a2556402"

// reservedHarnesses are served by agentsessionsd itself; a registration under
// either name would collide with the host's own harness.
var reservedHarnesses = []string{"chat", "echo"}

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// RenderInput is one packaged AgentSessions member and the environment it is
// lifted into. Model is the suite's pinned model; it is never overridden.
type RenderInput struct {
	Suite, Member, Model string
	// HarnessImage must be a digest-pinned harnessnode image reference.
	HarnessImage string
	Binding      runtime.AgentSessionsBinding
}

// Rendered is the plan-only output: an ActorTemplate, the harness registration
// pointing sessions at it, and the host model references. It carries no model
// credential, prompt or instructions.
type Rendered struct {
	BindingDigest  string        `json:"bindingDigest"`
	HarnessImage   string        `json:"harnessImage"`
	TemplateDigest string        `json:"templateDigest"`
	ActorTemplate  ActorTemplate `json:"actorTemplate"`
	// HarnessRegistration is the AgentSessions HarnessRegistration in its
	// canonical proto3-JSON form (proto field names, RFC 8785).
	HarnessRegistration json.RawMessage `json:"harnessRegistration"`
	// RegistrationSpecDigest is the host's spec_digest for the registration.
	RegistrationSpecDigest string    `json:"registrationSpecDigest"`
	HostModel              HostModel `json:"hostModel"`
}

// HostModel is the model endpoint agentsessionsd must serve for this harness:
// a credential reference only, never a credential value.
type HostModel struct {
	Name       string                             `json:"name"`
	Provider   string                             `json:"provider"`
	BaseURL    string                             `json:"baseURL,omitempty"`
	Credential runtime.AgentSessionsCredentialRef `json:"credential"`
}

// ActorTemplate mirrors the substrate ateapi ActorTemplate fields a render
// sets at SubstrateAPICommit, using proto field and enum names. At that commit
// protojson decodes it and CreateActorTemplate validation accepts it; other
// substrate revisions may refuse it.
type ActorTemplate struct {
	Metadata       ActorMetadata       `json:"metadata"`
	WorkerSelector *ActorSelector      `json:"worker_selector,omitempty"`
	Containers     []ActorContainer    `json:"containers"`
	SnapshotConfig ActorSnapshotConfig `json:"snapshot_config"`
	SandboxConfig  ActorSandboxConfig  `json:"sandbox_config"`
	// Resources is the actor's compute size: the sandbox is sized and
	// scheduled by it.
	Resources ActorResources `json:"resources"`
}

type ActorMetadata struct {
	Atespace string `json:"atespace"`
	Name     string `json:"name"`
}

type ActorSelector struct {
	MatchLabels map[string]string `json:"match_labels"`
}

type ActorContainer struct {
	Name            string               `json:"name"`
	Image           string               `json:"image"`
	Env             []ActorEnvVar        `json:"env"`
	WakeupProbe     ActorWakeupProbe     `json:"wakeup_probe"`
	SecurityContext ActorSecurityContext `json:"security_context"`
}

type ActorEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ActorWakeupProbe struct {
	HTTPGet        ActorHTTPGet `json:"http_get"`
	TimeoutSeconds int32        `json:"timeout_seconds"`
}

type ActorHTTPGet struct {
	Path string `json:"path"`
	Port int32  `json:"port"`
}

// ActorSecurityContext adds no capabilities: the harness needs none.
type ActorSecurityContext struct {
	Capabilities struct{} `json:"capabilities"`
}

type ActorResources struct {
	Limits []ActorLimit `json:"limits"`
}

type ActorLimit struct {
	Name     string `json:"name"`
	Quantity string `json:"quantity"`
}

type ActorSnapshotConfig struct {
	StorageLocation   string `json:"storage_location"`
	PreferredFidelity string `json:"preferred_fidelity"`
}

// ActorSandboxConfig.SandboxClass is substrate's lowercase class string,
// "microvm" or "gvisor".
type ActorSandboxConfig struct {
	SandboxClass string `json:"sandbox_class"`
	ConfigName   string `json:"config_name"`
}

// RendererVersion identifies the binding-to-ActorTemplate mapping, including
// the substrate API shape it targets (SubstrateAPICommit). It is part of hash8,
// so a mapping change names a new template instead of reusing a name whose
// rendered spec differs.
const RendererVersion = "kmx.agentsessions-actortemplate/v2"

// TemplateName is <suite>-<member>-<hash8>, where hash8 is the first 8 hex of
// sha256(RendererVersion + "\n" + image + "\n" + bindingDigest) and image is the
// full digest-pinned reference the template runs. Every other rendered field
// derives from the suite, member, binding and renderer, so templates whose specs
// differ never share a name: the same content digest under another repository
// or registry (a mirror), binding or renderer is another template. The result
// is a DNS label of at most 63 characters; suite and member are lowercased,
// sanitized and truncated to fit, never hash8.
func TemplateName(suite, member, image, bindingDigest string) string {
	return templateName(RendererVersion, suite, member, image, bindingDigest)
}

func templateName(renderer, suite, member, image, bindingDigest string) string {
	sum := sha256.Sum256([]byte(renderer + "\n" + image + "\n" + bindingDigest))
	hash8 := hex.EncodeToString(sum[:])[:8]
	clean := func(s string) string {
		return strings.Trim(notLabel.ReplaceAllString(strings.ToLower(s), "-"), "-")
	}
	s, m := clean(suite), clean(member)
	// 63 = len(s) + 1 + len(m) + 1 + 8; either part may use the other's unused share.
	const budget = 63 - 2 - 8
	for len(s)+len(m) > budget {
		if len(s) >= len(m) {
			s = s[:len(s)-1]
		} else {
			m = m[:len(m)-1]
		}
	}
	return strings.Trim(s, "-") + "-" + strings.Trim(m, "-") + "-" + hash8
}

// HarnessImageDigest returns the digest of a digest-pinned image reference
// (name@sha256:<64 hex>, no tag).
func HarnessImageDigest(image string) (string, error) {
	const msg = "harness image must be a digest-pinned reference (name@sha256:<64 hex>) without a tag"
	if image == "" || len(image) > maxImageReference || strings.TrimSpace(image) != image || secretshapes.Match(image) != nil {
		return "", errors.New(msg)
	}
	ref, err := reference.Parse(image)
	if err != nil {
		return "", errors.New(msg)
	}
	digested, ok := ref.(reference.Canonical)
	if _, tagged := ref.(reference.Tagged); !ok || tagged || digested.Digest().Algorithm() != "sha256" || !agentsuite.IsValidDigest(digested.Digest().String()) {
		return "", errors.New(msg)
	}
	return digested.Digest().String(), nil
}

// Render maps one AgentSessions member and its environment binding onto an
// ActorTemplate running the chat harness and the registration that sends the
// member's sessions to it. It refuses a binding that serves a model other than
// the suite's pinned model. It performs no I/O.
func Render(in RenderInput) (*Rendered, error) {
	if err := runtime.ValidateAgentSessionsBinding(in.Binding); err != nil {
		return nil, fmt.Errorf("AgentSessions binding: %w", err)
	}
	if err := runtime.CheckAgentSessionsBindingModel(in.Binding, in.Model); err != nil {
		return nil, err
	}
	if !scaffold.ValidSuiteName(in.Suite) {
		return nil, errors.New("suite must be a workspace suite name")
	}
	// The member names the AgentSessions harness, which the host requires to be
	// a DNS label.
	if scaffold.ValidateName(in.Member) != nil {
		return nil, errors.New("member must be a DNS label of at most 63 characters")
	}
	if slices.Contains(reservedHarnesses, in.Member) {
		return nil, fmt.Errorf("member %q is the name of a harness agentsessionsd serves itself (%s); rename the member", in.Member, strings.Join(reservedHarnesses, ", "))
	}
	if _, err := HarnessImageDigest(in.HarnessImage); err != nil {
		return nil, err
	}
	bindingDigest, err := runtime.AgentSessionsBindingDigest(in.Binding)
	if err != nil {
		return nil, err
	}
	b := in.Binding
	name := TemplateName(in.Suite, in.Member, in.HarnessImage, bindingDigest)
	container := ActorContainer{
		Name:  "harness",
		Image: in.HarnessImage,
		Env: []ActorEnvVar{
			{Name: "HARNESS_ADDR", Value: fmt.Sprintf(":%d", HarnessPort)},
			{Name: "HARNESS_READYZ", Value: fmt.Sprintf(":%d", ReadyzPort)},
			{Name: "HARNESS_KIND", Value: RenderedHarnessKind},
			// The model id only; provider configuration and credentials stay on the host.
			{Name: "HARNESS_MODEL", Value: in.Model},
		},
		WakeupProbe: ActorWakeupProbe{HTTPGet: ActorHTTPGet{Path: "/readyz", Port: ReadyzPort}, TimeoutSeconds: wakeupTimeoutSecond},
	}
	template := ActorTemplate{
		Metadata:   ActorMetadata{Atespace: b.Atespace, Name: name},
		Containers: []ActorContainer{container},
		SnapshotConfig: ActorSnapshotConfig{
			StorageLocation:   strings.TrimSuffix(b.Snapshot.StorageLocation, "/") + "/" + name + "/",
			PreferredFidelity: "SNAPSHOT_FIDELITY_" + b.Snapshot.Fidelity,
		},
		SandboxConfig: ActorSandboxConfig{SandboxClass: b.Sandbox.Class, ConfigName: b.Sandbox.ConfigName},
		Resources:     ActorResources{Limits: []ActorLimit{{Name: "cpu", Quantity: b.Resources.CPU}, {Name: "memory", Quantity: b.Resources.Memory}}},
	}
	if len(b.WorkerSelector.MatchLabels) > 0 {
		template.WorkerSelector = &ActorSelector{MatchLabels: b.WorkerSelector.MatchLabels}
	}
	rawTemplate, err := json.Marshal(template)
	if err != nil {
		return nil, err
	}
	templateDigest, err := agentsuite.Digest(rawTemplate)
	if err != nil {
		return nil, err
	}
	registration := &v1.HarnessRegistration{
		Metadata: &v1.ResourceMetadata{Name: in.Member},
		Spec: &v1.HarnessSpec{
			Placement:    &v1.HarnessSpec_Substrate{Substrate: &v1.SubstratePlacement{Atespace: b.Atespace, Template: name}},
			Capabilities: &v1.Capabilities{Resumability: v1.Resumability_RESUMABILITY_STATELESS_REPLAY},
			DescriptorId: RenderedHarnessKind,
		},
	}
	rawRegistration, err := canon.Proto(registration)
	if err != nil {
		return nil, fmt.Errorf("encode harness registration: %w", err)
	}
	rawSpec, err := canon.Proto(registration.Spec)
	if err != nil {
		return nil, fmt.Errorf("encode harness registration spec: %w", err)
	}
	sum := sha256.Sum256(rawSpec)
	return &Rendered{
		BindingDigest:          bindingDigest,
		HarnessImage:           in.HarnessImage,
		TemplateDigest:         templateDigest,
		ActorTemplate:          template,
		HarnessRegistration:    rawRegistration,
		RegistrationSpecDigest: "sha256:" + hex.EncodeToString(sum[:]),
		HostModel:              HostModel{Name: b.Model.Name, Provider: b.Model.Provider, BaseURL: b.Model.BaseURL, Credential: b.Model.Credential},
	}, nil
}
