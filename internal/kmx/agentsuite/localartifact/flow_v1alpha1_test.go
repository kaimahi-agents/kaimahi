package localartifact

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	suiteflow "github.com/agent-primitives/suite/experimental/flow"
)

func TestFlowContentLimitIsOnlyForFixedFlowPath(t *testing.T) {
	for _, name := range []string{suiteflow.FlowPathV1Alpha1, "coordination/other.json", "instructions/writer.md"} {
		body := bytes.Repeat([]byte("x"), fileBytes+1)
		raw := contentArchive(t, []tarRecord{{header: tar.Header{Name: name, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(body))}, body: body}})
		if _, err := Files(t.Context(), raw); !errors.Is(err, ErrArtifactLimit) {
			t.Fatalf("strict parser accepted %s: %v", name, err)
		}
		got, err := contentFiles(t.Context(), raw, true)
		if name == suiteflow.FlowPathV1Alpha1 {
			if err != nil || !bytes.Equal(got[name], body) {
				t.Fatalf("flow exception lost: %v", err)
			}
		} else if !errors.Is(err, ErrArtifactLimit) {
			t.Fatalf("exception widened to %s: %v", name, err)
		}
	}
	body := bytes.Repeat([]byte("x"), (4<<20)+1)
	raw := contentArchive(t, []tarRecord{{header: tar.Header{Name: suiteflow.FlowPathV1Alpha1, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(body))}, body: body}})
	if _, err := contentFiles(t.Context(), raw, true); !errors.Is(err, ErrArtifactLimit) {
		t.Fatalf("flow limit widened: %v", err)
	}
}

func TestFlowContentKeepsAggregateAndInventoryLimits(t *testing.T) {
	regular := func(name string, n int) tarRecord {
		return tarRecord{header: tar.Header{Name: name, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(n)}, body: bytes.Repeat([]byte("x"), n)}
	}
	for _, kind := range []string{"files", "entries", "total"} {
		t.Run(kind, func(t *testing.T) {
			var records []tarRecord
			switch kind {
			case "files":
				for i := 0; i <= MaxFiles; i++ {
					records = append(records, regular(fmt.Sprintf("file-%d", i), 0))
				}
			case "entries":
				for i := 0; i <= entriesLimit; i++ {
					records = append(records, tarRecord{header: tar.Header{Name: fmt.Sprintf("dir-%d/", i), Typeflag: tar.TypeDir, Mode: 0700}})
				}
			case "total":
				records = append(records, regular(suiteflow.FlowPathV1Alpha1, 4<<20))
				for i := 0; i < 4; i++ {
					records = append(records, regular(fmt.Sprintf("file-%d", i), 1<<20))
				}
				records = append(records, regular("one-byte-over", 1))
			}
			if _, err := contentFiles(t.Context(), contentArchive(t, records), true); !errors.Is(err, ErrArtifactLimit) {
				t.Fatal("flow widened aggregate/inventory limit", err)
			}
		})
	}
}

func TestFlowReadersPreserveCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadLayoutFlowV1Alpha1(ctx, "private-canary", ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := ReadArchiveFlowV1Alpha1(ctx, nil, 0, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
