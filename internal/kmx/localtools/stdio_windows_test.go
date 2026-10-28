//go:build windows

package localtools

import (
	"errors"
	"os"
	"testing"
)

func TestStdioWindowsRemainsUnavailable(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := StartStdioHost(t.Context(), StdioConfig{Command: program, Dir: t.TempDir(), Tools: map[string]string{"ping": "ping"}})
	if !errors.Is(err, ErrStdio) || owner != nil {
		t.Fatalf("Windows adapter must remain inert: %v", err)
	}
}
