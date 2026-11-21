package workspacetest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func testPrepared(t *testing.T) *Prepared {
	t.Helper()
	p, err := New(Summary{
		Workspace:         "workspace",
		SuiteDigest:       "sha256:suite",
		SourceFingerprint: "sha256:source",
		Runtime:           "orka",
		Context:           "remote",
		Namespace:         "orka-system",
		Inference:         "provider:model",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreparedHasOneConcurrentWinner(t *testing.T) {
	p := testPrepared(t)
	const attempts = 16
	start := make(chan struct{})
	results := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- p.Start(t.Context())
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unexpected result: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want 1", winners)
	}
}

func TestPreparedCancellationAndClose(t *testing.T) {
	p := testPrepared(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Start(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled start = %v", err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatalf("pre-cancelled call consumed authority: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := p.Start(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed invocation started: %v", err)
	}
}

func TestPreparedZeroAndCopyCannotDuplicateAuthority(t *testing.T) {
	var zero Prepared
	if err := zero.Start(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("zero value started: %v", err)
	}
	p := testPrepared(t)
	copy := *p
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := copy.Start(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("shallow copy duplicated authority: %v", err)
	}
}

func TestPreparedConcurrentCloseAndReadIsSafe(t *testing.T) {
	p := testPrepared(t)
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	go func() {
		<-start
		_ = p.Summary()
		done <- struct{}{}
	}()
	go func() {
		<-start
		_ = p.Close()
		done <- struct{}{}
	}()
	close(start)
	<-done
	<-done
	if err := p.Start(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed invocation started: %v", err)
	}
}

func TestPreparedSummaryIsNonAuthoritative(t *testing.T) {
	p := testPrepared(t)
	summary := p.Summary()
	summary.Workspace = "changed"
	if p.Summary().Workspace != "workspace" {
		t.Fatal("summary mutation changed prepared identity")
	}
	if _, err := json.Marshal(p); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("prepared authority serialized: %v", err)
	}
	if err := json.Unmarshal([]byte(`{}`), p); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("prepared authority restored: %v", err)
	}
}
