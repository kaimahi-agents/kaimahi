package mcp

import "os"

// Private source-history filesystem semantics are not qualified on Windows.
func openHistory(string, string, bool) (*historyStore, error) {
	return nil, refusal("history_unavailable")
}
func historyOwned(os.FileInfo, bool) bool { return false }
func historyOpenFile(*os.Root, string, int) (*os.File, error) {
	return nil, refusal("history_unavailable")
}
