package lifecycle

import (
	"context"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

// RevisionBuilder validates authored source and creates an immutable revision.
type RevisionBuilder interface {
	Build(context.Context, kmx.AgentSource) (kmx.AgentRevision, kmx.BuildReport, error)
}

type RuntimeDescriptor struct {
	ID          kmx.RuntimeID
	DisplayName string
}

type Runtime interface {
	Describe() RuntimeDescriptor
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
