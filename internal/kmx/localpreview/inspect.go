package localpreview

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/agent-primitives/sessions/api"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

// ReplayLabel marks journal inspection. Replay re-reads recorded Sessions; it
// sends no model request, evaluates no assertions and is never a fresh test
// result, passing proof or deployment evidence.
const ReplayLabel = Label + " journal replay; not a test result"

// Inspection is a read-only view of one retained run. Sessions carry the
// recorded instructions' input and answer content; they have no verdicts.
type Inspection struct {
	Label      string
	RunID      string
	Sessions   []agentsessions.PreviewSession
	ModelCalls int64
}

// DefaultInspectTimeout bounds Inspect when ctx has no deadline.
const DefaultInspectTimeout = 2 * time.Minute

// Inspect reopens a retained run's journal and reconstructs each preview
// Session. Every live model call is refused and counted; complete turns are
// replayed through the chat harness against their recorded completions.
// Without a ctx deadline, Inspect applies DefaultInspectTimeout. It returns a
// *Refusal or a context error.
func Inspect(ctx context.Context, dir, runID string) (out Inspection, err error) {
	out = Inspection{Label: ReplayLabel, RunID: runID}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultInspectTimeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	path, err := JournalPath(dir, runID)
	if err != nil {
		return out, err
	}
	if err := privateDir(dir, false); err != nil {
		return out, err
	}
	// Never create a journal while inspecting: this check gives the
	// specific refusal, and the read-write-only open below closes the race
	// with a concurrent Clear or prune.
	if info, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return out, refuse(RefusedJournal, "the selected local preview run is not retained", "list runs and select a retained one")
	} else if err != nil || !info.Mode().IsRegular() {
		return out, refuse(RefusedJournal, "the selected local preview journal is not a regular file", "clear the run")
	}
	directory, err := fileaccess.OpenPrivateDirectory(ctx, dir)
	if err != nil {
		return out, refuse(RefusedJournal, "the local preview journal directory is unavailable or unsafe", "clear the run")
	}
	journal, journalErr := fileaccess.OpenPrivateFileAt(ctx, directory, filepath.Base(path))
	if journalErr == nil {
		journalErr = fileaccess.CheckPrivate(ctx, journal, false)
		journalErr = errors.Join(journalErr, journal.Close())
	}
	directoryErr := directory.Close()
	if journalErr != nil || directoryErr != nil {
		return out, refuse(RefusedJournal, "the selected local preview journal is unavailable or unsafe", "clear the run")
	}
	var calls atomic.Int64
	forbidden := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		return api.ModelResponse{}, refuse(RefusedRequest, "journal inspection never calls a model", "run a new preview for a fresh answer")
	}
	h, err := openHost(path, "", forbidden)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, h.close()) }()
	// Sessions client errors can quote journal content; report fixed text.
	replayErr := func() error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return refuse(RefusedJournal, "the local preview journal could not be replayed", "run a new preview, or clear this run")
	}
	uids, err := h.client.ListPreviewSessions(ctx)
	if err != nil {
		return out, replayErr()
	}
	// ListSessions is newest first; report in execution order.
	for i := len(uids) - 1; i >= 0; i-- {
		session, err := h.client.InspectPreviewSession(ctx, uids[i])
		if err != nil {
			out.ModelCalls = calls.Load()
			return out, replayErr()
		}
		out.Sessions = append(out.Sessions, session)
	}
	out.ModelCalls = calls.Load()
	return out, nil
}
