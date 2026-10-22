package app

import "testing"

func testFIFO(t *testing.T, path string, mode uint32) {
	t.Helper()
	t.Skip("FIFO fixture requires Unix")
}

func assertOrkaForwardExited(t *testing.T, dir string) {
	t.Helper()
	t.Skip("process-exit assertion requires Unix signals")
}
