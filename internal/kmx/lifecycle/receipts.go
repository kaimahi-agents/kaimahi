package lifecycle

import (
	"time"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

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
