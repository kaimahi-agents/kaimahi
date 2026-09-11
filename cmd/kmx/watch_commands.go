package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

// newWatchCommand follows the trail as it is written.
//
// A sibling of `flow` rather than a flag on it, because the two answer
// different questions and have opposite orderings of the same rows. `flow`
// reads oldest-first to explain how a cluster got here; this one appends as
// things happen, and appending is the only ordering a live feed can have.
//
// It is append-only rather than a full-screen view on purpose: the scrollback
// is the evidence, and a repainting dashboard throws it away.
func newWatchCommand(state *commandState) *cobra.Command {
	var opt app.WatchOptions
	cmd := &cobra.Command{
		Use:   "watch [credential]",
		Short: "Follow the plane's decisions as they happen",
		Args:  usageArgs(0, 1, "kmx watch [<credential>] [flags]"),
	}
	cmd.Flags().DurationVar(&opt.Interval, "interval", 2*time.Second, "how often to poll")
	cmd.Flags().IntVar(&opt.Limit, "limit", 0, "stop after this many events (0 follows until interrupted)")
	cmd.Flags().DurationVar(&opt.For, "for", 0, "stop after this long (0 has no deadline)")
	cmd.Flags().IntVar(&opt.Replay, "replay", 0, "print this many already-recorded events first")
	cmd.Flags().BoolVar(&opt.JSON, "json", false, "one JSON object per line, for scripts")
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.Credential = parseOptionalCredential(cmd.Flags().Args(), "")
		return a.Watch(opt)
	})
	return cmd
}
