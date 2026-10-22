package config

import "testing"

func testFIFO(t *testing.T, path string) {
	t.Helper()
	t.Skip("FIFO fixture requires Unix")
}
