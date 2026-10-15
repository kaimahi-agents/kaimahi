package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
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
	// The shared no-follow bounded reader anchors both file and directory identity;
	// enforce private permissions before accepting operator declarations.
	raw, parent, file, err := readSuiteKagentBindings(path)
	if err != nil || parent.Mode().Perm()&0077 != 0 || file.Mode().Perm()&0077 != 0 || len(raw) > 16<<10 {
		return nil, errSuiteSessionsHost
	}
	var host suiteSessionsHostFile
	if spec.Unmarshal(raw, &host) != nil || host.SchemaVersion != 1 || !suiteSessionsLoopback(host.Address) {
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
	recheck := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := workspace.CheckCurrent(ctx, p.root, p.prepared); err != nil {
			return err
		}
		if err := p.sessions.recheck(p.suite); err != nil {
			return err
		}
		return p.output.unchanged()
	}
	if err := recheck(); err != nil {
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
	for _, frame := range p.inventory {
		if err := recheck(); err != nil {
			return err
		}
		caseCtx, cancel := context.WithTimeout(ctx, p.options.CaseTimeout)
		result, caseErr := client.RunSuiteCase(caseCtx, agentsessions.SuiteCaseRequest{SuiteDigest: p.suite.Digest, Member: p.suite.MemberID, CaseDigest: frame.Metadata.CaseDigest, Instructions: string(p.suite.Instructions), Model: p.suite.Model, Input: frame.Case.Input, UsedSessionUIDs: sessions})
		cancel()
		if caseErr != nil {
			if _, err := fmt.Fprintf(a.Out, "%s: unknown\n", frame.Case.ID); err != nil {
				return err
			}
			return caseErr
		}
		if err := recheck(); err != nil {
			return err
		}
		if sessions[result.SessionUID] || executions[result.ExecutionID] {
			return errSuitePassingReceipt
		}
		sessions[result.SessionUID], executions[result.ExecutionID] = true, true
		rows := agentruntime.EvaluateAssertions(frame.Case, agentruntime.EvaluationEvidence{AnswerAvailable: true, Answer: result.Output})
		verdict := agentruntime.EvaluationAssertionsVerdict(rows)
		if _, err := fmt.Fprintf(a.Out, "%s: %s\n", frame.Case.ID, verdict); err != nil {
			return err
		}
		if verdict != agentruntime.EvaluationPass {
			return errSuitePassingReceipt
		}
		observations = append(observations, suiteSessionsObservation{Result: result, Assertions: rows})
	}
	if err := recheck(); err != nil {
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
	if err := recheck(); err != nil {
		return err
	}
	return p.publishPassingReceipt(ctx, receipt)
}
