package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSuiteManagedOutputCannotOccupyCompletionRecord(t *testing.T) {
	_, _, f, state := publicNativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	opt.ManagedPass = true
	opt.Out = ""
	opt.ResultPort = f.opt.ResultPort
	prepared, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	root, cache, suite := prepared.root, prepared.store.cache, prepared.suite
	// A user copying a record pathname from diagnostics must not put an ordinary
	// --out receipt into the reserved completion-record namespace.
	opt.Out = prepared.store.completion.path
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err == nil {
		p.Close()
		t.Fatal("explicit output accepted inside managed completion namespace")
	}
	if len(state.creates) != 0 || len(state.deletes) != 0 {
		t.Fatal("unsafe output had native effects")
	}
	store, err := openSuitePassStore(t.Context(), root, cache, suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// The failed selection neither poisoned the partition nor retained its lock.
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type suitePassCancelOnCoherence struct {
	context.Context
	checks int
}

func (c *suitePassCancelOnCoherence) Err() error {
	c.checks++
	if c.checks >= 2 {
		return context.Canceled
	}
	return nil
}

func TestSuitePassStoreCancellationPreservesIdentityAndProof(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	record := commitStoreReceipt(t, store, suite, raw)
	ctx := &suitePassCancelOnCoherence{Context: t.Context()}
	if _, _, err := store.latest(ctx, suite); !errors.Is(err, context.Canceled) {
		t.Fatalf("coherence cancellation misreported as unsafe store: %v", err)
	}
	if _, selected, err := store.latest(t.Context(), suite); err != nil || selected != record {
		t.Fatalf("cancellation revoked proof: %s %v", selected, err)
	}
}

type suitePassCancelOnVisibility struct {
	context.Context
	path string
}

func (c suitePassCancelOnVisibility) Err() error {
	if _, err := os.Stat(c.path); err == nil {
		return context.Canceled
	}
	return nil
}

func TestSuitePassStoreCancellationAfterLinkKeepsVisibleProof(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := store.completion.path
	ctx := suitePassCancelOnVisibility{Context: t.Context(), path: path}
	record, err := store.commit(ctx, raw, suite)
	if !errors.Is(err, context.Canceled) || record != path {
		t.Fatalf("post-link cancellation lost record identity: %s %v", record, err)
	}
	if _, selected, err := store.latest(t.Context(), suite); err != nil || selected != record {
		t.Fatalf("visible proof revoked: %s %v", selected, err)
	}
	if filepath.Base(record) != "00000000000000000001.json" {
		t.Fatal("wrong committed sequence")
	}
}
