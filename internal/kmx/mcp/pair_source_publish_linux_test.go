package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// The engine's receipt/failure coverage is reused; these cases catch the native
// wrapper discarding per-source facts or claiming a full revision on an error.
func TestPublishSourcePairPartialFacts(t *testing.T) {
	for _, fault := range []string{"cancel-after-first", "foreign-before-second", "unknown-second"} {
		t.Run(fault, func(t *testing.T) {
			root := sourceRoot(t)
			req := nativePublishRequest(t, "create", filepath.Join(root, "a.agent.md"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prior, attempts := renameNoReplace, 0
			t.Cleanup(func() { renameNoReplace = prior })
			renameNoReplace = func(fd int, from, to string) error {
				attempts++
				if attempts == 2 && fault == "unknown-second" {
					return unix.EIO
				}
				err := prior(fd, from, to)
				if err == nil && attempts == 1 {
					switch fault {
					case "cancel-after-first":
						cancel()
					case "foreign-before-second":
						sourceWrite(t, req.Tests.Path, "foreign")
					}
				}
				return err
			}
			got, err := PublishSourcePair(ctx, req)
			wantTests, wantAttempts := "not_written", 1
			if fault == "unknown-second" {
				wantTests, wantAttempts = "unknown", 2
			}
			if err == nil || got.Revision != "" || got.Agent.State != "written" || got.Agent.Revision != revision(req.AgentBytes) || got.Tests.State != wantTests || got.Tests.Revision != "" || attempts != wantAttempts {
				t.Fatalf("partial facts = %+v, %v; attempts = %d", got, err, attempts)
			}
			if fault == "foreign-before-second" {
				body, err := os.ReadFile(req.Tests.Path)
				must(t, err)
				if string(body) != "foreign" {
					t.Fatal("overwrote intervening source")
				}
			}
			wantEntries := 2 // root and written agent, no stages or local state
			if fault == "foreign-before-second" {
				wantEntries++
			}
			if len(sourceTree(t, root)) != wantEntries {
				t.Fatal("stage or local state residue", sourceTree(t, root))
			}
		})
	}
}
