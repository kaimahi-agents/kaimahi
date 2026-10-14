package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func validateReferenceEvaluationOptions(opt EvaluateAgentBundleOptions) error {
	if opt.FromSessions == "" {
		if opt.SourceSessions != "" || opt.SourceSessionsCA != "" || opt.ShowContent {
			return fmt.Errorf("--source-sessions, --source-sessions-ca and --show-content require --from-sessions")
		}
		return nil
	}
	if opt.Sessions == "" || opt.SourceSessions == "" {
		return fmt.Errorf("--from-sessions requires --sessions and --source-sessions")
	}
	if opt.ToContext != "" || opt.ResultPort != "" {
		return fmt.Errorf("--from-sessions cannot be combined with --to-context or --result-port")
	}
	return nil
}

func confirmReferenceCandidate(address string) error {
	destination, err := agentsessions.NormalizeAddress(address)
	if err != nil {
		return fmt.Errorf("invalid candidate sessions destination")
	}
	if os.Getenv("KAIMAHI_CONFIRM") != destination {
		return fmt.Errorf("recorded inputs will be sent to %s and retained in candidate journals; set KAIMAHI_CONFIRM=%s to consent before running", destination, destination)
	}
	return nil
}
func confirmReferenceJudge(address, keyEnv string) error {
	if address == "" {
		return nil
	}
	destination, err := canonicalEvaluationJudgeURL(address)
	if err != nil {
		return fmt.Errorf("invalid judge destination")
	}
	// Confirmation names the destination, so apply the credential-in-path
	// boundary before quoting it. The judge constructor validates remaining
	// transport and credential details without exposing their values.
	if keyEnv != "" {
		if !evaluationJudgeKeyEnvRE.MatchString(keyEnv) {
			return fmt.Errorf("invalid judge credential environment name")
		}
		key := os.Getenv(keyEnv)
		parsed, _ := url.Parse(destination)
		if key == "" || strings.Contains(destination, key) || strings.Contains(parsed.Path, key) {
			return fmt.Errorf("invalid judge destination or credential configuration")
		}
	}
	if os.Getenv("KAIMAHI_CONFIRM_JUDGE") != destination {
		return fmt.Errorf("recorded inputs, candidate answers and authored rubrics will be sent to %s; set KAIMAHI_CONFIRM_JUDGE=%s to consent before running", destination, destination)
	}
	return nil
}

// Canonical case bytes bind the recorded source prefix, extraction policy and
// authored assertions into the existing receipt/diff identities. No input is
// installed in a case until the explicitly selected source has verified it.
func loadSessionReferenceEvaluationCases(path, source string) ([]bundleEvaluationCase, []agentruntime.EvaluationCaseFile, []agentruntime.SessionReferenceCase, error) {
	manifest, err := readSessionReferenceManifest(path)
	if err != nil {
		return nil, nil, nil, err
	}
	address, err := agentsessions.NormalizeAddress(source)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid source sessions destination")
	}
	var cases []bundleEvaluationCase
	var files []agentruntime.EvaluationCaseFile
	for _, ref := range manifest.Cases {
		if ref.Source.Store != address {
			return nil, nil, nil, fmt.Errorf("source sessions destination differs from manifest")
		}
		raw, err := json.Marshal(struct {
			Policy string                            `json:"policy"`
			Case   agentruntime.SessionReferenceCase `json:"case"`
		}{manifest.Policy, ref})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid reference case")
		}
		file := agentruntime.EvaluationCaseFile{Name: ref.ID + ".json", Bytes: raw}
		files = append(files, file)
		cases = append(cases, bundleEvaluationCase{File: file.Name, Digest: agentruntime.EvaluationCaseDigest(raw), Case: agentruntime.EvaluationCase{ID: ref.ID, ExpectContains: ref.ExpectContains, Assertions: ref.Assertions}})
	}
	return cases, files, manifest.Cases, nil
}
