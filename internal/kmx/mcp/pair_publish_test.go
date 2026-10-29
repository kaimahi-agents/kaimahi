//go:build linux

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"
)

func TestPairPublicationAcknowledgesPartialCreate(t *testing.T) {
	for _, window := range []string{"first-error", "second-error", "cancel-after-first"} {
		t.Run(window, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			cs := historyClient(t, f)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prior := renameNoReplace
			t.Cleanup(func() { renameNoReplace = prior })
			attempts := 0
			renameNoReplace = func(fd int, from, to string) error {
				attempts++
				if window == "first-error" || window == "second-error" && attempts == 2 {
					return unix.EIO
				}
				err := prior(fd, from, to)
				if window == "cancel-after-first" && attempts == 1 {
					cancel()
				}
				return err
			}
			// Call the same operation handler without SDK cancellation discarding the ACK.
			op := Operations(f, nil, nil)["create"].V3
			args, _ := json.Marshal(map[string]any{"path": "a.agent.md", "content": "Prompt\n", "testsContent": historyTests})
			result, err := op.Handler(ctx, &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{Arguments: args}})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			var ack pairSaveResult
			if json.Unmarshal(data, &ack) != nil || !result.IsError || ack.SnapshotRevision != "" || ack.SourceVersion != "" {
				t.Fatalf("forged complete acknowledgement: %s", data)
			}
			if window == "first-error" {
				if ack.Agent.Status != "uncertain" || ack.Tests.Status != "not_saved" || attempts != 1 {
					t.Fatalf("first effect: %s", data)
				}
			} else {
				if ack.Agent.Status != "saved" {
					t.Fatalf("lost first save: %s", data)
				}
				actual, err := os.ReadFile(filepath.Join(root, "a.agent.md"))
				if err != nil || string(actual) != "Prompt\n" {
					t.Fatal("first saved bytes lost", err)
				}
				if window == "cancel-after-first" && (ack.State != "partial" || ack.Tests.Status != "not_saved" || attempts != 1) {
					t.Fatalf("cancel acknowledgement: %s", data)
				}
				if window == "second-error" && (ack.State != "uncertain" || ack.Tests.Status != "uncertain" || attempts != 2) {
					t.Fatalf("second effect: %s", data)
				}
			}
			if _, err := os.Lstat(filepath.Join(root, "a.agent.test.md")); !os.IsNotExist(err) {
				t.Fatal("unexpected tests publication", err)
			}
			_ = cs
		})
	}
}

func TestPairPublicationForeignAbsentParentHasNoSaves(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, "", base)
	b, err := f.createPairBinding(t.Context(), filepath.Join(root, "new", "a.agent.md"), []byte("Prompt\n"), []byte(historyTests))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if err := os.Mkdir(filepath.Join(root, "new"), 0755); err != nil {
		t.Fatal(err)
	}
	ack := b.publish(t.Context(), b.pair, []byte("Prompt\n"), []byte(historyTests))
	if ack.State != "not_saved" || ack.Agent.Status != "not_saved" || ack.Tests.Status != "not_saved" {
		t.Fatalf("false unchanged effects: %+v", ack)
	}
	if _, err := os.Lstat(filepath.Join(root, "new", "a.agent.md")); !os.IsNotExist(err) {
		t.Fatal("adopted foreign directory")
	}
}
