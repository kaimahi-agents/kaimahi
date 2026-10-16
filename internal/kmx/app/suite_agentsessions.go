package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const suiteSessionsDevelopmentWarning = "Development: unauthenticated operator-observed host; not authenticated remote promotion."

var errSuiteSessionsHost = errors.New("agentsessions requires an explicit private operator-observed development host with a literal loopback address and matching pinned chat/model contract")

type suiteSessionsHostFile struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Address       string                       `json:"address"`
	Observation   suiteSessionsHostObservation `json:"observation"`
}
type suiteSessionsHostSnapshot struct {
	path         string
	parent, file os.FileInfo
	raw          []byte
	host         suiteSessionsHostFile
}

func snapshotSuiteSessionsHost(path string, suite *workspace.NativeSuite) (*suiteSessionsHostSnapshot, error) {
	if err := suite.RequireUnprofiled(); err != nil {
		return nil, err
	}
	// The shared no-follow bounded reader anchors both file and directory identity;
	// enforce private permissions before accepting operator declarations.
	raw, parent, file, err := readSuiteKagentBindings(path)
	if err != nil || parent.Mode().Perm()&0077 != 0 || file.Mode().Perm()&0077 != 0 || len(raw) > 16<<10 {
		return nil, errSuiteSessionsHost
	}
	var host suiteSessionsHostFile
	if agentsuite.Unmarshal(raw, &host) != nil || host.SchemaVersion != 1 || !suiteSessionsLoopback(host.Address) {
		return nil, errSuiteSessionsHost
	}
	if _, err := newSuiteSessionsSubject(suite, host.Observation); err != nil {
		return nil, errSuiteSessionsHost
	}
	return &suiteSessionsHostSnapshot{path, parent, file, raw, host}, nil
}
func suiteSessionsLoopback(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	number, err := strconv.Atoi(port)
	return ip != nil && ip.IsLoopback() && err == nil && number > 0 && number <= 65535 && strconv.Itoa(number) == port
}
func (s *suiteSessionsHostSnapshot) recheck(suite *workspace.NativeSuite) error {
	if s == nil {
		return errSuiteSessionsHost
	}
	current, err := snapshotSuiteSessionsHost(s.path, suite)
	if err != nil || !os.SameFile(s.parent, current.parent) || !os.SameFile(s.file, current.file) || !bytes.Equal(s.raw, current.raw) {
		return errSuiteSessionsHost
	}
	return nil
}

// Retain typed causes for callers without printing untrusted source, writer or
// remote diagnostics through the CLI's ordinary error formatting.
type suiteSessionsEvaluationError struct{ cause error }

func (e *suiteSessionsEvaluationError) Error() string {
	if errors.Is(e.cause, errSuitePassingExport) {
		return "agentsessions receipt publication incomplete; output may exist; Sessions may remain, no automatic retry or cleanup"
	}
	return "agentsessions suite evaluation did not pass; Sessions may remain, no automatic retry or cleanup"
}
func (e *suiteSessionsEvaluationError) Unwrap() error { return e.cause }

// Development selection is explicit consent to retained journal content and model
// execution on an operator-owned host, not a registry placement or auth fallback.
func (a *App) evaluateSuiteSessionsWorkspace(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation) (err error) {
	defer func() {
		if err != nil {
			err = &suiteSessionsEvaluationError{cause: errors.Join(err, ctx.Err())}
		}
	}()
	if p == nil || p.suite == nil || p.output == nil || p.started || p.sessions == nil || a.Out == nil || a.Err == nil {
		return errSuiteSessionsHost
	}
	recheck := func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		if err := workspace.CheckCurrent(checkCtx, p.root, p.prepared); err != nil {
			return err
		}
		if err := p.sessions.recheck(p.suite); err != nil {
			return err
		}
		return p.output.unchanged()
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	subject, err := newSuiteSessionsSubject(p.suite, p.sessions.host.Observation)
	if err != nil {
		return err
	}
	p.started = true
	if _, err := fmt.Fprintln(a.Err, suiteSessionsDevelopmentWarning); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(a.Err, "Journal: instructions, inputs, answers and per-exec config are retained in cleartext; retention is operator-managed. Closing the connection does not delete Sessions or journal content."); err != nil {
		return err
	}
	for _, frame := range p.inventory {
		if frame.Metadata.Runs > 1 {
			if _, err := fmt.Fprintf(a.Err, "Run preflight: %d fresh Sessions and Execs, %s per run, %s total execution budget (upper bound; cancellation can stop earlier).\n", len(p.inventory), p.options.CaseTimeout, time.Duration(len(p.inventory))*p.options.CaseTimeout); err != nil {
				return err
			}
			break
		}
	}
	if p.judge != nil {
		count := 0
		for _, frame := range p.inventory {
			for _, assertion := range frame.Case.Assertions {
				if assertion.Type == "llmJudge" {
					count += 2
				}
			}
		}
		if _, err := fmt.Fprintf(a.Err, "Judge preflight: %d independent requests over case inputs, attributed answers and authored rubrics; shares each run\x27s deadline. Identity/version are user-declared, not provider attestation. Reasoning is discarded.\n", count); err != nil {
			return err
		}
	}
	client, err := agentsessions.Dial(agentsessions.Options{Address: p.sessions.host.Address})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	if _, err := fmt.Fprintf(a.Out, "Workspace: %q; AgentSuite: %s\n", p.root, p.suite.Digest); err != nil {
		return err
	}
	observations := make([]suiteSessionsObservation, 0, len(p.inventory))
	sessions, executions := map[string]bool{}, map[string]bool{}
	_, err = evaluateSuiteRuns(ctx, p.inventory, p.options.CaseTimeout, a.Out, func(runCtx context.Context, frame suitePassingInventoryCase) (suitePassingObservation, error) {
		var observed suitePassingObservation
		if err := recheck(runCtx); err != nil {
			return observed, err
		}
		result, err := client.RunSuiteCase(runCtx, agentsessions.SuiteCaseRequest{SuiteDigest: p.suite.Digest, Member: p.suite.MemberID, CaseDigest: frame.Metadata.CaseDigest, Instructions: string(p.suite.Instructions), Model: p.suite.Model, Input: frame.Case.Input, UsedSessionUIDs: sessions})
		if err != nil {
			return observed, err
		}
		if err := recheck(runCtx); err != nil {
			return observed, err
		}
		if sessions[result.SessionUID] || executions[result.ExecutionID] {
			return observed, errSuitePassingReceipt
		}
		sessions[result.SessionUID], executions[result.ExecutionID] = true, true
		evidence := agentruntime.EvaluationEvidence{AnswerAvailable: true, Answer: result.Output}
		rows := agentruntime.EvaluateAssertions(frame.Case, evidence)
		if p.judge != nil {
			rows = p.judge(runCtx, frame.Case, evidence)
		}
		if err := recheck(runCtx); err != nil {
			return observed, err
		}
		observed.AnswerSHA256, observed.Assertions = agentruntime.EvaluationAnswerDigest(result.Output), rows
		observations = append(observations, suiteSessionsObservation{Result: result, Assertions: rows})
		return observed, nil
	})
	if err != nil {
		return err
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	receipt, err := newSuiteSessionsPassingReceipt(ctx, p, subject, observations)
	if err != nil {
		return err
	}
	// Report before publication: an output failure must not leave usable evidence.
	if _, err := fmt.Fprintln(a.Out, "Full passing AgentSessions evidence; unsigned operator-observed development proof."); err != nil {
		return err
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	return p.publishPassingReceipt(ctx, receipt)
}
