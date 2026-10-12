package kubectlfake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func requestForTest(t *testing.T) Request {
	return Request{Context: t.Context(), Getenv: Environment(nil), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
}

func TestDispatchExitIncludingZeroStopsNestedHandler(t *testing.T) {
	for _, code := range []int{0, 1, 2} {
		reached := false
		err := dispatch(requestForTest(t), func(Request) { func() { exit(code) }(); reached = true })
		if reached {
			t.Fatal("exit returned to caller")
		}
		if code == 0 {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var status *ExitError
			if !errors.As(err, &status) || status.Code != code {
				t.Fatalf("exit %d: %v", code, err)
			}
		}
	}
}

func TestDispatchRepanicsUnrelatedValues(t *testing.T) {
	value := &struct{ label string }{"unrelated"}
	defer func() {
		if got := recover(); got != value {
			t.Fatalf("panic = %v, want original value", got)
		}
	}()
	_ = dispatch(requestForTest(t), func(Request) { panic(value) })
	t.Fatal("panic swallowed")
}

type failingWriter struct {
	err   error
	short bool
}

func (w failingWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, w.err
}

func TestDispatchObservesIgnoredOutputErrors(t *testing.T) {
	failure := errors.New("bounded output failed")
	for _, stream := range []string{"stdout", "stderr"} {
		for _, short := range []bool{false, true} {
			r := requestForTest(t)
			writer := failingWriter{err: failure, short: short}
			if stream == "stdout" {
				r.Stdout = writer
			} else {
				r.Stderr = writer
			}
			err := dispatch(r, func(r Request) {
				fmt.Fprint(r.Stdout, "stdout")
				fmt.Fprint(r.Stderr, "stderr")
				exit(0)
			})
			want := failure
			if short {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s short=%t: %v", stream, short, err)
			}
		}
	}
}

func TestDelayHonorsContextWithoutContinuingHandler(t *testing.T) {
	r := requestForTest(t)
	ctx, cancel := context.WithCancel(r.Context)
	r.Context = ctx
	reached := false
	cancel()
	err := dispatch(r, func(r Request) { r.delay(time.Hour); reached = true })
	if !errors.Is(err, context.Canceled) || reached {
		t.Fatalf("cancelled request continued: %v, %t", err, reached)
	}
	r.Context = t.Context()
	err = dispatch(r, func(r Request) {
		ctx, cancel := context.WithCancel(r.Context)
		r.Context = ctx
		cancel()
		r.delay(time.Hour)
		reached = true
	})
	var status *ExitError
	if !errors.As(err, &status) || reached {
		t.Fatalf("cancelled delay continued: %v, %t", err, reached)
	}
}

func TestEnvironmentLastDuplicateAndMissing(t *testing.T) {
	getenv := Environment([]string{"KEY=first", "EMPTY=", "KEY=last=tail", "invalid"})
	if getenv("KEY") != "last=tail" || getenv("EMPTY") != "" || getenv("missing") != "" {
		t.Fatal("incorrect effective environment")
	}
}
