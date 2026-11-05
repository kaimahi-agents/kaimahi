package app

import (
	"context"
	"errors"
	"strings"
)

// One invocation budget spans native Tool reads and the other target observers.
type discoveryReadSession struct {
	context       string
	bytes, pages  int
	failure       *discoveryReadFailure
	verifyRuntime func() error
	exhausted     bool
}

func (s *discoveryReadSession) capture(ctx context.Context, a *App, args ...string) ([]byte, *discoveryReadFailure) {
	page := len(args) == 3 && args[0] == "get" && args[1] == "--raw" && strings.Contains(args[2], "?")
	if s.exhausted || s.bytes >= 8<<20 || (page && s.pages >= 32) {
		s.exhausted = true
		f := &discoveryReadFailure{reason: "read_unknown"}
		if s.failure == nil {
			s.failure = f
		}
		return nil, f
	}
	if page {
		s.pages++
	}
	raw, f, n := a.discoveryCaptureBudget(ctx, s.context, min(4<<20, (8<<20)-s.bytes), args...)
	s.bytes += n
	if f != nil {
		if s.failure == nil {
			s.failure = f
		}
		if errors.Is(f.cause, errDiscoveryLimit) {
			s.exhausted = true
		}
	}
	return raw, f
}
