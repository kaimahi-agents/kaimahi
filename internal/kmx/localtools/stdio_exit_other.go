//go:build unix && !linux && !darwin

package localtools

// No leader-exit proof is qualified on other Unix targets. Cleanup still kills
// and reaps, but cannot authorize a successful completion without that proof.
func newStdioLeaderObserver(int) (*stdioExitObserver, error) { return nil, ErrStdioCleanup }
