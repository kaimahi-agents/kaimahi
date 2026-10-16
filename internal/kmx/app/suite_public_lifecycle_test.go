package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestSuitePublicCompletionLifecycle(t *testing.T) {
	for _, variant := range []string{"complete", "missing-build", "close", "cleanup", "cancel", "source-drift"} {
		t.Run(variant, func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			if err := store.reserve(t.Context()); err != nil {
				t.Fatal(err)
			}
			prepared, err := workspace.Prepare(t.Context(), store.workspace, store.cache)
			if err != nil {
				t.Fatal(err)
			}
			output, err := preflightSuitePassingExport(t.Context(), filepath.Join(store.partition, ".pending-public-test"), store.workspace, store.cache)
			if err != nil {
				t.Fatal(err)
			}
			m := publicMetadataFixture()
			m.CompletedAt = ""
			m.StartedAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			p := &PreparedSuiteWorkspaceEvaluation{prepared: prepared, store: store, suite: suite, output: output, completed: raw, root: store.workspace, publicMetadata: m, options: SuiteWorkspaceEvaluationOptions{ManagedPass: true}}
			var evaluationErr error
			ctx := t.Context()
			switch variant {
			case "missing-build":
				m.Producer = suitePublicProducer{}
			case "close":
				output.directory.Close()
			case "cleanup":
				evaluationErr = errors.New("private-cleanup-canary")
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "source-drift":
				writeFile(t, filepath.Join(store.workspace, "eval/second.yaml"), "id: changed\ninput: private-prompt-canary\nexpectContains: [answer]\n")
			}
			err = (&App{Err: io.Discard}).finishManagedEvaluation(ctx, p, evaluationErr)
			if variant == "complete" || variant == "missing-build" {
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.latest(t.Context(), suite); err != nil {
					t.Fatal("private proof failed", err)
				}
				got, e := store.readPublicMetadata(t.Context(), suite)
				if variant == "complete" {
					if e != nil || got.StartedAt != m.StartedAt || got.CompletedAt == "" {
						t.Fatal("completion missing", got, e)
					}
					completed, _ := time.Parse(time.RFC3339Nano, got.CompletedAt)
					if completed.After(time.Now()) {
						t.Fatal("invented future completion")
					}
				} else if e == nil {
					t.Fatal("missing provenance eligible")
				}
			} else {
				if err == nil {
					t.Fatal("failed lifecycle committed")
				}
				entries, _ := os.ReadDir(store.partition)
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "public-") || suitePassRecordName.MatchString(entry.Name()) {
						t.Fatal("failed lifecycle created eligibility", entry.Name())
					}
				}
			}
		})
	}
}

type publicWarningSink struct {
	writes int
	always bool
}

func (w *publicWarningSink) Write(p []byte) (int, error) {
	w.writes++
	if w.always || w.writes == 1 {
		return 0, errors.New("private-warning-writer-canary")
	}
	return len(p), nil
}

func TestSuitePublicWarningFailurePreservesPrivateCommit(t *testing.T) {
	for _, always := range []bool{false, true} {
		t.Run(fmt.Sprint(always), func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			if err := store.reserve(t.Context()); err != nil {
				t.Fatal(err)
			}
			prepared, err := workspace.Prepare(t.Context(), store.workspace, store.cache)
			if err != nil {
				t.Fatal(err)
			}
			output, err := preflightSuitePassingExport(t.Context(), filepath.Join(store.partition, ".pending-warning"), store.workspace, store.cache)
			if err != nil {
				t.Fatal(err)
			}
			p := &PreparedSuiteWorkspaceEvaluation{prepared: prepared, store: store, suite: suite, output: output, completed: raw, root: store.workspace, publicMetadata: publicMetadataFixture(), options: SuiteWorkspaceEvaluationOptions{ManagedPass: true}}
			p.publicMetadata.Producer = suitePublicProducer{}
			writer := &publicWarningSink{always: always}
			err = (&App{Err: writer}).finishManagedEvaluation(t.Context(), p, nil)
			if err == nil || strings.Contains(err.Error(), "private-warning-writer-canary") {
				t.Fatal("warning failure swallowed or leaked", err)
			}
			if _, _, err := store.latest(t.Context(), suite); err != nil {
				t.Fatal("warning failure poisoned private eligibility", err)
			}
		})
	}
}

func TestSuitePublicSourceAndOptionalDigestIndependent(t *testing.T) {
	producer := publicProducerFixture()
	producer.Revision = strings.Repeat("a", 64)
	if _, err := producer.publicVersion(); err != nil {
		t.Fatal("64-digit VCS revision rejected", err)
	}
	store, suite, raw := passStoreFixture(t)
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := publicMetadataFixture()
	m.BinaryDigest = ""
	if err := store.publishPublicMetadata(t.Context(), raw, suite, m); err != nil {
		t.Fatal("optional binary digest required", err)
	}
	if _, err := store.commit(t.Context(), raw, suite); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readPublicMetadata(t.Context(), suite); err != nil {
		t.Fatal(err)
	}
}
