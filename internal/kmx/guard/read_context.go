package guard

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/muesli/cancelreader"
)

func readLineContext(ctx context.Context, in *os.File) (string, error) {
	if ctx.Done() == nil {
		return readLine(in), nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// cancelreader v0.2.2's fallback cannot interrupt a pending Read, and
	// Windows cancellation can fail during overlapped reads. Fail closed on
	// those paths; explicit pre-confirmation and legacy Check still work.
	switch runtime.GOOS {
	case "linux":
	case "darwin", "freebsd", "netbsd", "openbsd", "dragonfly", "solaris":
		// /dev/tty on BSD/macOS (and all Solaris files) uses select, whose
		// high-FD fallback cannot cancel. Reject before starting a reader.
		if (in.Name() == "/dev/tty" || runtime.GOOS == "solaris") && in.Fd() >= 1024 {
			return "", fmt.Errorf("cancellable terminal confirmation unavailable")
		}
	default:
		return "", fmt.Errorf("cancellable terminal confirmation unavailable")
	}
	reader, err := cancelreader.NewReader(in)
	if err != nil {
		return "", fmt.Errorf("cannot prepare cancellable terminal confirmation: %w", err)
	}
	defer reader.Close() // Closes owned polling resources, not the borrowed input.
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			reader.Cancel()
		case <-done:
		}
	}()
	// Read synchronously: no detached reader can consume a later prompt's
	// input. Join the cancellation watcher before closing its descriptors.
	answer := readLine(reader)
	close(done)
	<-stopped
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return answer, nil
}
