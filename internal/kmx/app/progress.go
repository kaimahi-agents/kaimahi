package app

import (
	"fmt"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// phase identifies one durable unit in a longer command. Native command
// output remains between the start and finish lines, so failures retain all of
// their original diagnostics while the overall journey stays easy to scan.
type phase struct {
	current int
	total   int
	name    string
}

func (a *App) timeNow() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *App) runPhase(p phase, fn func() error) error {
	started := a.timeNow()
	ui := a.presenter()
	fmt.Fprintf(a.Err, "\n%s  [%d/%d] %s\n", ui.Phase("PHASE"), p.current, p.total, p.name)
	err := fn()
	elapsed := a.timeNow().Sub(started)
	if err != nil {
		fmt.Fprintf(a.Err, "%s [%d/%d] %s (%s)\n", ui.Failure("FAILED"), p.current, p.total, p.name, formatElapsed(elapsed))
		return err
	}
	fmt.Fprintf(a.Err, "%s   [%d/%d] %s (%s)\n", ui.Success("DONE"), p.current, p.total, p.name, formatElapsed(elapsed))
	return nil
}

func (a *App) complete(label string, started time.Time) {
	ui := a.presenter()
	fmt.Fprintf(a.Err, "\n%s  %s (%s total)\n", ui.Success("COMPLETE"), label, formatElapsed(a.timeNow().Sub(started)))
}

func (a *App) presenter() progressPresenter {
	if a.progressUI != nil {
		return a.progressUI
	}
	return cliui.New(a.Err)
}

func formatElapsed(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed < time.Second {
		return elapsed.Round(10 * time.Millisecond).String()
	}
	if elapsed < time.Minute {
		return elapsed.Round(100 * time.Millisecond).String()
	}
	return elapsed.Round(time.Second).String()
}
