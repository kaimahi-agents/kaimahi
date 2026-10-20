package app

// Frozen v0.8.1 status annotation reader. Keep the wire shape and version gate
// independent of today's statusRecord: an old client must not credit v2 attempts.
import (
	"strings"
	"time"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type frozenV081Provenance struct {
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

type frozenV081Record struct {
	frozenV081Provenance
	Actor *string `json:"actor"`
}

func frozenV081TargetValid(target ReleaseLiftTarget) bool {
	return statusSafeText(target.Context) && scaffold.ValidateNamespace(target.Namespace) == nil && statusOpaque(target.ClusterUID) && statusOpaque(target.NamespaceUID)
}
func frozenV081SameTarget(a, b ReleaseLiftTarget) bool {
	// Kubeconfig aliases supply access, not cluster/environment identity.
	return a.ClusterUID == b.ClusterUID && a.Namespace == b.Namespace && a.NamespaceUID == b.NamespaceUID
}
func frozenV081StatusRecord(raw string, runtime string) *frozenV081Record {
	if len(raw) == 0 || len(raw) > 8<<10 {
		return nil
	}
	var fields map[string]any
	var r frozenV081Provenance
	if agentsuite.Unmarshal([]byte(raw), &fields) != nil || agentsuite.Unmarshal([]byte(raw), &r) != nil {
		return nil
	}
	if _, ok := fields["evaluatedOnTarget"].(bool); !ok {
		return nil
	}
	if r.Version != 1 || !evaluationRunID(r.LiftID) || !evaluationRunID(r.ReceiptRunID) || !agentsuite.IsValidDigest(r.SuiteDigest) || !agentsuite.IsValidDigest(r.ReceiptDigest) || !frozenV081TargetValid(r.Target) || !frozenV081TargetValid(r.Evaluation) || r.EvaluationName != "" && !statusSafeText(r.EvaluationName) || r.Actor != "" && !statusSafeText(r.Actor) {
		return nil
	}
	if r.Gate != "promotion" && r.Gate != "same-subject" || r.EvaluatedOnTarget != (r.Gate == "same-subject") || r.Gate == "same-subject" && !frozenV081SameTarget(r.Evaluation, r.Target) {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, r.AttemptedAt); err != nil || !strings.HasSuffix(r.AttemptedAt, "Z") {
		return nil
	}
	companion := "Provider"
	if runtime == "kagent" {
		companion = "ModelConfig"
	}
	if len(r.ResourceSpecs) != 2 || !validOrkaMarkerDigest(r.ResourceSpecs["Agent"]) || !validOrkaMarkerDigest(r.ResourceSpecs[companion]) {
		return nil
	}
	// Match the producer's typed-ID exemption, not a blanket secret exemption.
	masked := r
	masked.LiftID, masked.ReceiptRunID = "lift", "run"
	checked, err := agentsuite.Marshal(masked)
	if err != nil || scaffold.RefuseKeyShapes(string(checked)) != nil {
		return nil
	}
	var actor *string
	if r.Actor != "" {
		value := r.Actor
		actor = &value
	}
	return &frozenV081Record{r, actor}
}
