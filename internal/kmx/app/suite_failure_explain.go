package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

type SuiteEvaluationExplainOptions struct {
	Workspace, File, ID string
	ShowContent, Clear  bool
}

// ExplainSuiteEvaluation reads only the workspace descriptor and private local
// diagnostics. It neither prepares source nor loads operational configuration.
func (a *App) ExplainSuiteEvaluation(ctx context.Context, opt SuiteEvaluationExplainOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opt.ID != "" && !suiteFailureID.MatchString(opt.ID) {
		return errors.New("invalid evaluation failure ID")
	}
	if opt.Clear && opt.ShowContent {
		return errors.New("--clear and --show-content are mutually exclusive")
	}
	if strings.TrimSpace(opt.Workspace) != "" && strings.TrimSpace(opt.File) != "" {
		return errors.New("--workspace and --file are mutually exclusive")
	}
	if opt.Clear && opt.ID == "" && strings.TrimSpace(opt.Workspace) == "" && strings.TrimSpace(opt.File) == "" {
		return errors.New("clearing all failure diagnostics requires explicit --workspace DIR or --file FILE")
	}
	if strings.TrimSpace(opt.File) != "" {
		return a.explainFileFailure(ctx, opt)
	}
	var start string
	if !filepath.IsAbs(opt.Workspace) {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return errSuiteFailureStore
		}
	}
	root, err := workspace.Resolve(start, opt.Workspace)
	if err != nil {
		return errors.New("evaluation failure inspection requires an existing workspace; use --workspace DIR")
	}
	out := a.Out
	if out == nil {
		out = io.Discard
	}
	if opt.Clear {
		count, err := clearSuiteFailures(ctx, root, opt.ID)
		if errors.Is(err, errSuiteFailureMissing) && opt.ID == "" {
			count, err = 0, nil
		}
		if err != nil {
			if count > 0 {
				return errors.New("some evaluation failures may have been cleared; inspect private KMX state")
			}
			return err
		}
		return writeSuiteFailureExplanation(out, fmt.Sprintf("Cleared %d local evaluation failure file(s).\n", count), true)
	}
	s, err := openSuiteFailureStore(root, false)
	if errors.Is(err, errSuiteFailureMissing) && opt.ID == "" {
		return writeSuiteFailureExplanation(out, "No retained evaluation failures.\n", false)
	}
	if err != nil {
		return err
	}
	entries, scanErr := s.scan()
	closeErr := s.Close()
	if scanErr != nil {
		return scanErr
	}
	if closeErr != nil {
		return closeErr
	}
	var report strings.Builder
	found := false
	for _, entry := range entries {
		d := entry.record.Detail
		if opt.ID != "" && d.ID != opt.ID {
			continue
		}
		found = true
		fmt.Fprintf(&report, "%s · %s · %q run %d/%d · %s %q · %s\n", d.ID, d.Verdict, d.CaseID, d.Run, d.Runs, d.Runtime, d.MemberID, d.SuiteDigest)
		if opt.ID != "" || opt.ShowContent {
			fmt.Fprintf(&report, "  operation %s; complete=%t attributed=%t; local diagnostics, not evidence\n", d.OperationID, d.Complete, d.Attributed)
			if d.ContentTruncated {
				fmt.Fprintf(&report, "  retained content truncated; %d trailing assertions omitted\n", d.OmittedAssertions)
			}
			if err := writeSuiteFailureDetail(&report, d, opt.ShowContent); err != nil {
				return err
			}
		}
	}
	if !found {
		if opt.ID != "" {
			return errSuiteFailureMissing
		}
		report.WriteString("No retained evaluation failures.\n")
	}
	if secretshapes.Match(report.String()) != nil {
		return errSuiteFailureStore
	}
	return writeSuiteFailureExplanation(out, report.String(), false)
}

func (a *App) explainFileFailure(ctx context.Context, opt SuiteEvaluationExplainOptions) error {
	filename, err := filepath.Abs(opt.File)
	if err != nil {
		return errSuiteFailureStore
	}
	scope, currentRevision, err := currentFileFailureScopeRevision(filename)
	if err != nil {
		return errors.New("file failure inspection requires an existing readable agent file; use the exact --file FILE subject")
	}
	out := a.Out
	if out == nil {
		out = io.Discard
	}
	if opt.Clear {
		count, err := clearFailureStore(ctx, opt.ID, func() (*suiteFailureStore, error) { return openFileFailureStoreBySubject(scope.subject) })
		if errors.Is(err, errSuiteFailureMissing) && opt.ID == "" {
			count, err = 0, nil
		}
		if err != nil {
			if count > 0 {
				return errors.New("some file failures may have been cleared; inspect private KMX state")
			}
			return err
		}
		return writeSuiteFailureExplanation(out, fmt.Sprintf("Cleared %d local file failure file(s).\n", count), true)
	}
	store, err := openFileFailureStoreBySubject(scope.subject)
	if errors.Is(err, errSuiteFailureMissing) && opt.ID == "" {
		return writeSuiteFailureExplanation(out, "No retained file failures.\n", false)
	}
	if err != nil {
		return err
	}
	entries, scanErr := store.scan()
	closeErr := store.Close()
	if scanErr != nil {
		return scanErr
	}
	if closeErr != nil {
		return closeErr
	}
	var report strings.Builder
	found := false
	for _, entry := range entries {
		if entry.file == nil || opt.ID != "" && entry.file.Detail.ID != opt.ID {
			continue
		}
		found = true
		record := entry.file
		status := "current"
		if currentRevision != record.Revision {
			status = "historical"
		}
		fmt.Fprintf(&report, "%s · %s · %q · %s/%s · %s\n", record.Detail.ID, record.Detail.Verdict, record.Detail.CaseID, record.Model.Provider, record.Model.Model, status)
		if opt.ID != "" || opt.ShowContent {
			fmt.Fprintf(&report, "  revision %s; operation %s; scope=%s; agent=%q; complete=%t attributed=%t; local diagnostics, not evidence\n", record.Revision, record.Operation, record.Selection, record.AgentID, record.Detail.Complete, record.Detail.Attributed)
			fmt.Fprintf(&report, "  judge %s/%s\n", record.Judge.Provider, record.Judge.Model)
			for _, assertion := range record.Assertions {
				fmt.Fprintf(&report, "  assertion %s (%s) %s; definition %s; reason=%s\n", assertion.ID, assertion.Type, assertion.Verdict, assertion.Digest, assertion.ReasonCode)
			}
			if record.Detail.ContentTruncated {
				fmt.Fprintf(&report, "  retained content truncated; %d trailing assertions omitted\n", record.Detail.OmittedAssertions)
			}
			if err := writeSuiteFailureDetail(&report, record.Detail, opt.ShowContent); err != nil {
				return err
			}
		}
	}
	if !found {
		if opt.ID != "" {
			return errSuiteFailureMissing
		}
		report.WriteString("No retained file failures.\n")
	}
	if secretshapes.Match(report.String()) != nil {
		return errSuiteFailureStore
	}
	return writeSuiteFailureExplanation(out, report.String(), false)
}

func writeSuiteFailureExplanation(out io.Writer, text string, cleared bool) error {
	n, err := io.WriteString(out, text)
	if err != nil || n != len(text) {
		if cleared {
			return errors.New("local evaluation failures cleared; report delivery failed")
		}
		return errors.New("evaluation failure report delivery failed")
	}
	return nil
}
