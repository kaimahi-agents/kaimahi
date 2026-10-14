package app

import (
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

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
	n.SuiteIdentity, err = suiteNativeDigest("kmx.native-suite.diff-subject/v1", struct {
		Subject         suiteNativeSubject
		Provider, Agent suiteNativeOwnedResource
		Kagent          *suiteKagentPassingProof
	}{r.Subject, r.Provider, r.Agent, r.Kagent})
	if err != nil {
		return n, false
	}
	inventory := make([]suitePassingCase, 0, len(r.Cases))
	for _, row := range r.Cases {
		c := normalizedEvaluationCase{
			CaseDigest: row.CaseDigest, InputDigest: row.InputDigest, Verdict: row.Verdict,
			IdentityComplete: true, Assertions: map[string]agentruntime.EvaluationAssertionResult{},
		}
		for _, assertion := range row.Assertions {
			c.Assertions[assertion.ID] = agentruntime.EvaluationAssertionResult{
				ID: assertion.ID, Type: assertion.Type, DefinitionDigest: assertion.DefinitionDigest,
				Verdict: agentruntime.EvaluationVerdict(assertion.Verdict),
			}
		}
		n.Cases[row.ID] = c
		row.TaskName, row.TaskUID, row.AnswerDigest = "", "", ""
		row.KagentExecution = nil
		inventory = append(inventory, row)
	}
	n.SuiteInventory, err = suiteNativeDigest("kmx.native-suite.diff-inventory/v1", inventory)
	return n, err == nil
}
