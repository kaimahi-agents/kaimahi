//go:build !windows

package app

import (
	"errors"
	"strings"
	"testing"
)

func TestSuitePassStorePreLinkFailureReportsNoRecord(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	store.completion.beforeLink = func() error { return errors.New("pre-link refusal") }
	record, err := store.commit(t.Context(), raw, suite)
	if err == nil || record != "" || strings.Contains(err.Error(), "visibility is unresolved") {
		t.Fatalf("pre-link failure = record %q, err %v", record, err)
	}
}
