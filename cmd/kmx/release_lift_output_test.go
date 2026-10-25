package main

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
	"testing"
)

func TestSessionsPendingOutputFailureExitsOne(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			cmd := &cobra.Command{Use: "deploy"}
			cmd.SetOut(previewBrokenWriter{})
			summary := app.ReleaseLiftSummary{Runtime: "agentsessions", SessionsAgent: map[string]any{"kind": "Agent"}}
			err := reportReleaseLiftResult(cmd, format, summary, app.ReleaseLiftResult{State: "pending"}, app.ErrDestinationStatusPending)
			if got := exitCode(err); got != 1 {
				t.Fatalf("output failure exit=%d error=%v", got, err)
			}
		})
	}
}
