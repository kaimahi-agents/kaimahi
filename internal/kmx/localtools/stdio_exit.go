//go:build unix

package localtools

// stdioLeaderExited retains the existing one-shot observer contract for stdio
// MCP ownership. Command tools keep a persistent observer across their complete
// call to cover short-lived Darwin processes.
func stdioLeaderExited(pid int) (bool, error) {
	observer, err := newStdioLeaderObserver(pid)
	if err != nil {
		return false, err
	}
	exited, observeErr := observer.observe()
	closeErr := observer.close()
	if observeErr != nil {
		return false, observeErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	return exited, nil
}
