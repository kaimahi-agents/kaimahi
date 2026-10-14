// Package suitedeploy proposes the internal handoff between AgentSuite builders
// and deployment adapters. It is not a public SDK or an installed CLI workflow.
package suitedeploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

// Reference separates retrieval location from descriptor identity. Location is
// used in-process; receipts retain descriptors rather than mutable locations.
type Reference struct {
	Location   string
	Descriptor agentsuite.Descriptor
}

// Suite is a resolver-produced snapshot of the complete validated graph.
// LogicalDigest hashes canonical agentsuite.json; Artifact pins the OCI manifest.
// Resolver implementations must verify content, not just populate these fields.
type Suite struct {
	Artifact                 Reference
	LogicalDigest            string
	Manifest                 agentsuite.Suite
	Agents                   []Agent
	ToolProviders            []agentsuite.ToolProvider
	Compositions             []agentsuite.Composition
	ToolProviderCompositions []agentsuite.ToolProviderComposition
	BuildProfiles            []agentsuite.BuildProfile
}

type Agent struct {
	Manifest     agentsuite.Agent
	Instructions []byte
}

type Resolver interface {
	Resolve(context.Context, string) (Suite, error)
}

// VersionedContract names an exact schema/semantic revision, not a runtime brand
// or a SemVer range. Adapters explicitly advertise the revisions they implement.
type VersionedContract struct {
	Name    string
	Version string
}

// Execution is builder-attested image metadata. Protocol describes the agent's
// serving interface, independently of the model protocol and container platform.
type Execution struct {
	Schema               VersionedContract
	HarnessABI           VersionedContract
	Modes                []Mode
	Inputs               []Input
	RequiredCapabilities []VersionedContract
}

type Mode struct {
	Name      string
	Protocol  VersionedContract
	Lifecycle string // service or session
}

type Input struct {
	Name     string
	Kind     string // value or secret-reference
	Required bool
}

// BuildOutput is the proposed extension to #339's archive result. ArchivePath is
// optional local output; Image is the actual runnable platform manifest.
// ExecutionDescriptor commits to the Execution record. Binding commits to the
// existing AgentSuite sandbox binding, not a new deployment-specific image label.
type BuildOutput struct {
	Agent               string
	Platform            agentsuite.Platform
	Composition         agentsuite.Descriptor
	BuildProfile        agentsuite.Descriptor
	SuiteManifest       agentsuite.Descriptor
	Image               Reference
	ExecutionDescriptor agentsuite.Descriptor
	Execution           Execution
	Binding             agentsuite.Descriptor
	Conformance         string // experimental or conformant
	Diagnostics         []string
	ArchivePath         string
}

type BuildRequest struct {
	Suite        Suite
	Agent        string
	Platform     agentsuite.Platform
	Composition  agentsuite.Descriptor
	BuildProfile agentsuite.Descriptor
}

type Builder interface {
	Build(context.Context, BuildRequest) (BuildOutput, error)
}

// Target is a destination, not a source behavior field. AccessContext is an
// access locator; ClusterUID and Namespace establish placement identity.
// The same runtime adapter serves kind and AKS.
type Target struct {
	AccessContext string
	ClusterUID    string
	Namespace     string
	Runtime       string
}

type Installation struct {
	Runtime            string
	Version            string
	Identity           string
	CapabilityRevision string
	ExecutionContracts []VersionedContract
	HarnessABIs        []VersionedContract
	Protocols          []VersionedContract
	Lifecycles         []string
	BindingFormats     []VersionedContract
	Capabilities       []VersionedContract
	Reconcile          bool
}

// TargetBinding contains no credential values. Adapter-specific bytes use an
// exact versioned schema and are interpreted only by that adapter. Its canonical
// digest commits to the format and payload; operation stores own persistence.
type TargetBinding struct {
	Format  VersionedContract
	Digest  string
	Payload []byte
}

// Selection is an explicit union. Native execution must not discard a selected
// image; image execution must be qualified by the destination adapter.
type Selection struct {
	Agent   string
	Kind    string // native or image
	Image   *BuildOutput
	Mode    string
	Binding TargetBinding
}

type Request struct {
	OperationID string
	Instance    string
	Suite       Suite
	Target      Target
	Selections  []Selection
	Reconcile   bool
}

// Validate checks request topology before adapters perform cluster work. Artifact
// verification and semantic compatibility remain resolver/adapter obligations.
func (r Request) Validate() error {
	if r.OperationID == "" || r.Instance == "" || r.Target.ClusterUID == "" || r.Target.Namespace == "" || r.Target.AccessContext == "" || r.Target.Runtime == "" {
		return errors.New("operation, instance and explicit target are required")
	}
	if !validDescriptor(r.Suite.Artifact.Descriptor) || !validDigest(r.Suite.LogicalDigest) {
		return errors.New("suite requires distinct OCI and logical identities")
	}
	if len(r.Suite.Agents) == 0 || len(r.Selections) != len(r.Suite.Agents) {
		return errors.New("select every suite agent exactly once")
	}
	agents := map[string]bool{}
	for _, agent := range r.Suite.Agents {
		id := agent.Manifest.ID
		if id == "" || agents[id] {
			return errors.New("invalid or duplicate suite agent")
		}
		agents[id] = true
	}
	seen := map[string]bool{}
	for _, selection := range r.Selections {
		if !agents[selection.Agent] || seen[selection.Agent] {
			return errors.New("missing, duplicate or undeclared member selection")
		}
		seen[selection.Agent] = true
		if selection.Binding.Format.Name == "" || selection.Binding.Format.Version == "" || !validDigest(selection.Binding.Digest) || len(selection.Binding.Payload) == 0 {
			return fmt.Errorf("agent %s requires a versioned binding", selection.Agent)
		}
		switch selection.Kind {
		case "native":
			if selection.Image != nil || selection.Mode != "" {
				return errors.New("native selection cannot contain image inputs")
			}
		case "image":
			image := selection.Image
			if image == nil || image.Agent != selection.Agent || image.SuiteManifest != r.Suite.Artifact.Descriptor {
				return errors.New("image must bind the selected suite and agent")
			}
			if !validDescriptor(image.Image.Descriptor) || !validDescriptor(image.Composition) || !validDescriptor(image.BuildProfile) || !validDescriptor(image.Binding) || !validDescriptor(image.ExecutionDescriptor) {
				return errors.New("image inputs must be descriptor-bound")
			}
			if image.Conformance != "conformant" {
				return errors.New("experimental images require qualification before suite deployment")
			}
			if image.Platform.OS != "linux" || (image.Platform.Architecture != "amd64" && image.Platform.Architecture != "arm64") || image.Platform.Variant != "" {
				return errors.New("image requires an exact supported platform")
			}
			if image.Image.Descriptor.MediaType != "application/vnd.oci.image.manifest.v1+json" || image.Composition.MediaType != agentsuite.MediaTypeComposition || image.BuildProfile.MediaType != agentsuite.MediaTypeBuildProfile || image.Binding.MediaType != agentsuite.MediaTypeSandboxBinding {
				return errors.New("image descriptors have incorrect semantic media types")
			}
			if image.Execution.Schema.Name == "" || image.Execution.Schema.Version == "" || image.Execution.HarnessABI.Name == "" || image.Execution.HarnessABI.Version == "" {
				return errors.New("image execution and harness contracts must be versioned")
			}
			modeFound := false
			modes := map[string]bool{}
			for _, mode := range image.Execution.Modes {
				if mode.Name == "" || modes[mode.Name] || mode.Protocol.Name == "" || mode.Protocol.Version == "" || (mode.Lifecycle != "service" && mode.Lifecycle != "session") {
					return errors.New("invalid or duplicate execution mode")
				}
				modes[mode.Name] = true
				modeFound = modeFound || mode.Name == selection.Mode
			}
			if !modeFound {
				return errors.New("selected execution mode is not declared by image")
			}
		default:
			return errors.New("selection kind must be native or image")
		}
	}
	return nil
}

// Adapter plans the entire suite, including shared resources and invocation
// edges. Plan must reject unsupported members before any mutation. Deploy must
// consume the exact plan, reverify target/installation/resource identities, and
// return partial evidence on failure. It never provisions target infrastructure.
type Adapter interface {
	ID() string
	Inspect(context.Context, Target) (Installation, error)
	Plan(context.Context, Request, Installation) (*Plan, error)
	Deploy(context.Context, *Plan, Authorization) (Receipt, error)
	Observe(context.Context, DeploymentRef) (Observation, error)
}

// Gate and confirmation are separate from execution. Authorization is issued
// only for the reviewed plan and its evaluation decision, not a mutable tag.
type Gate interface {
	Check(context.Context, PlanSummary) (GateDecision, error)
}

type GateDecision struct {
	PlanDigest      string
	Verdict         string // pass, not-required, refused, unknown
	EvidenceDigests []string
}

type DeploymentRef struct {
	Instance   string
	ClusterUID string
	Namespace  string
	Runtime    string
}

type ResourceOutcome struct {
	ID         string
	UID        string
	Generation int64
	State      string // created, updated, reused, failed, unknown
}

type MemberOutcome struct {
	Agent     string
	State     string // ready, pending, failed, unknown, not-attempted
	Resources []ResourceOutcome
}

type Receipt struct {
	OperationID string
	Deployment  DeploymentRef
	PlanDigest  string
	State       string // ready, partial, unknown
	Members     []MemberOutcome
}

type Observation struct {
	Deployment DeploymentRef
	State      string // absent, pending, ready, degraded, retired, unknown
	Members    []MemberOutcome
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	for _, c := range value[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validDescriptor(d agentsuite.Descriptor) bool {
	return d.MediaType != "" && validDigest(d.Digest) && d.Size >= 0
}
