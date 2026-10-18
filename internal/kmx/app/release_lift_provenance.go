package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const releaseLiftRecordAnnotation = "kaimahi.dev/lift-record"

var errReleaseLiftProvenance = errors.New("Lift provenance unavailable, changed or incompletely persisted; inspect resource outcomes; no retry or repair")

// This unsigned create-time observation is separate from ownership and plan
// identity. A copied annotation cannot establish original resource UID continuity.
// Attempt time never claims successful pair completion or fresh execution.
type releaseLiftProvenance struct {
	Version           int               `json:"version"`
	LiftID            string            `json:"liftID"`
	SuiteDigest       string            `json:"suiteDigest"`
	ReceiptDigest     string            `json:"receiptDigest"`
	ReceiptRunID      string            `json:"receiptRunID"`
	Evaluation        ReleaseLiftTarget `json:"evaluation"`
	EvaluationName    string            `json:"evaluationName,omitempty"`
	Target            ReleaseLiftTarget `json:"target"`
	Gate              string            `json:"gate"`
	EvaluatedOnTarget bool              `json:"evaluatedOnTarget"`
	AttemptedAt       string            `json:"attemptedAt"`
	Actor             string            `json:"actor,omitempty"`
	ResourceSpecs     map[string]string `json:"resourceSpecs"`
}

func (a *App) prepareReleaseLiftProvenance(ctx context.Context, p *PreparedReleaseLift, docs []map[string]any) (*releaseLiftRecordWriter, string, error) {
	creates := false
	version := 1
	for _, r := range p.summary.Resources {
		creates = creates || r.Action == "create" || r.Action == "update"
		if r.Action == "update" {
			version = 2
		}
	}
	if !creates {
		return nil, "", nil
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, "", errReleaseLiftProvenance
	}
	r := releaseLiftProvenance{Version: version, LiftID: id, SuiteDigest: p.summary.SuiteDigest, ReceiptDigest: p.summary.EvidenceDigest, ReceiptRunID: p.summary.EvidenceRunID, Evaluation: p.summary.SourceTarget, EvaluationName: p.options.EvaluationName, Target: p.summary.Target, Gate: p.summary.Gate, EvaluatedOnTarget: p.summary.Gate == "same-subject", AttemptedAt: a.timeNow().UTC().Format(time.RFC3339Nano), ResourceSpecs: map[string]string{}}
	for _, resource := range p.summary.Resources {
		r.ResourceSpecs[resource.Kind] = resource.SpecDigest
	}
	raw, err := agentsuite.Marshal(r)
	if err != nil || len(raw) > 8<<10 {
		return nil, "", errReleaseLiftProvenance
	}
	// As with private passing receipts, typed random IDs are classified before
	// scanning the remaining public metadata for credential-shaped values.
	masked := r
	masked.LiftID, masked.ReceiptRunID = "lift", "run"
	checked, err := agentsuite.Marshal(masked)
	if err != nil || scaffold.RefuseKeyShapes(string(checked)) != nil {
		return nil, "", errReleaseLiftProvenance
	}
	// Canonical bytes were already validated against the package and are rechecked
	// by Apply. The store verifies their exact consumed EvidenceDigest again.
	receipt, err := agentsuite.Marshal(p.receipt)
	if err != nil {
		return nil, "", errReleaseLiftProvenance
	}
	writer, err := preflightReleaseLiftRecord(ctx, id, p.summary, receipt, string(raw))
	if err != nil {
		return nil, "", err
	}
	for i, doc := range docs {
		if p.summary.Resources[i].Action == "create" || p.summary.Resources[i].Action == "update" {
			meta := doc["metadata"].(map[string]any)
			ann, _ := meta["annotations"].(map[string]any)
			if ann == nil {
				ann = map[string]any{}
				meta["annotations"] = ann
			}
			ann[releaseLiftRecordAnnotation] = string(raw)
		}
	}
	return writer, id, nil
}

// Only newly created objects participate. Existing objects and historical
// annotations are never patched, and their absence is not synthesized here.
func (a *App) verifyReleaseLiftProvenance(ctx context.Context, p *PreparedReleaseLift, docs []map[string]any, result ReleaseLiftResult) error {
	for i, resource := range result.Resources {
		if resource.Action != "create" || resource.Outcome != "created" {
			continue
		}
		capture := a.orkaCapture
		plural := orkaPlural(resource.Kind)
		if p.kagent != nil {
			capture = a.kagentCapture
			plural = kagentPlural(resource.Kind)
		}
		raw, err := capture(ctx, nil, "-n", p.summary.Target.Namespace, "get", plural, resource.Name, "-o", "json")
		if err != nil {
			return errors.Join(errReleaseLiftProvenance, ctx.Err())
		}
		var observed struct {
			Metadata struct {
				UID         string            `json:"uid"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &observed) != nil || observed.Metadata.UID != resource.UID || observed.Metadata.Annotations[releaseLiftRecordAnnotation] != docs[i]["metadata"].(map[string]any)["annotations"].(map[string]any)[releaseLiftRecordAnnotation] {
			return errReleaseLiftProvenance
		}
	}
	return nil
}
