package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestEvaluationJudgeLocalDiagnosticsCompatibility(t *testing.T) {
	const clarified = "Reasoning is never receipt or evaluation evidence; it may be kept in separate local diagnostics."
	const historical = "Reasoning is terminal-only and is never receipt evidence."
	const digest = "2866b5c14b0efa8dd13964a40030ffd79877bba4dc00405c1121000113adf793"
	if !strings.HasSuffix(EvaluationJudgePolicy, clarified) {
		t.Fatal("policy does not clarify separate local diagnostics")
	}
	legacy := strings.TrimSuffix(EvaluationJudgePolicy, clarified) + historical
	sum := sha256.Sum256([]byte(legacy))
	if hex.EncodeToString(sum[:]) != digest || EvaluationJudgeEvaluatorDigest() != digest {
		t.Fatal("storage clarification changed scoring rules or historical evidence identity")
	}
	// This literal v2 judge row predates local diagnostics and must remain valid.
	const row = `{"id":"quality","type":"llmJudge","definitionDigest":"d58d7753601ecd06b6ace92c80c60fc5078f5eaab749f74fbf81cbcc417c5578","verdict":"pass","reason":"assertion satisfied","judge":{"requestStyle":"openai-chat-completions-v1","model":"qwen-3.5-2b","modelVersion":"aikit-qwen3.5-2b@sha256:d838c5eebf533b5b73f67cc7f4984921f87d64d28e4a52d482740b667f46b8b4","provenance":"user-declared","evaluatorDigest":"2866b5c14b0efa8dd13964a40030ffd79877bba4dc00405c1121000113adf793","configurationDigest":"b7ce49e0b23890067e384d903c5deb6425e2909c300411898f2773fa2bd16579","minScore":70,"scores":[80,90],"modelMatches":["requested","requested"]}}`
	var old EvaluationAssertionResult
	if json.Unmarshal([]byte(row), &old) != nil || !EvaluationJudgeEvidenceValid(old) {
		t.Fatal("historical judge evidence no longer valid")
	}
}
