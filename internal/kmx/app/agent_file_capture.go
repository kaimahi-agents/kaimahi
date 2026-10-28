package app

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

// RecordingSaver is trusted authoring authority, never derived from a display path.
// It must publish once with the frozen file identity/revision or refuse the save.
type RecordingSaver func(context.Context, []agentfile.Recording) (string, error)

func pendingRecording(ex agentfile.Example, name string) bool {
	if ex.Mocks != nil {
		for _, tool := range ex.Mocks.Tools {
			if tool.Name == name {
				return tool.Mode == "recorded" && len(tool.Calls) == 0
			}
		}
	}
	return false
}

func prepareRecordingSave(ctx context.Context, opt FilePreviewOptions, revision string, selected []agentfile.Example, settings []localtools.ToolSetting) (RecordingSaver, error) {
	for _, ex := range selected {
		if ex.Mocks == nil {
			continue
		}
		for _, tool := range ex.Mocks.Tools {
			if !pendingRecording(ex, tool.Name) || localtools.IsDisabled(settings, tool.Name) {
				continue
			}
			if opt.pair != nil {
				if opt.PreparePairRecordingSave == nil {
					return nil, agentfile.ErrRecordingSave
				}
				save, err := opt.PreparePairRecordingSave(ctx, opt.pair)
				if err != nil {
					return nil, err
				}
				if save == nil {
					return nil, agentfile.ErrRecordingSave
				}
				return func(ctx context.Context, recordings []agentfile.Recording) (string, error) {
					ack, err := save(ctx, recordings)
					if err != nil {
						return "", err
					}
					if err := validatePairRecordingSave(opt.pair, ack); err != nil {
						return "", err
					}
					*opt.pairSave = ack
					return ack.Revision, nil
				}, nil
			}
			if opt.PrepareRecordingSave == nil {
				return nil, agentfile.ErrRecordingSave
			}
			save, err := opt.PrepareRecordingSave(ctx, revision)
			if err == nil && save == nil {
				err = agentfile.ErrRecordingSave
			}
			return save, err
		}
	}
	return nil, nil
}

func recordingReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, agentfile.ErrRecordingChanged):
		return "recording_source_changed"
	case errors.Is(err, agentfile.ErrRecordingInvalid):
		return "recording_unrepresentable"
	default:
		return "recording_save_failed"
	}
}

func saveFileRecordings(ctx context.Context, save RecordingSaver, outcome agentpreview.Outcome, selected []agentfile.Example, result *FilePreviewResult, opt FilePreviewOptions) error {
	expected := map[[2]string]bool{}
	for i, row := range result.Examples {
		for _, tool := range row.Tools {
			if pendingRecording(selected[i], tool.Name) && !localtools.IsDisabled(result.ToolSettings, tool.Name) && tool.Source == "live" && tool.CallCount > 0 {
				expected[[2]string{row.ID, tool.Name}] = true
			}
		}
	}
	if len(expected) != len(outcome.Recordings) {
		return agentfile.ErrRecordingSave
	}
	for _, recording := range outcome.Recordings {
		key := [2]string{recording.ExampleID, recording.Tool.Name}
		if !expected[key] || recording.Tool.Mode != "recorded" || len(recording.Tool.Calls) != 1 {
			return agentfile.ErrRecordingSave
		}
		expected[key] = false
	}
	if len(expected) == 0 {
		return nil
	}
	if save == nil || outcome.NoJournal || outcome.Journal == "" {
		return agentfile.ErrRecordingSave
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	revision, err := save(ctx, outcome.Recordings)
	if err != nil {
		return err
	}
	if len(revision) != 71 || !strings.HasPrefix(revision, "sha256:") || opt.pair == nil && revision == result.File.Revision || strings.ToLower(revision) != revision {
		return agentfile.ErrRecordingSave
	}
	if _, err := hex.DecodeString(revision[7:]); err != nil {
		return agentfile.ErrRecordingSave
	}
	if opt.pair != nil {
		if opt.pairSave == nil || validatePairRecordingSave(opt.pair, *opt.pairSave) != nil {
			return agentfile.ErrRecordingSave
		}
		target := opt.pairSave.Target
		target.Path = pairPreviewPath(target.Path, opt.FullPath)
		result.RecordingTarget, result.SavedSnapshotRevision = &target, opt.pairSave.SnapshotRevision
	}
	result.SavedRevision = revision
	for i := range result.Examples {
		for j := range result.Examples[i].Tools {
			tool := &result.Examples[i].Tools[j]
			if _, ok := expected[[2]string{result.Examples[i].ID, tool.Name}]; ok {
				tool.RecordingSaved = true
			}
		}
	}
	return nil
}
