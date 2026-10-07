package lifecycle

import (
	"time"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

func NewInfrastructureReceipt(id kmx.ReceiptID, operation kmx.OperationID, target kmx.TargetRef, recordedAt time.Time) (kmx.InfrastructureReceipt, error) {
	receipt := kmx.InfrastructureReceipt{ID: id, Operation: operation, Target: target, RecordedAt: recordedAt.UTC()}
	return receipt, receipt.Validate()
}

func NewRuntimeReceipt(id kmx.ReceiptID, operation kmx.OperationID, runtime kmx.RuntimeRef, recordedAt time.Time) (kmx.RuntimeReceipt, error) {
	receipt := kmx.RuntimeReceipt{ID: id, Operation: operation, Runtime: runtime, RecordedAt: recordedAt.UTC()}
	return receipt, receipt.Validate()
}

func NewDeploymentReceipt(id kmx.ReceiptID, deploymentID kmx.DeploymentID, bundle RuntimeBundle, recordedAt time.Time) (kmx.DeploymentReceipt, error) {
	receipt := kmx.DeploymentReceipt{
		ID: id, Operation: bundle.Operation(),
		Deployment: kmx.DeploymentRef{
			ID: deploymentID, Source: bundle.Source(), Runtime: bundle.Runtime(),
		},
		BindingDigest: bundle.BindingDigest(), RenderedDigest: bundle.RenderedDigest(),
		DeployDigest: bundle.DeployDigest(),
		RecordedAt:   recordedAt.UTC(),
	}
	return receipt, receipt.Validate()
}

func NewRetirementReceipt(id kmx.ReceiptID, operation kmx.OperationID, deployment kmx.DeploymentReceipt, recordedAt time.Time) (kmx.RetirementReceipt, error) {
	if err := deployment.Validate(); err != nil {
		return kmx.RetirementReceipt{}, err
	}
	receipt := kmx.RetirementReceipt{
		ID: id, Operation: operation, Deployment: deployment.Deployment,
		Outcome: kmx.MutationSucceeded, RecordedAt: recordedAt.UTC(),
	}
	return receipt, receipt.Validate()
}

func NewTeardownReceipt(id kmx.ReceiptID, operation kmx.OperationID, infrastructure kmx.InfrastructureReceipt, recordedAt time.Time) (kmx.TeardownReceipt, error) {
	if err := infrastructure.Validate(); err != nil {
		return kmx.TeardownReceipt{}, err
	}
	receipt := kmx.TeardownReceipt{
		ID: id, Operation: operation, Target: infrastructure.Target,
		Outcome: kmx.MutationSucceeded, RecordedAt: recordedAt.UTC(),
	}
	return receipt, receipt.Validate()
}
