// Evaluation judge transport keeps untrusted payloads and provider diagnostics out of receipts.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const (
	evaluationJudgeRequestLimit  = 1 << 20
	evaluationJudgeResponseLimit = 64 << 10
)

var evaluationJudgeKeyEnvRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type evaluationJudgeConfig struct {
	BaseURL, KeyEnv string
	// Snapshot the already validated credential for diagnostic redaction before
	// any run starts; callers must never serialize this transient value.
	diagnosticKey *string
}

type evaluationJudgeClient struct {
	baseURL    string
	key        string
	httpClient *http.Client
	out        io.Writer
}

// newEvaluationCaseJudge validates the selected suite before any agent execution.
// No endpoint, model, or credential is inferred from the agent configuration.
func newEvaluationCaseJudge(config evaluationJudgeConfig, selectedCases []agentruntime.EvaluationCase, out io.Writer) (agentruntime.EvaluationJudge, error) {
	hasJudge := false
	for _, selected := range selectedCases {
		if err := agentruntime.ValidateEvaluationAssertions(selected.Assertions); err != nil {
			return nil, fmt.Errorf("invalid selected evaluation assertions")
		}
		for _, a := range selected.Assertions {
			if a.Type == "llmJudge" {
				hasJudge = true
			}
		}
	}
	if !hasJudge {
		if config.BaseURL != "" || config.KeyEnv != "" {
			return nil, fmt.Errorf("judge configuration provided but no selected llmJudge assertions")
		}
		return nil, nil
	}
	if config.BaseURL == "" {
		return nil, fmt.Errorf("llmJudge assertions require --judge-base-url")
	}
	baseURL, err := canonicalEvaluationJudgeURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	key := ""
	if config.KeyEnv != "" {
		if !evaluationJudgeKeyEnvRE.MatchString(config.KeyEnv) {
			return nil, fmt.Errorf("judge key must nominate a valid environment variable name")
		}
		if !strings.HasPrefix(baseURL, "https://") {
			return nil, fmt.Errorf("judge credentials require HTTPS")
		}
		key = os.Getenv(config.KeyEnv)
		if key == "" {
			return nil, fmt.Errorf("judge key environment variable is empty")
		}
		for i := 0; i < len(key); i++ {
			if key[i] < 32 || key[i] == 127 {
				return nil, fmt.Errorf("judge key contains invalid header bytes")
			}
		}
		// Canonicalization already validated the URL; Path decodes escaped bytes.
		parsedURL, _ := url.Parse(baseURL)
		if strings.Contains(baseURL, key) || strings.Contains(parsedURL.Path, key) {
			return nil, fmt.Errorf("judge URL must not contain credentials")
		}
	}
	if config.diagnosticKey != nil {
		*config.diagnosticKey = key
	}
	if out == nil {
		out = io.Discard
	}
	// Clone standard defaults: ProxyFromEnvironment and normal TLS verification
	// remain active. Redirects are forbidden even to the same host.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &evaluationJudgeClient{baseURL: baseURL, key: key, out: out, httpClient: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	return client.evaluate, nil
}

func canonicalEvaluationJudgeURL(raw string) (string, error) {
	invalid := func() (string, error) {
		return "", fmt.Errorf("judge URL must be an absolute HTTP(S) base without credentials, query, or fragment")
	}
	if len(raw) > 4096 || !utf8.ValidString(raw) || secretshapes.Match(raw) != nil || strings.ContainsAny(raw, "?#") {
		return invalid()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return invalid()
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "127.0.0.1" || host == "::1")) {
		return "", fmt.Errorf("judge URL requires HTTPS except keyless HTTP at literal 127.0.0.1 or ::1")
	}
	if strings.HasSuffix(u.Host, ":") {
		return invalid()
	}
	port := u.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return invalid()
		}
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	if !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1"
		if u.RawPath != "" {
			u.RawPath += "/v1"
		}
	}
	return u.String(), nil
}

// evaluate preserves ordinary assertion rows and fills only judge rows while the
// attributed answer is ephemeral. Adapters invoke it only after execution succeeds.
func (c *evaluationJudgeClient) evaluate(ctx context.Context, evaluationCase agentruntime.EvaluationCase, evidence agentruntime.EvaluationEvidence) []agentruntime.EvaluationAssertionResult {
	if notes := suiteFailureRunNotes(ctx); notes != nil {
		notes.key = c.key
	}
	rows := agentruntime.EvaluateAssertions(evaluationCase, evidence)
	assertions := agentruntime.EvaluationCaseAssertions(evaluationCase)
	for i, a := range assertions {
		if a.Type != "llmJudge" {
			continue
		}
		// Direct adapter callers may bypass suite loading; never export unsafe identity.
		if agentruntime.ValidateEvaluationAssertions([]agentruntime.EvaluationAssertion{a}) != nil {
			continue
		}
		row := &rows[i]
		note := suiteJudgeDiagnostic{CaseID: evaluationCase.ID, AssertionID: a.ID, Class: "judge-unavailable"}
		defer func() { recordSuiteJudgeDiagnostic(ctx, note) }()
		row.Judge = &agentruntime.EvaluationJudgeEvidence{
			Model: a.JudgeModel, ModelVersion: a.JudgeVersion, Provenance: "user-declared", RequestStyle: agentruntime.EvaluationJudgeRequestStyle,
			EvaluatorDigest:     agentruntime.EvaluationJudgeEvaluatorDigest(),
			ConfigurationDigest: agentruntime.EvaluationJudgeConfigurationDigest(c.baseURL, a.JudgeModel, a.JudgeVersion), MinScore: *a.MinScore,
		}
		if !evidence.AnswerAvailable {
			note.Class = "answer-unavailable"
			row.Reason = "answer unavailable"
			continue
		}
		if _, ok := ctx.Deadline(); !ok || c.httpClient == nil {
			continue
		}
		payload, ok := evaluationJudgePayload(evaluationCase.Input, evidence.Answer, a)
		if !ok {
			continue
		}
		scores := make([]int, 0, 2)
		matches := make([]string, 0, 2)
		reasons := make([]string, 0, 2)
		failure := ""
		for sample := 0; sample < 2; sample++ {
			if ctx.Err() != nil {
				failure = "judge response unavailable"
				break
			}
			score, reason, match, errReason, class := c.sample(ctx, payload, a.JudgeModel, a.JudgeVersion)
			if errReason != "" {
				// Attempt the other independent sample while the deadline remains active,
				// but never retain a partial score or turn an unavailable sample into a pass.
				if failure == "" {
					failure = errReason
					note.Class = class
				}
				continue
			}
			scores = append(scores, score)
			matches = append(matches, match)
			reasons = append(reasons, reason)
		}
		if failure != "" {
			row.Reason = failure
			continue
		}
		note.Class, note.Reasons = "", reasons
		row.Judge.Scores, row.Judge.ModelMatches = scores, matches
		row.Verdict = agentruntime.EvaluationJudgeVerdict(scores, *a.MinScore)
		switch row.Verdict {
		case agentruntime.EvaluationPass:
			row.Reason = "assertion satisfied"
		case agentruntime.EvaluationFail:
			row.Reason = "assertion not satisfied"
		default:
			row.Reason = "judge verdict split"
			note.Class = "judge-split"
		}
		if c.out != nil && suiteFailureRunNotes(ctx) == nil {
			for _, reason := range reasons {
				// Output failure cannot change already valid evidence; discard writer errors
				// rather than exposing an error that could itself contain provider payloads.
				_, _ = fmt.Fprintln(c.out, reason)
			}
		}
	}
	return rows
}

func evaluationJudgePayload(input, answer string, a agentruntime.EvaluationAssertion) ([]byte, bool) {
	if len(input) > evaluationJudgeRequestLimit || len(answer) > evaluationJudgeRequestLimit || !utf8.ValidString(input) || !utf8.ValidString(answer) {
		return nil, false
	}
	data, err := json.Marshal(struct {
		Input  string `json:"input"`
		Answer string `json:"answer"`
		Rubric string `json:"rubric"`
	}{input, answer, a.Rubric})
	if err != nil {
		return nil, false
	}
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload, err := json.Marshal(struct {
		Model               string    `json:"model"`
		MaxCompletionTokens int       `json:"max_completion_tokens"`
		Stream              bool      `json:"stream"`
		Messages            []message `json:"messages"`
	}{Model: a.JudgeModel, MaxCompletionTokens: 1024, Stream: false, Messages: []message{{Role: "system", Content: agentruntime.EvaluationJudgePolicy}, {Role: "user", Content: string(data)}}})
	return payload, err == nil && len(payload) <= evaluationJudgeRequestLimit
}

func (c *evaluationJudgeClient) sample(ctx context.Context, payload []byte, model, version string) (int, string, string, string, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return 0, "", "", "judge response unavailable", "judge-unavailable"
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	// Enforce redirect refusal even when the caller supplies a TLS test client
	// or custom transport; copying avoids mutating a shared HTTP client.
	httpClient := *c.httpClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(req)
	if err != nil {
		return 0, "", "", "judge response unavailable", suiteFailureClass(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, "", "", "judge response unavailable", suiteFailureClass(suiteHTTPDiagnostic("judge", response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, evaluationJudgeResponseLimit+1))
	if err != nil {
		return 0, "", "", "judge response unavailable", suiteFailureClass(err)
	}
	if len(body) > evaluationJudgeResponseLimit {
		return 0, "", "", "judge response malformed", "judge-malformed"
	}
	score, reason, match, ok := parseEvaluationJudgeResponse(body, model, version, c.key)
	if !ok {
		return 0, "", "", "judge response malformed", "judge-malformed"
	}
	return score, reason, match, "", ""
}

func parseEvaluationJudgeResponse(body []byte, model, version, key string) (int, string, string, bool) {
	if !evaluationJudgeJSONUnique(body) {
		return 0, "", "", false
	}
	var envelope struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role         string          `json:"role"`
				Content      json.RawMessage `json:"content"`
				ToolCalls    json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage `json:"function_call"`
				Refusal      json.RawMessage `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Choices) != 1 {
		return 0, "", "", false
	}
	match := "requested"
	if envelope.Model != model {
		if version == "" || envelope.Model != model+"-"+version {
			return 0, "", "", false
		}
		match = "version-qualified"
	}
	choice := envelope.Choices[0]
	msg := choice.Message
	absent := func(raw json.RawMessage) bool {
		return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	if choice.FinishReason != "stop" || msg.Role != "assistant" || !absent(msg.ToolCalls) || !absent(msg.FunctionCall) || !absent(msg.Refusal) || absent(msg.Content) {
		return 0, "", "", false
	}
	var content string
	if json.Unmarshal(msg.Content, &content) != nil || !evaluationJudgeJSONUnique([]byte(content)) {
		return 0, "", "", false
	}
	var result map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &result) != nil || len(result) != 2 || absent(result["score"]) || absent(result["reasoning"]) {
		return 0, "", "", false
	}
	var score int
	var reasoning string
	if json.Unmarshal(result["score"], &score) != nil || score < 0 || score > 100 || json.Unmarshal(result["reasoning"], &reasoning) != nil || !utf8.ValidString(reasoning) || len(reasoning) > 4096 {
		return 0, "", "", false
	}
	safe := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, safeTerminal(reasoning))
	// Check both representations: stripping controls can assemble a credential.
	for _, text := range []string{reasoning, safe} {
		if (key != "" && strings.Contains(text, key)) || secretshapes.Match(text) != nil {
			return 0, "", "", false
		}
	}
	// Header-block boundaries can disappear when CR/LF are stripped. Inspect
	// the original rationale before returning its normalized display form;
	// withholding non-evidence text must not discard an otherwise valid score.
	if _, ok := suiteDiagnosticText(reasoning, len(reasoning), []string{key}); !ok {
		safe = ""
	}
	return score, safe, match, true
}

// evaluationJudgeJSONUnique rejects duplicate keys at every object depth and
// trailing data, without restricting normal OpenAI envelope extensions.
func evaluationJudgeJSONUnique(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			var keys []string
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return false
				}
				key, ok := token.(string)
				if !ok {
					return false
				}
				// Match encoding/json's Unicode case folding so aliases cannot
				// shadow identity, choices, or other evidence-bearing fields.
				for _, seen := range keys {
					if strings.EqualFold(key, seen) {
						return false
					}
				}
				keys = append(keys, key)
				if !walk(depth + 1) {
					return false
				}
			}
		case '[':
			for decoder.More() {
				if !walk(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		end, err := decoder.Token()
		return err == nil && ((delimiter == '{' && end == json.Delim('}')) || (delimiter == '[' && end == json.Delim(']')))
	}
	if !walk(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
