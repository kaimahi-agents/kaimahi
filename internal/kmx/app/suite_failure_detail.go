package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// Diagnostic content has no evidence authority and is never a receipt field.
// The key exists only in the run's transient notes, never in a saved detail.
type suiteJudgeDiagnostic struct {
	CaseID, AssertionID, Class string
	Reasons                    []string
}
type suiteFailureNotesKey struct{}
type suiteFailureNotes struct {
	judge map[string]suiteJudgeDiagnostic
	class string
	key   string
}

func newSuiteFailureContext(ctx context.Context) (context.Context, *suiteFailureNotes) {
	notes := &suiteFailureNotes{judge: map[string]suiteJudgeDiagnostic{}}
	if reporter, ok := ctx.Value(suiteFailureReportingKey{}).(suiteFailureReporter); ok {
		notes.key = reporter.key
	}
	return context.WithValue(ctx, suiteFailureNotesKey{}, notes), notes
}
func suiteFailureRunNotes(ctx context.Context) *suiteFailureNotes {
	notes, _ := ctx.Value(suiteFailureNotesKey{}).(*suiteFailureNotes)
	return notes
}
func recordSuiteJudgeDiagnostic(ctx context.Context, note suiteJudgeDiagnostic) {
	if notes := suiteFailureRunNotes(ctx); notes != nil {
		notes.judge[note.AssertionID] = note
	}
}
func recordSuiteFailureClass(ctx context.Context, class string) {
	if notes := suiteFailureRunNotes(ctx); notes != nil {
		notes.class = class
	}
}

type suiteFailureDetail struct {
	ID                   string                  `json:"diagnosticID,omitempty"`
	OperationID          string                  `json:"operationID,omitempty"`
	SuiteDigest          string                  `json:"suiteDigest,omitempty"`
	Runtime              string                  `json:"runtime,omitempty"`
	MemberID             string                  `json:"memberID,omitempty"`
	Complete             bool                    `json:"complete"`
	Attributed           bool                    `json:"attributed"`
	RetentionUnavailable bool                    `json:"retentionUnavailable,omitempty"`
	ContentTruncated     bool                    `json:"contentTruncated,omitempty"`
	OmittedAssertions    int                     `json:"omittedAssertions,omitempty"`
	CaseID               string                  `json:"caseID"`
	Run                  int                     `json:"run"`
	Runs                 int                     `json:"runs"`
	MinPasses            int                     `json:"minPasses"`
	Verdict              string                  `json:"verdict"`
	Class                string                  `json:"class,omitempty"`
	Answer               string                  `json:"answer,omitempty"`
	AnswerStatus         string                  `json:"answerStatus"`
	Assertions           []suiteFailureAssertion `json:"assertions,omitempty"`
}
type suiteFailureAssertion struct {
	ID                   string   `json:"id"`
	Type                 string   `json:"type"`
	Verdict              string   `json:"verdict"`
	Class                string   `json:"class,omitempty"`
	Expected             string   `json:"expected,omitempty"`
	Threshold            *int     `json:"threshold,omitempty"`
	Scores               []int    `json:"scores,omitempty"`
	Reasons              []string `json:"reasons,omitempty"`
	RationaleUnavailable bool     `json:"rationaleUnavailable,omitempty"`
	Excerpt              string   `json:"excerpt,omitempty"`
	ExcerptKind          string   `json:"excerptKind,omitempty"`
}

// Withhold known header fields and consecutive header-like fields. Ambiguous
// multi-line labels, bare URLs or times also fail closed. Shape and header
// checks still cannot identify arbitrary opaque secrets.
var suiteDiagnosticHeader = regexp.MustCompile("(?im)^HTTP/[0-9.]+[ \\t]+[0-9]{3}\\b|^[ \\t]*[!#$%&'*+.^_`|~0-9A-Za-z-]+:[ \\t]*[^\\r\\n]*\\r?\\n[ \\t]*[!#$%&'*+.^_`|~0-9A-Za-z-]+:[ \\t]*")
var suiteDiagnosticSensitiveHeader = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|api[-_]?key|x-[a-z0-9-]+|content-[a-z0-9-]+|user-agent|host|connection|transfer-encoding|www-authenticate)[ \t]*:`)

// suiteDiagnosticViews is shared with provider-owned preview preflight so the
// exact display normalization cannot assemble a credential after it was checked.
func suiteDiagnosticViews(raw string, limit int) ([]string, string, bool) {
	if !utf8.ValidString(raw) {
		return nil, "", false
	}
	safe := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, safeTerminal(raw))
	// Preserve newlines for excerpts, but also check their removal: controls
	// can split a credential which becomes recognizable when copied/joined.
	stripControls := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, s)
	}
	compact := stripControls(safe)
	clipped := suiteDiagnosticCut(safe, limit)
	checks := []string{raw, safe, compact}
	// A cut can create a new credential/header boundary absent in the full field.
	if len(clipped) < len(safe) {
		checks = append(checks, clipped, stripControls(clipped))
	}
	return checks, clipped, true
}

func suiteDiagnosticText(raw string, limit int, keys []string) (string, bool) {
	checks, clipped, ok := suiteDiagnosticViews(raw, limit)
	if !ok {
		return "", false
	}
	for _, s := range checks {
		if secretshapes.Match(s) != nil || suiteDiagnosticHeader.MatchString(s) || suiteDiagnosticSensitiveHeader.MatchString(s) {
			return "", false
		}
		for _, key := range keys {
			if key != "" && strings.Contains(s, key) {
				return "", false
			}
		}
	}
	return clipped, true
}
func suiteDiagnosticCut(s string, limit int) string {
	if len(s) <= limit {
		return strings.Clone(s)
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return strings.Clone(s[:limit])
}
func suiteDiagnosticIdentity(raw string, keys []string) string {
	safe, ok := suiteDiagnosticText(raw, 256, keys)
	if !ok {
		return "withheld"
	}
	return strings.Join(strings.Fields(safe), " ")
}
func suiteFailureClass(err error) string {
	if err == nil || errors.Is(err, errSuiteNativeCaseFailed) {
		return ""
	}
	var safe *suiteDiagnosticError
	if errors.As(err, &safe) {
		return safe.class
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection-refused"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	var certificate *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var authority x509.UnknownAuthorityError
	if errors.As(err, &certificate) || errors.As(err, &record) || errors.As(err, &authority) {
		return "tls"
	}
	var port *orkaResultPortError
	if errors.As(err, &port) {
		return "result-port-unavailable"
	}
	return "unknown"
}

// Transport wrappers retain the existing safe message and error identity;
// evaluation copies only class, never the cause, into diagnostic data.
type suiteDiagnosticError struct {
	class string
	cause error
}

func (e *suiteDiagnosticError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.class
}
func (e *suiteDiagnosticError) Unwrap() error { return e.cause }

// Transport consumers keep their existing message and error identity. Only
// evaluation extracts the allowlisted class; arbitrary causes are not exposed.
func withSuiteFailureClass(err error, class string) error {
	return &suiteDiagnosticError{class: class, cause: err}
}
func suiteHTTPDiagnostic(source string, status int) error {
	if status < 100 || status > 599 {
		return &suiteDiagnosticError{class: source + "-http-unknown"}
	}
	return &suiteDiagnosticError{class: fmt.Sprintf("%s-http-%d", source, status)}
}

func captureSuiteFailure(frame suitePassingInventoryCase, observation suitePassingObservation, answer string, notes map[string]suiteJudgeDiagnostic, cause error, keys ...string) *suiteFailureDetail {
	return captureSuiteFailureWithReasons(frame, observation, answer, notes, true, cause, keys...)
}

func captureSuiteFailureWithReasons(frame suitePassingInventoryCase, observation suitePassingObservation, answer string, notes map[string]suiteJudgeDiagnostic, retainReasons bool, cause error, keys ...string) *suiteFailureDetail {
	verdict := suiteRunVerdict(observation.Assertions)
	if cause == nil && verdict == agentruntime.EvaluationPass && suiteRunComplete(observation, frame) {
		return nil
	}
	runs, required := frame.Case.RunPolicy()
	d := &suiteFailureDetail{CaseID: suiteDiagnosticIdentity(frame.Case.ID, keys), Run: frame.Metadata.Run, Runs: runs, MinPasses: required, Verdict: string(verdict), Class: suiteFailureClass(cause), AnswerStatus: "unavailable"}
	if d.Run == 0 {
		d.Run = 1
	}
	available := observation.AnswerSHA256 != "" && !observation.Stop && observation.Detail == ""
	d.Complete, d.Attributed = suiteRunComplete(observation, frame), available
	var full string
	if available {
		safe, ok := suiteDiagnosticText(answer, len(answer), keys)
		if !ok {
			d.AnswerStatus = "withheld"
		} else {
			full = safe
			d.Answer, ok = suiteDiagnosticText(suiteDiagnosticCut(safe, 64<<10), 64<<10, keys)
			switch {
			case !ok:
				d.AnswerStatus = "withheld"
			case len(d.Answer) < len(safe):
				d.AnswerStatus = "truncated"
			default:
				d.AnswerStatus = "complete"
			}
		}
	}
	definitions := agentruntime.EvaluationCaseAssertions(frame.Case)
	excerptBudget := 2048
	for i, row := range observation.Assertions {
		if row.Verdict == agentruntime.EvaluationPass {
			continue
		}
		a := suiteFailureAssertion{ID: suiteDiagnosticIdentity(row.ID, keys), Type: suiteDiagnosticIdentity(row.Type, keys), Verdict: string(row.Verdict)}
		if i < len(definitions) {
			definition := definitions[i]
			operand := definition.Value
			if definition.Type == "regex" {
				operand = definition.Pattern
			}
			if definition.Type == "toolCalled" || definition.Type == "toolNotCalled" {
				operand = definition.Tool
			}
			a.Expected, _ = suiteDiagnosticText(operand, 512, keys)
			if definition.MinScore != nil {
				n := *definition.MinScore
				a.Threshold = &n
			}
			if available && d.AnswerStatus != "withheld" {
				// Find a forbidden match in the complete safe answer before truncation.
				start := 0
				a.ExcerptKind = "leading"
				if definition.Type == "notContains" && a.Expected != "" {
					if at := strings.Index(full, definition.Value); at >= 0 {
						start = max(0, at-128)
						for start > 0 && !utf8.RuneStart(full[start]) {
							start--
						}
						a.ExcerptKind = "match"
					}
				}
				clipped := suiteDiagnosticCut(full[start:], min(512, excerptBudget))
				a.Excerpt, _ = suiteDiagnosticText(clipped, len(clipped), keys)
				excerptBudget -= len(a.Excerpt)
			}
		}
		if row.Judge != nil {
			a.Scores = append([]int(nil), row.Judge.Scores...)
			n := row.Judge.MinScore
			a.Threshold = &n
		}
		if note, ok := notes[row.ID]; ok {
			a.Class = note.Class
			for _, reason := range note.Reasons {
				if !retainReasons {
					break
				}
				if !available {
					break
				}
				if safe, ok := suiteDiagnosticText(reason, 512, keys); ok && strings.TrimSpace(safe) != "" {
					a.Reasons = append(a.Reasons, safe)
				} else {
					a.RationaleUnavailable = true
				}
			}
		}
		d.Assertions = append(d.Assertions, a)
	}
	return d
}

func captureSuiteFailureContext(ctx context.Context, frame suitePassingInventoryCase, observation *suitePassingObservation, answer string, cause error) {
	notes := suiteFailureRunNotes(ctx)
	if notes == nil {
		return
	}
	observation.failure = captureSuiteFailure(frame, *observation, answer, notes.judge, cause, notes.key)
	if observation.failure != nil && notes.class != "" {
		observation.failure.Class = notes.class
	}
}
