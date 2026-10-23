package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// AgentRunProfileExportOptions selects one canonical source and a new private file.
type AgentRunProfileExportOptions struct{ File, Out string }

var errAgentRunProfileExport = errors.New("run profile export incomplete; output may exist; inspect the original destination before exporting again")

// ExportAgentRunProfile freezes a single canonical snapshot without workspace,
// configuration, model, registry or runtime access. It grants no execution proof.
func (*App) ExportAgentRunProfile(ctx context.Context, opt AgentRunProfileExportOptions) (agentfile.RunProfileSummary, error) {
	var zero agentfile.RunProfileSummary
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if strings.TrimSpace(opt.Out) == "" {
		return zero, errors.New("run export requires a new private --out file")
	}
	// Reject traversal before filepath.Abs in the shared publisher can clean it.
	if slices.Contains(strings.Split(filepath.ToSlash(opt.Out[len(filepath.VolumeName(opt.Out)):]), "/"), "..") {
		return zero, errors.New("run export requires an output path without parent traversal")
	}
	snapshot, err := agentfile.Load(opt.File)
	if err != nil {
		return zero, err
	}
	raw, summary, err := agentfile.FreezeRunProfile(snapshot)
	if err != nil {
		return zero, err
	}
	export, err := preflightDetachedExport(ctx, opt.Out, false)
	if err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			return zero, errors.Join(errors.New("run export private file publication is unavailable on native Windows"), errors.ErrUnsupported)
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, errors.New("run export requires a new file in an existing private nonsymlink output directory")
	}
	if err := publishAgentRunProfile(ctx, export, raw); err != nil {
		return zero, err
	}
	return summary, nil
}

// Close before returning a success summary, including post-publication errors.
// The suite publisher's receipt-specific diagnostics stay behind this boundary.
func publishAgentRunProfile(ctx context.Context, export *suitePassingExport, raw []byte) (err error) {
	defer func() {
		if export.Close() != nil {
			err = errors.Join(err, errAgentRunProfileExport)
		}
	}()
	if export.publishBytes(ctx, raw) != nil {
		return errors.Join(errAgentRunProfileExport, ctx.Err())
	}
	return nil
}
