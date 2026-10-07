package lifecycle

import (
	"context"
	"time"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

// RevisionBuilder validates authored source and creates an immutable revision.
type RevisionBuilder interface {
	Build(context.Context, kmx.AgentSource) (kmx.AgentRevision, kmx.BuildReport, error)
}

type PlatformDescriptor struct {
	ID          kmx.PlatformID
	DisplayName string
}

type Platform interface {
	Describe() PlatformDescriptor
}

// PlatformResolver resolves an existing target without claiming ownership.
type PlatformResolver interface {
	Resolve(context.Context, kmx.TargetSpec) (kmx.TargetRef, error)
}

type ProvisionRequest struct {
	Operation kmx.OperationID
	Target    kmx.TargetSpec
}

func (r ProvisionRequest) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	return r.Target.Validate()
}

// PlatformProvisioner provisions target infrastructure and returns ownership
// evidence required by later deprovisioning.
type PlatformProvisioner interface {
	Provision(context.Context, ProvisionRequest) (kmx.InfrastructureReceipt, error)
}

type PlatformProvisionRecoverer interface {
	RecoverProvision(context.Context, kmx.OperationID) (kmx.InfrastructureReceipt, error)
}

type TargetObservation struct {
	Target     kmx.TargetRef
	State      kmx.TargetState
	ObservedAt time.Time
}

type PlatformInspector interface {
	Inspect(context.Context, kmx.TargetRef) (TargetObservation, error)
}

type DeprovisionRequest struct {
	Operation      kmx.OperationID
	Infrastructure kmx.InfrastructureReceipt
}

func (r DeprovisionRequest) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	return r.Infrastructure.Validate()
}

// PlatformDeprovisioner verifies durable ownership and removes only the target
// identified by the scoped infrastructure receipt.
type PlatformDeprovisioner interface {
	Deprovision(context.Context, DeprovisionRequest) (kmx.TeardownReceipt, error)
}

type PlatformDeprovisionRecoverer interface {
	RecoverDeprovision(context.Context, kmx.OperationID) (kmx.TeardownReceipt, error)
}

// AgentSuiteValidator validates the closed AgentSuite content graph.
type AgentSuiteValidator interface {
	Validate(context.Context, kmx.ValidateSuiteRequest) (kmx.SuiteReport, error)
}

// AgentSuiteBuilder performs deterministic, network-free OCI construction.
type AgentSuiteBuilder interface {
	Package(context.Context, kmx.PackageSuiteRequest) (kmx.PackageSuiteResult, error)
	BuildSandbox(context.Context, kmx.BuildSandboxRequest) (kmx.AgentSandboxImage, error)
}

type AgentSuiteBuildRecoverer interface {
	RecoverPackage(context.Context, kmx.OperationID) (kmx.PackageSuiteResult, error)
	RecoverBuildSandbox(context.Context, kmx.OperationID) (kmx.AgentSandboxImage, error)
}

// OCIArtifactPublisher is the separate networked registry transport.
type OCIArtifactPublisher interface {
	// Publish changes only Artifact.Location. Digest, MediaType, and
	// ArtifactType remain identical to the validated input.
	Publish(context.Context, kmx.PublishArtifactRequest) (kmx.OCIArtifactRef, error)
}

type OCIArtifactPublishRecoverer interface {
	RecoverPublish(context.Context, kmx.OperationID) (kmx.OCIArtifactRef, error)
}

type RuntimeDescriptor struct {
	ID          kmx.RuntimeID
	DisplayName string
}

type Runtime interface {
	Describe() RuntimeDescriptor
}

type EnsureRuntimeRequest struct {
	Operation kmx.OperationID
	Target    kmx.TargetRef
	Options   kmx.RuntimeOptions
}

func (r EnsureRuntimeRequest) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	return r.Target.Validate()
}

// RuntimeInstaller ensures one runtime installation exists on an exact target.
type RuntimeInstaller interface {
	Ensure(context.Context, EnsureRuntimeRequest) (kmx.RuntimeReceipt, error)
}

type RuntimeInstallRecoverer interface {
	RecoverEnsure(context.Context, kmx.OperationID) (kmx.RuntimeReceipt, error)
}

// RuntimeBuilder validates one direct revision or sandbox-image source and
// renders exact runtime-native documents, refusing behavior it cannot honor.
type RuntimeBuilder interface {
	Build(context.Context, RuntimeBuildInput) (RuntimeBundle, error)
}

type DeployOptions struct {
	Reconcile bool
}

// RuntimeDeployer writes only RuntimeBundle.DeployDocuments and returns durable
// deployment evidence.
type RuntimeDeployer interface {
	Deploy(context.Context, RuntimeBundle, DeployOptions) (kmx.DeploymentReceipt, error)
}

type RuntimeDeployRecoverer interface {
	RecoverDeploy(context.Context, kmx.OperationID) (kmx.DeploymentReceipt, error)
}

// RuntimeObserver distinguishes absence from unreadable deployment state.
type RuntimeObserver interface {
	Observe(context.Context, kmx.DeploymentRef) (kmx.DeploymentSnapshot, error)
}

// RuntimeRetirer verifies deployment and native ownership evidence before
// deleting or releasing workload resources. It has no target teardown authority.
type RuntimeRetirer interface {
	Retire(context.Context, kmx.RetireRequest) (kmx.RetirementReceipt, error)
}

type RuntimeRetireRecoverer interface {
	RecoverRetire(context.Context, kmx.OperationID) (kmx.RetirementReceipt, error)
}
