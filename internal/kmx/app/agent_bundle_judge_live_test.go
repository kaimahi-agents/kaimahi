package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// This opt-in is a real production-client proof, not a measure of general judge quality.
func TestLiveEvaluationJudgeAIKitProof(t *testing.T) {
	baseURL := os.Getenv("KMX_LIVE_JUDGE_BASE_URL")
	model := os.Getenv("KMX_LIVE_JUDGE_MODEL")
	version := os.Getenv("KMX_LIVE_JUDGE_VERSION")
	dir := os.Getenv("KMX_LIVE_JUDGE_EVIDENCE_DIR")
	if baseURL == "" || model == "" || version == "" || dir == "" {
		t.Skip("requires explicit KMX_LIVE_JUDGE_BASE_URL, MODEL, VERSION and EVIDENCE_DIR")
	}
	if model != "qwen-3.5-2b" || !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(version) {
		t.Fatal("live judge proof requires the explicitly selected Qwen AIKit model and image digest")
	}
	// The runner exclusively creates this private directory; refuse links and reuse of the file.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || !filepath.IsAbs(dir) || resolved != filepath.Clean(dir) {
		t.Fatal("live judge evidence requires an existing absolute directory without links")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("live judge evidence requires a private owned directory")
	}

	minScore := 50
	c := agentruntime.EvaluationCase{
		ID: "public-judge-proof", Input: "Return exactly YES.",
		Assertions: []agentruntime.EvaluationAssertion{{
			ID: "exact-yes", Type: "llmJudge",
			Rubric:   "The score is 100 if the answer is exactly YES. Otherwise the score is 0.",
			MinScore: &minScore, JudgeModel: model, JudgeVersion: version,
		}},
	}
	var reasoning bytes.Buffer
	// No injected HTTP client, fake response, retries, or agent/model inference.
	judge, err := newEvaluationJudge(evaluationJudgeConfig{BaseURL: baseURL}, []bundleEvaluationCase{{Case: c}}, &reasoning)
	if err != nil || judge == nil {
		t.Fatal("live production judge configuration refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	good := judge(ctx, c, agentruntime.EvaluationEvidence{Answer: "YES", AnswerAvailable: true})
	bad := judge(ctx, c, agentruntime.EvaluationEvidence{Answer: "NO", AnswerAvailable: true})
	// The production callback already sanitizes valid reasoning and refuses credential echoes.
	if text := strings.TrimSpace(reasoning.String()); text != "" {
		t.Log(text)
	}
	if len(good) != 1 || len(bad) != 1 {
		t.Fatal("live judge proof did not return one actual assertion row per answer")
	}
	proof := struct {
		Model        string                                 `json:"model"`
		ModelVersion string                                 `json:"modelVersion"`
		Good         agentruntime.EvaluationAssertionResult `json:"good"`
		Bad          agentruntime.EvaluationAssertionResult `json:"bad"`
	}{model, version, good[0], bad[0]}
	// Only actual payload-free rows are persisted, including unknown on proof failure.
	data, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		t.Fatal("cannot encode live judge evidence")
	}
	file, err := os.OpenFile(filepath.Join(dir, "judge-proof.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("cannot exclusively create live judge evidence")
	}
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("cannot write live judge evidence")
	}
	for _, fixture := range []struct {
		name    string
		row     agentruntime.EvaluationAssertionResult
		verdict agentruntime.EvaluationVerdict
		score   int
	}{{"good", good[0], agentruntime.EvaluationPass, 100}, {"bad", bad[0], agentruntime.EvaluationFail, 0}} {
		e := fixture.row.Judge
		if fixture.row.Verdict != fixture.verdict || !agentruntime.EvaluationJudgeEvidenceValid(fixture.row) || e == nil ||
			e.Model != model || e.ModelVersion != version || e.MinScore != 50 || len(e.Scores) != 2 ||
			e.Scores[0] != fixture.score || e.Scores[1] != fixture.score {
			t.Errorf("public %s judge proof failed: got %s, expected %s with two scores of %d; unknown never passes", fixture.name, fixture.row.Verdict, fixture.verdict, fixture.score)
		}
	}
	if !t.Failed() {
		t.Log("public judge proof: good pass, bad fail; four temperature-zero judgments, no retries")
	}
}
