package app

// `kmx watch` — follow the plane's decisions as they are made.
//
// The session is held open for the whole watch rather than reopened per
// poll: the admin port is on no Service, so every open is a port-forward,
// and reopening one every two seconds would spend more time establishing
// the connection than reading through it.

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

// WatchOptions are `kmx watch`'s flags.
type WatchOptions struct {
	Credential string
	Interval   time.Duration
	Limit      int
	For        time.Duration
	JSON       bool
	Replay     int
}

// Watch follows the merged trail until interrupted.
//
// Ctrl-C ends it cleanly and exits zero: stopping a watch is the normal way
// to finish one, not a failure, and an operator who has been reading a feed
// should not be told their command failed when they close it.
func (a *App) Watch(opt WatchOptions) error {
	if opt.Interval <= 0 {
		opt.Interval = 2 * time.Second
	}
	if opt.Interval < time.Second {
		return fmt.Errorf("--interval %s is below one second; the plane's timestamps are "+
			"second-resolution, so polling faster reads the same second repeatedly", opt.Interval)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return a.session(func(c *admin.Client) error {
		return c.Watch(a.Out, ctx.Done(), admin.WatchOptions{
			Credential: opt.Credential,
			Interval:   opt.Interval,
			Limit:      opt.Limit,
			For:        opt.For,
			JSON:       opt.JSON,
			Replay:     opt.Replay,
		})
	})
}
