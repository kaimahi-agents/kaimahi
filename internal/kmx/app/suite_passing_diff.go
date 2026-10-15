package app

import (
	"fmt"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Named to permit only a validated candidate run scanner mask. The field order
// and names retain the historical canonical identity bytes, including ownership.
type suitePassingDiffSubject struct {
	Subject         suiteNativeSubject
	Provider, Agent suiteNativeOwnedResource
	Kagent          *suiteKagentPassingProof
}

// Normalize only the private reader's complete passing metadata. Execution IDs
// and answer hashes are deliberately not comparison identities; the prepared
// subject, observed ownership and ordered authored inventory must match exactly.
func normalizeSuitePassingEvaluation(r *suitePassingReceipt) (normalizedEvaluation, bool) {
	if r == nil || (r.ExecutionMode != "native-orka" && r.ExecutionMode != "native-kagent") {
		return normalizedEvaluation{}, false
	}
	n := normalizedEvaluation{
		Version: r.SchemaVersion, RunID: r.RunID, PortableDigest: r.suiteDigest(),
		CasesDigest: r.CasesDigest, Result: r.Result, FullCaseSet: true, IdentityComplete: true,
		Identity: evaluationDiffIdentity{Runtime: r.ExecutionMode},
		Cases:    map[string]normalizedEvaluationCase{},
	}
	n.Bundle = r.Subject.Suite
	if r.Kagent != nil {
		n.Bundle = r.Kagent.Observation.Subject.Suite
	}
	var err error
	n.SuiteIdentity, err = suiteNativeDigest("kmx.native-suite.diff-subject/v1", suitePassingDiffSubject{r.Subject, r.Provider, r.Agent, r.Kagent})
	if err != nil {
		return n, false
	}
	inventory := make([]suitePassingCase, 0, len(r.Cases))
	for _, row := range r.Cases {
		c, exists := n.Cases[row.ID]
		if !exists {
			c = normalizedEvaluationCase{
				CaseDigest: row.CaseDigest, InputDigest: row.InputDigest, Verdict: "pass",
				IdentityComplete: true, Assertions: map[string]agentruntime.EvaluationAssertionResult{},
			}
		}
		for _, assertion := range row.Assertions {
			key := assertion.ID
			if row.Run > 0 {
				key = fmt.Sprintf("%s/run/%d", key, row.Run)
			}
			c.Assertions[key] = agentruntime.EvaluationAssertionResult{
				ID: assertion.ID, Type: assertion.Type, DefinitionDigest: assertion.DefinitionDigest,
				Verdict: agentruntime.EvaluationVerdict(assertion.Verdict), Judge: assertion.Judge,
			}
		}
		n.Cases[row.ID] = c
		inventory = append(inventory, suitePassingDefinition(row, false))
	}
	n.SuiteInventory, err = suiteNativeDigest("kmx.native-suite.diff-inventory/v1", inventory)
	return n, err == nil
}
