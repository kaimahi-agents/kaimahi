//go:build linux || darwin

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func sourceRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func sourceWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sourceRequest(intent, agent string) SourcePairPublishRequest {
	tests, _ := agentfile.DefaultTestsPath(agent)
	return SourcePairPublishRequest{Intent: intent, AgentPath: agent, Tests: agentfile.TestsSelection{Path: tests}}
}

func sourceTree(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			names = append(names, strings.TrimPrefix(p, root)+"!unreadable")
			return fs.SkipDir
		}
		names = append(names, strings.TrimPrefix(p, root)+d.Type().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func sameNames(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

func openDescriptors() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// window runs fn once at the named acquisition stage and reports the tree it left.
func window(t *testing.T, root, stage string, fn func()) *[]string {
	t.Helper()
	var after []string
	fired := false
	sourcePairAcquireWindow = func(s string) {
		if s == stage && !fired {
			fired = true
			fn()
			after = sourceTree(t, root)
		}
	}
	t.Cleanup(func() { sourcePairAcquireWindow = nil })
	return &after
}

func wantRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func TestSourcePairAcquireEditHoldsPresentRoles(t *testing.T) {
	root := sourceRoot(t)
	agent := filepath.Join(root, "one", "a.agent.md")
	explicit := filepath.Join(root, "two", "checks.md")
	sourceWrite(t, agent, "agent-bytes")
	sourceWrite(t, explicit, "tests-bytes")
	req := sourceRequest("edit", agent)
	req.Tests = agentfile.TestsSelection{Path: explicit, Explicit: true}
	b, err := acquireSourcePair(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if b.agentPath != agent || b.testsPath != explicit || b.agent == nil || b.tests == nil || b.agentAbsent != nil || b.absent != nil {
		t.Fatalf("roles = %+v", b)
	}
	if string(b.agent.source) != "agent-bytes" || string(b.tests.source) != "tests-bytes" {
		t.Fatalf("held bytes = %q %q", b.agent.source, b.tests.source)
	}
	if err := b.check(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSourcePairAcquireEditHoldsDefaultAbsence(t *testing.T) {
	root := sourceRoot(t)
	agent := filepath.Join(root, "a.agent.md")
	sourceWrite(t, agent, "agent-bytes")
	b, err := acquireSourcePair(t.Context(), sourceRequest("edit", agent))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	tests := filepath.Join(root, "a.agent.test.md")
	if b.tests != nil || b.absent == nil || b.absent.path != tests || b.absent.name != "a.agent.test.md" || b.testsPath != tests {
		t.Fatalf("absent default = %+v", b)
	}
	// A new default companion appearing after acquisition is a stale baseline.
	sourceWrite(t, tests, "late")
	wantRefusal(t, b.check(t.Context()), "stale_revision")
}

func TestSourcePairAcquireRefusalsMutateNothingAndCloseDescriptors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string) SourcePairPublishRequest
		code  string
	}{
		{"missing agent", func(t *testing.T, r string) SourcePairPublishRequest {
			return sourceRequest("edit", filepath.Join(r, "a.agent.md"))
		}, "not_found"},
		{"explicit missing", func(t *testing.T, r string) SourcePairPublishRequest {
			a := filepath.Join(r, "a.agent.md")
			sourceWrite(t, a, "x")
			q := sourceRequest("edit", a)
			q.Tests.Explicit = true // explicit selection of the absent default path
			return q
		}, "not_found"},
		{"same inode", func(t *testing.T, r string) SourcePairPublishRequest {
			a := filepath.Join(r, "a.agent.md")
			sourceWrite(t, a, "x")
			if err := os.Link(a, filepath.Join(r, "linked.md")); err != nil {
				t.Fatal(err)
			}
			q := sourceRequest("edit", a)
			q.Tests = agentfile.TestsSelection{Path: filepath.Join(r, "linked.md"), Explicit: true}
			return q
		}, "unsafe_path"},
		{"tests leaf symlink", func(t *testing.T, r string) SourcePairPublishRequest {
			a := filepath.Join(r, "a.agent.md")
			sourceWrite(t, a, "x")
			sourceWrite(t, filepath.Join(r, "real.md"), "y")
			q := sourceRequest("edit", a)
			if err := os.Symlink(filepath.Join(r, "real.md"), q.Tests.Path); err != nil {
				t.Fatal(err)
			}
			return q
		}, "unsafe_path"},
		{"relative agent", func(*testing.T, string) SourcePairPublishRequest {
			return SourcePairPublishRequest{Intent: "edit", AgentPath: "a.agent.md", Tests: agentfile.TestsSelection{Path: "/x/a.agent.test.md"}}
		}, "unsafe_path"},
		{"traversal", func(t *testing.T, r string) SourcePairPublishRequest {
			return sourceRequest("edit", r+"/sub/../a.agent.md")
		}, "unsafe_path"},
		{"not an agent leaf", func(t *testing.T, r string) SourcePairPublishRequest {
			return SourcePairPublishRequest{Intent: "edit", AgentPath: filepath.Join(r, "a.md"), Tests: agentfile.TestsSelection{Path: filepath.Join(r, "t.md"), Explicit: true}}
		}, "unsafe_path"},
		{"empty name leaf", func(t *testing.T, r string) SourcePairPublishRequest {
			return SourcePairPublishRequest{Intent: "edit", AgentPath: filepath.Join(r, ".agent.md"), Tests: agentfile.TestsSelection{Path: filepath.Join(r, "t.md"), Explicit: true}}
		}, "unsafe_path"},
		{"empty tests path", func(t *testing.T, r string) SourcePairPublishRequest {
			return SourcePairPublishRequest{Intent: "edit", AgentPath: filepath.Join(r, "a.agent.md")}
		}, "unsafe_path"},
		{"relative tests path", func(t *testing.T, r string) SourcePairPublishRequest {
			return SourcePairPublishRequest{Intent: "edit", AgentPath: filepath.Join(r, "a.agent.md"), Tests: agentfile.TestsSelection{Path: "t.md", Explicit: true}}
		}, "unsafe_path"},
		{"same path", func(t *testing.T, r string) SourcePairPublishRequest {
			a := filepath.Join(r, "a.agent.md")
			return SourcePairPublishRequest{Intent: "edit", AgentPath: a, Tests: agentfile.TestsSelection{Path: a, Explicit: true}}
		}, "unsafe_path"},
		{"unknown intent", func(t *testing.T, r string) SourcePairPublishRequest {
			return sourceRequest("replace", filepath.Join(r, "a.agent.md"))
		}, "invalid_options"},
		{"create explicit tests", func(t *testing.T, r string) SourcePairPublishRequest {
			q := sourceRequest("create", filepath.Join(r, "a.agent.md"))
			q.Tests.Explicit = true
			return q
		}, "invalid_options"},
		{"create existing agent", func(t *testing.T, r string) SourcePairPublishRequest {
			a := filepath.Join(r, "a.agent.md")
			sourceWrite(t, a, "x")
			return sourceRequest("create", a)
		}, "already_exists"},
		{"create existing tests", func(t *testing.T, r string) SourcePairPublishRequest {
			a := filepath.Join(r, "a.agent.md")
			q := sourceRequest("create", a)
			sourceWrite(t, q.Tests.Path, "x")
			return q
		}, "already_exists"},
		{"create below symlink", func(t *testing.T, r string) SourcePairPublishRequest {
			sourceWrite(t, filepath.Join(r, "real", "keep"), "x")
			if err := os.Symlink(filepath.Join(r, "real"), filepath.Join(r, "link")); err != nil {
				t.Fatal(err)
			}
			return sourceRequest("create", filepath.Join(r, "link", "a.agent.md"))
		}, "unsafe_path"},
		{"create below symlink and absent dirs", func(t *testing.T, r string) SourcePairPublishRequest {
			if err := os.Mkdir(filepath.Join(r, "real"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(r, "real"), filepath.Join(r, "link")); err != nil {
				t.Fatal(err)
			}
			return sourceRequest("create", filepath.Join(r, "link", "new", "deep", "a.agent.md"))
		}, "unsafe_path"},
		{"create below file", func(t *testing.T, r string) SourcePairPublishRequest {
			sourceWrite(t, filepath.Join(r, "file"), "x")
			return sourceRequest("create", filepath.Join(r, "file", "a.agent.md"))
		}, "unsafe_path"},
		{"create below inaccessible ancestor", func(t *testing.T, r string) SourcePairPublishRequest {
			if os.Geteuid() == 0 {
				t.Skip("root bypasses directory permissions")
			}
			locked := filepath.Join(r, "locked")
			if err := os.MkdirAll(filepath.Join(locked, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(locked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(locked, 0o755) })
			return sourceRequest("create", filepath.Join(locked, "sub", "a.agent.md"))
		}, "unsafe_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := sourceRoot(t)
			req := tc.setup(t, root)
			before, fds := sourceTree(t, root), openDescriptors()
			b, err := acquireSourcePair(t.Context(), req)
			if b != nil {
				b.close()
			}
			wantRefusal(t, err, tc.code)
			if b != nil || !sameNames(before, sourceTree(t, root)) || openDescriptors() != fds {
				t.Fatalf("refusal changed state: binding=%v tree=%v fds %d->%d", b, sourceTree(t, root), fds, openDescriptors())
			}
		})
	}
}

func TestSourcePairAcquireCreateHoldsAbsenceWithoutMaterializing(t *testing.T) {
	root := sourceRoot(t)
	agent := filepath.Join(root, ".github", "agents", "a.agent.md")
	before := sourceTree(t, root)
	b, err := acquireSourcePair(t.Context(), sourceRequest("create", agent))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if b.agentAbsent == nil || b.absent == nil || b.agent != nil || b.tests != nil || b.absent.path != filepath.Join(root, ".github", "agent-tests", "a.agent.test.md") {
		t.Fatalf("create roles = %+v", b)
	}
	if b.agentAbsent.name != ".github" || b.agentAbsent.parent.Name() != root {
		t.Fatalf("absence anchored at %q probing %q", b.agentAbsent.parent.Name(), b.agentAbsent.name)
	}
	if !sameNames(before, sourceTree(t, root)) {
		t.Fatalf("acquisition materialized %v", sourceTree(t, root))
	}
	if err := b.check(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSourcePairAcquireCancellationClosesDescriptors(t *testing.T) {
	root := sourceRoot(t)
	agent := filepath.Join(root, "a.agent.md")
	sourceWrite(t, agent, "x")
	sourceWrite(t, filepath.Join(root, "a.agent.test.md"), "y")
	fds := openDescriptors()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	b, err := acquireSourcePair(ctx, sourceRequest("edit", agent))
	wantRefusal(t, err, "cancelled")
	if b != nil || openDescriptors() != fds {
		t.Fatalf("initial cancellation leaked: binding=%v fds %d->%d", b, fds, openDescriptors())
	}
	ctx, cancel = context.WithCancel(t.Context())
	window(t, root, "after_hold", cancel)
	b, err = acquireSourcePair(ctx, sourceRequest("edit", agent))
	wantRefusal(t, err, "cancelled")
	if b != nil || openDescriptors() != fds {
		t.Fatalf("cancelled acquisition leaked: binding=%v fds %d->%d", b, fds, openDescriptors())
	}
}

// Every swap keeps the immediate parent's inode reachable by name, so only the
// retained higher-ancestor identities can refuse it.
func TestSourcePairAcquireRefusesHigherAncestorSwaps(t *testing.T) {
	swaps := []struct {
		name, code string
		swap       func(t *testing.T, root string)
	}{
		{"symlink resolving back to the same parent", "unsafe_path", func(t *testing.T, root string) {
			must(t, os.Rename(filepath.Join(root, "a"), filepath.Join(root, "moved")))
			must(t, os.Symlink(filepath.Join(root, "moved"), filepath.Join(root, "a")))
		}},
		{"replaced higher directory", "stale_revision", func(t *testing.T, root string) {
			must(t, os.Mkdir(filepath.Join(root, "a2"), 0o755))
			must(t, os.Rename(filepath.Join(root, "a", "b"), filepath.Join(root, "a2", "b")))
			must(t, os.Rename(filepath.Join(root, "a"), filepath.Join(root, "old")))
			must(t, os.Rename(filepath.Join(root, "a2"), filepath.Join(root, "a")))
		}},
		{"replaced parent", "stale_revision", func(t *testing.T, root string) {
			must(t, os.Rename(filepath.Join(root, "a", "b"), filepath.Join(root, "a", "old")))
			must(t, os.Mkdir(filepath.Join(root, "a", "b"), 0o755))
		}},
	}
	for _, intent := range []string{"edit", "create"} {
		for _, stage := range []string{"before_open", "after_hold"} {
			for _, s := range swaps {
				t.Run(intent+"/"+stage+"/"+s.name, func(t *testing.T) {
					root := sourceRoot(t)
					agent := filepath.Join(root, "a", "b", "c.agent.md")
					if intent == "edit" {
						sourceWrite(t, agent, "x")
					} else {
						must(t, os.MkdirAll(filepath.Dir(agent), 0o755))
					}
					after := window(t, root, stage, func() { s.swap(t, root) })
					fds := openDescriptors()
					b, err := acquireSourcePair(t.Context(), sourceRequest(intent, agent))
					if b != nil {
						b.close()
					}
					wantRefusal(t, err, s.code)
					if b != nil || !sameNames(*after, sourceTree(t, root)) || openDescriptors() != fds {
						t.Fatalf("swap leaked: binding=%v tree %v -> %v fds %d->%d", b, *after, sourceTree(t, root), fds, openDescriptors())
					}
				})
			}
		}
	}
}

func TestSourcePairAcquireRefusesAbsentPathBecomingOccupied(t *testing.T) {
	root := sourceRoot(t)
	agent := filepath.Join(root, "a.agent.md")
	window(t, root, "before_open", func() { sourceWrite(t, agent, "raced") })
	b, err := acquireSourcePair(t.Context(), sourceRequest("create", agent))
	if b != nil {
		b.close()
	}
	wantRefusal(t, err, "already_exists")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSourcePairAcquireCheckStrictAndPublishingTimes(t *testing.T) {
	root := sourceRoot(t)
	agent := filepath.Join(root, "a", "b", "c.agent.md")
	sourceWrite(t, agent, "x")
	b, err := acquireSourcePair(t.Context(), sourceRequest("edit", agent))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	// Own staging changes only directory times: strict check refuses them while the
	// relaxed publishing check keeps identity, bytes and absence checks.
	must(t, os.Chtimes(filepath.Dir(agent), time.Unix(1, 0), time.Unix(1, 0)))
	wantRefusal(t, b.check(t.Context()), "stale_revision")
	if err := b.checkPublishing(t.Context()); err != nil {
		t.Fatalf("own directory time refused: %v", err)
	}
	must(t, os.WriteFile(agent, []byte("y"), 0o600))
	wantRefusal(t, b.checkPublishing(t.Context()), "stale_revision")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	wantRefusal(t, b.checkPublishing(cancelled), "cancelled")
}

func TestSourcePairAcquireCheckRefusesChangedAncestor(t *testing.T) {
	root := sourceRoot(t)
	agent := filepath.Join(root, "a", "b", "c.agent.md")
	sourceWrite(t, agent, "x")
	b, err := acquireSourcePair(t.Context(), sourceRequest("edit", agent))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	must(t, os.Rename(filepath.Join(root, "a"), filepath.Join(root, "moved")))
	must(t, os.Symlink(filepath.Join(root, "moved"), filepath.Join(root, "a")))
	wantRefusal(t, b.check(t.Context()), "unsafe_path")
	wantRefusal(t, b.checkPublishing(t.Context()), "unsafe_path")
}

func TestSourcePairAcquirePrepareCreatesOnlyOwnDirectories(t *testing.T) {
	root := sourceRoot(t)
	agent := filepath.Join(root, ".github", "agents", "a.agent.md")
	b, err := acquireSourcePair(t.Context(), sourceRequest("create", agent))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	created := map[string]os.FileInfo{}
	must(t, b.prepareAbsentParent(b.agentAbsent, created))
	// The first role's mkdirs changed directory times and made .github visible, but
	// that directory is in our created map, so the second role may traverse it.
	must(t, b.prepareAbsentParent(b.absent, created))
	for _, dir := range []string{"agents", "agent-tests"} {
		if info, err := os.Lstat(filepath.Join(root, ".github", dir)); err != nil || !info.IsDir() {
			t.Fatalf("%s not prepared: %v", dir, err)
		}
	}
	if b.agentAbsent.parent.Name() != filepath.Join(root, ".github", "agents") || b.absent.name != "a.agent.test.md" {
		t.Fatalf("prepared slots anchored at %q, %q", b.agentAbsent.parent.Name(), b.absent.name)
	}
	if err := b.checkPublishing(t.Context()); err != nil {
		t.Fatalf("own created directories refused: %v", err)
	}
}

func TestSourcePairAcquirePrepareRefusesForeignDirectoriesBeforeMkdir(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap func(t *testing.T, root string)
	}{
		{"foreign appears before first mkdir", func(t *testing.T, root string) { must(t, os.Mkdir(filepath.Join(root, ".github"), 0o755)) }},
		{"first role's own directory replaced", func(t *testing.T, root string) {
			must(t, os.Rename(filepath.Join(root, ".github", "agents"), filepath.Join(root, ".github", "old")))
			must(t, os.Mkdir(filepath.Join(root, ".github", "agents"), 0o755))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := sourceRoot(t)
			b, err := acquireSourcePair(t.Context(), sourceRequest("create", filepath.Join(root, ".github", "agents", "a.agent.md")))
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			created := map[string]os.FileInfo{}
			if tc.name != "foreign appears before first mkdir" {
				must(t, b.prepareAbsentParent(b.agentAbsent, created))
			}
			tc.swap(t, root)
			before := sourceTree(t, root)
			target := b.agentAbsent
			if len(created) > 0 {
				target = b.absent
			}
			wantRefusal(t, b.prepareAbsentParent(target, created), "stale_revision")
			if !sameNames(before, sourceTree(t, root)) {
				t.Fatalf("refused preparation created %v", sourceTree(t, root))
			}
		})
	}
}

const nativePublishSource = "---\nname: reader\nmodel: model/v1\nkmx:\n  schemaVersion: 1\n  runtime: orka\n  tools: []\n  skills: []\n---\nKeep whitespace.  \n"

func nativePublishValidate(tuple SourcePairTuple) error {
	_, err := agentfile.ComposeOrkaSourcePair(tuple.AgentPath, tuple.AgentBytes, tuple.Tests, tuple.TestsBytes, tuple.TestsPresent)
	return err
}

func nativePublishRequest(t *testing.T, intent, path string) SourcePairPublishRequest {
	t.Helper()
	req := sourceRequest(intent, path)
	req.AgentBytes, req.TestsBytes, req.TestsPresent = []byte(nativePublishSource), []byte(historyTests), true
	req.Validate = nativePublishValidate
	req.Preflight = func(SourcePairTuple, SourcePairPublishResult) error { return nil }
	if intent == "edit" {
		baseline, err := agentfile.LoadOrkaPair(path, "")
		must(t, err)
		req.ExpectedRevision = baseline.SnapshotRevision
		req.TestsPresent = baseline.Tests.State == "external"
		if !req.TestsPresent {
			req.TestsBytes = nil
		}
		req.Current = func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			now, err := agentfile.LoadOrkaPair(path, "")
			if err != nil {
				return err
			}
			if !baseline.SameSourceVersion(now) {
				return refusal("stale_revision")
			}
			return nil
		}
	}
	return req
}

// Catches fabricated identities, skipped preflight and Current rerun after our
// own staging. The validator uses the actual native composer.
func TestPublishSourcePairNativeBinding(t *testing.T) {
	root := sourceRoot(t)
	req := nativePublishRequest(t, "create", filepath.Join(root, ".github", "agents", "a.agent.md"))
	req.Tests.Key = "forged"
	before := sourceTree(t, root)
	validated := false
	req.Validate = func(tuple SourcePairTuple) error { validated = true; return nativePublishValidate(tuple) }
	var prospective SourcePairPublishResult
	req.Preflight = func(tuple SourcePairTuple, ack SourcePairPublishResult) error {
		if !validated || !sameNames(before, sourceTree(t, root)) || tuple.Tests.Key == "forged" || tuple.AgentPath != req.AgentPath || tuple.Tests.Path != req.Tests.Path {
			t.Fatal("preflight not bound before parent/stage effects", tuple)
		}
		prospective = ack
		return nil
	}
	got, err := PublishSourcePair(t.Context(), req)
	if err != nil || got.Agent.State != "written" || got.Tests.State != "written" || got.Revision == "" || got != prospective {
		t.Fatalf("create = %+v, %v; prospective = %+v", got, err, prospective)
	}
	loaded, err := agentfile.LoadOrkaPair(req.AgentPath, "")
	must(t, err)
	if got.Revision != loaded.SnapshotRevision || got.Agent.Revision != loaded.Agent.Revision || got.Tests.Revision != loaded.Tests.Revision || !bytes.Equal(loaded.Agent.Source, req.AgentBytes) || !bytes.Equal(loaded.Tests.Suite.Source, req.TestsBytes) {
		t.Fatal("ack did not bind exact native sources", got)
	}
	req = nativePublishRequest(t, "edit", req.AgentPath)
	req.AgentBytes = append(req.AgentBytes, []byte("More instructions.\n")...)
	got, err = PublishSourcePair(t.Context(), req)
	if err != nil || got.Agent.State != "written" || got.Tests.State != "unchanged" || got.Revision == "" {
		t.Fatalf("checked edit = %+v, %v", got, err)
	}
}

// Catches permission inferred from candidate presence or a caller's selection key.
func TestPublishSourcePairPresenceAndIntent(t *testing.T) {
	for _, tc := range []struct {
		name, intent, code string
		agent, tests       bool
		change             func(*SourcePairPublishRequest)
	}{
		{"create lacks tests", "create", "invalid_options", false, false, func(q *SourcePairPublishRequest) { q.TestsPresent, q.TestsBytes = false, nil }},
		{"create expected revision", "create", "invalid_options", false, false, func(q *SourcePairPublishRequest) { q.ExpectedRevision = "stale" }},
		{"create sparse permission", "create", "invalid_options", false, false, func(q *SourcePairPublishRequest) { q.CreateDefaultTests = true }},
		{"create occupied agent", "create", "already_exists", true, false, nil},
		{"create occupied tests", "create", "already_exists", false, true, nil},
		{"edit missing revision", "edit", "invalid_options", true, true, func(q *SourcePairPublishRequest) { q.ExpectedRevision = "" }},
		{"edit keeps absent tests", "edit", "", true, false, nil},
		{"raw replacement cannot create tests", "edit", "invalid_options", true, false, func(q *SourcePairPublishRequest) { q.TestsPresent, q.TestsBytes = true, []byte(historyTests) }},
		{"sparse first default tests", "edit", "", true, false, func(q *SourcePairPublishRequest) {
			q.TestsPresent, q.TestsBytes, q.CreateDefaultTests = true, []byte(historyTests), true
		}},
		{"explicit missing default", "edit", "not_found", true, false, func(q *SourcePairPublishRequest) { q.Tests.Explicit = true }},
		{"explicit missing with permission", "edit", "invalid_options", true, false, func(q *SourcePairPublishRequest) {
			q.Tests.Explicit, q.TestsPresent, q.CreateDefaultTests = true, true, true
			q.TestsBytes = []byte(historyTests)
		}},
		{"implicit nondefault", "edit", "invalid_options", true, false, func(q *SourcePairPublishRequest) { q.Tests.Path += ".other" }},
		{"edit never deletes tests", "edit", "invalid_options", true, true, func(q *SourcePairPublishRequest) { q.TestsPresent, q.TestsBytes = false, nil }},
		{"absent with bytes", "edit", "invalid_options", true, false, func(q *SourcePairPublishRequest) { q.TestsBytes = []byte(historyTests) }},
		{"permission without creation", "edit", "invalid_options", true, false, func(q *SourcePairPublishRequest) { q.CreateDefaultTests = true }},
		{"permission on present tests", "edit", "invalid_options", true, true, func(q *SourcePairPublishRequest) { q.CreateDefaultTests = true }},
		{"explicit present default", "edit", "", true, true, func(q *SourcePairPublishRequest) { q.Tests.Explicit = true }},
		{"agent cannot become absent", "edit", "native", true, true, func(q *SourcePairPublishRequest) { q.AgentBytes = nil }},
		{"nil validator", "create", "invalid_options", false, false, func(q *SourcePairPublishRequest) { q.Validate = nil }},
		{"nil preflight", "create", "invalid_options", false, false, func(q *SourcePairPublishRequest) { q.Preflight = nil }},
		{"nil current", "edit", "invalid_options", true, true, func(q *SourcePairPublishRequest) { q.Current = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := sourceRoot(t)
			path := filepath.Join(root, "a.agent.md")
			if tc.agent {
				sourceWrite(t, path, nativePublishSource)
			}
			if tc.tests {
				sourceWrite(t, filepath.Join(root, "a.agent.test.md"), historyTests)
			}
			req := nativePublishRequest(t, tc.intent, path)
			if tc.change != nil {
				tc.change(&req)
			}
			before := sourceTree(t, root)
			got, err := PublishSourcePair(t.Context(), req)
			if tc.code == "" {
				if err != nil || got.Revision == "" {
					t.Fatalf("result = %+v, %v", got, err)
				}
				loaded, err := agentfile.LoadOrkaPair(path, "")
				must(t, err)
				if got.Revision != loaded.SnapshotRevision || (loaded.Tests.State == "external") != req.TestsPresent || !bytes.Equal(loaded.Agent.Source, req.AgentBytes) {
					t.Fatal("presence or complete revision does not match publication", got)
				}
				if req.TestsPresent && !bytes.Equal(loaded.Tests.Suite.Source, req.TestsBytes) {
					t.Fatal("authorized first tests not published")
				}
				return
			}
			if tc.code == "native" {
				var diagnostic *agentfile.Error
				if !errors.As(err, &diagnostic) {
					t.Fatalf("native validation = %v", err)
				}
			} else {
				wantRefusal(t, err, tc.code)
			}
			if got.Revision != "" || !sameNames(before, sourceTree(t, root)) {
				t.Fatal("refusal mutated tree or returned complete revision", got)
			}
			for path, body := range map[string]string{path: nativePublishSource, filepath.Join(root, "a.agent.test.md"): historyTests} {
				if raw, err := os.ReadFile(path); err == nil && string(raw) != body {
					t.Fatal("occupied half replaced", path)
				}
			}
		})
	}
}

// Invalid baseline policy must veto a valid candidate; stale revisions must be
// compared only after full baseline validation, before candidate validation.
func TestPublishSourcePairPrepareOrder(t *testing.T) {
	for _, mode := range []string{"baseline-veto", "invalid-native-baseline", "stale-revision", "invalid-candidate"} {
		t.Run(mode, func(t *testing.T) {
			root := sourceRoot(t)
			path := filepath.Join(root, "a.agent.md")
			sourceWrite(t, path, nativePublishSource)
			sourceWrite(t, filepath.Join(root, "a.agent.test.md"), historyTests)
			req := nativePublishRequest(t, "edit", path)
			req.AgentBytes = append(req.AgentBytes, []byte("Candidate.\n")...)
			req.TestsBytes = append(req.TestsBytes, []byte("Candidate prose.\n")...)
			if mode == "invalid-native-baseline" {
				sourceWrite(t, req.Tests.Path, "not a tests source")
			}
			if mode == "stale-revision" {
				req.ExpectedRevision = "sha256:stale"
			}
			if mode == "invalid-candidate" {
				req.AgentBytes = []byte("not a native source")
			}
			veto := errors.New("baseline is not valid for this adapter")
			var events []string
			current := req.Current
			req.Current = func(ctx context.Context) error { events = append(events, "current"); return current(ctx) }
			req.Validate = func(tuple SourcePairTuple) error {
				events = append(events, "validate")
				if err := nativePublishValidate(tuple); err != nil {
					return err
				}
				if mode == "baseline-veto" && string(tuple.TestsBytes) == historyTests {
					return veto
				}
				return nil
			}
			req.Preflight = func(SourcePairTuple, SourcePairPublishResult) error {
				t.Fatal("preflight ran after refusal")
				return nil
			}
			got, err := PublishSourcePair(t.Context(), req)
			wantEvents := "current,validate"
			if mode == "invalid-native-baseline" {
				wantEvents = "current" // the real checked loader already refuses invalid native content
			}
			if mode == "invalid-candidate" {
				wantEvents += ",validate"
			}
			if err == nil || got.Revision != "" || strings.Join(events, ",") != wantEvents {
				t.Fatalf("events = %v, result = %+v, %v", events, got, err)
			}
			if mode == "baseline-veto" && !errors.Is(err, veto) {
				t.Fatal("callback error changed", err)
			}
			if mode == "stale-revision" {
				wantRefusal(t, err, "stale_revision")
			}
			raw, readErr := os.ReadFile(path)
			must(t, readErr)
			if string(raw) != nativePublishSource || len(sourceTree(t, root)) != 3 {
				t.Fatal("validation wrote or staged sources")
			}
			if mode == "invalid-native-baseline" {
				var diagnostic *agentfile.Error
				if !errors.As(err, &diagnostic) {
					t.Fatal("invalid native baseline not diagnosed", err)
				}
			}
		})
	}
}

// Each callback gets an independent byte copy. Mutating inbound/visible bytes
// cannot alter held baselines, later callbacks, publication bytes or no-op facts.
func TestPublishSourcePairOwnsCallbackBytesAndNoOp(t *testing.T) {
	root := sourceRoot(t)
	path := filepath.Join(root, "a.agent.md")
	sourceWrite(t, path, nativePublishSource)
	sourceWrite(t, filepath.Join(root, "a.agent.test.md"), historyTests)
	req := nativePublishRequest(t, "edit", path)
	before, err := os.Stat(path)
	must(t, err)
	current, events := req.Current, []string{}
	req.Current = func(ctx context.Context) error {
		events = append(events, "current")
		req.AgentBytes[0], req.TestsBytes[0] = 'X', 'X'
		return current(ctx)
	}
	check := func(tuple SourcePairTuple) {
		t.Helper()
		if string(tuple.AgentBytes) != nativePublishSource || string(tuple.TestsBytes) != historyTests {
			t.Fatal("callback bytes not isolated")
		}
		pair, err := agentfile.ComposeOrkaSourcePair(tuple.AgentPath, tuple.AgentBytes, tuple.Tests, tuple.TestsBytes, tuple.TestsPresent)
		must(t, err)
		if tuple.Revision != pair.SnapshotRevision || tuple.AgentRevision != pair.Agent.Revision || tuple.TestsRevision != pair.Tests.Revision || tuple.Tests.Key != pair.Tests.Selection.Key {
			t.Fatal("tuple identities not recomputed", tuple)
		}
		tuple.AgentBytes[0], tuple.TestsBytes[0] = 'Y', 'Y'
	}
	req.Validate = func(tuple SourcePairTuple) error { events = append(events, "validate"); check(tuple); return nil }
	req.Preflight = func(tuple SourcePairTuple, ack SourcePairPublishResult) error {
		events = append(events, "preflight")
		check(tuple)
		if ack.Revision != req.ExpectedRevision || ack.Agent.State != "unchanged" || ack.Tests.State != "unchanged" {
			t.Fatal("incorrect prospective no-op", ack)
		}
		return nil
	}
	got, err := PublishSourcePair(t.Context(), req)
	if err != nil || got.Revision != req.ExpectedRevision || got.Agent.State != "unchanged" || got.Tests.State != "unchanged" || strings.Join(events, ",") != "current,validate,validate,preflight" {
		t.Fatalf("no-op = %+v, %v; events = %v", got, err, events)
	}
	after, err := os.Stat(path)
	must(t, err)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("no-op rewrote agent")
	}
}

// nativeBudgetRecord is a test-only future-shaped ACK, NOT a public wire
// contract or the CLI encoder. Embed the real native projections, overriding
// Source so raw UTF-8 is a JSON string (never []byte/base64). Role instructions
// and ordered tests therefore occupy bytes in both raw and typed form.
func nativeBudgetRecord(pair *agentfile.OrkaSourcePair, ack SourcePairPublishResult, intent string) ([]byte, error) {
	record := struct {
		app.MachineResult
		AgentPath      string
		TestsSelection agentfile.TestsSelection
		TestsState     string
		TestsPresent   bool
		Agent          struct {
			*agentfile.OrkaSource
			Source string
		}
		Tests struct {
			*agentfile.Tests
			Source string
		}
		Writes SourcePairPublishResult
	}{MachineResult: app.MachineResult{
		MachineEnvelope: app.MachineEnvelope{SchemaVersion: 1, Kind: "kmx.result", Operation: "file." + intent,
			OperationID: uuid.NewString(), Seq: ^uint64(0), ElapsedMs: 1<<63 - 1},
		Outcome: "complete", Verdict: "not_applicable", ExitCode: 0},
		AgentPath: pair.AgentPath, TestsSelection: pair.Tests.Selection,
		TestsState: pair.Tests.State, TestsPresent: pair.Tests.Suite != nil, Writes: ack}
	record.Agent.OrkaSource, record.Agent.Source = pair.Agent, string(pair.Agent.Source)
	record.Tests.Tests = pair.Tests.Suite
	if pair.Tests.Suite != nil {
		record.Tests.Source = string(pair.Tests.Suite.Source)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// Catches raw-only or inbound-edit budgets, lost native projections/ACK fields,
// newline off-by-one, and preflight moved after parent/stage/source effects.
func TestPublishSourcePairPreflightBudget(t *testing.T) {
	for _, mode := range []string{"create", "edit", "no-op"} {
		for _, size := range []int{65536, 65537} {
			t.Run(fmt.Sprintf("%s/%d", mode, size), func(t *testing.T) {
				root := sourceRoot(t)
				path := filepath.Join(root, strings.Repeat("long-", 20), strings.Repeat("path-", 20), ".github", "agents", "a.agent.md")
				intent, agentState, testsState := "create", "written", "written"
				if mode != "create" {
					intent, testsState = "edit", "unchanged"
				}
				if mode == "no-op" {
					agentState = "unchanged"
				}
				req := nativePublishRequest(t, "create", path) // bind checked Edit only after seeding below
				role := "Read \"quoted\" C:\\native\\source — ✓.\n"
				input := `Ada "quoted" C:\native\tests — ✓` + strings.Repeat("t", 4000)
				req.AgentBytes = []byte(nativePublishSource + role)
				req.TestsBytes = []byte(strings.Replace(historyTests, "input: Ada", "input: '"+input+"'", 1))
				compose := func() *agentfile.OrkaSourcePair {
					pair, err := agentfile.ComposeOrkaSourcePair(path, req.AgentBytes, req.Tests, req.TestsBytes, true)
					must(t, err)
					return pair
				}
				pair := compose()
				prospective := SourcePairPublishResult{SourcePairWrite{agentState, pair.Agent.Revision}, SourcePairWrite{testsState, pair.Tests.Revision}, pair.SnapshotRevision}
				raw, err := nativeBudgetRecord(pair, prospective, intent)
				must(t, err)
				// Role padding is duplicated; prose outside test fences is raw-only,
				// so one extra prose byte gives the adjacent odd boundary exactly.
				padding := size - len(raw)
				if padding < 0 {
					t.Fatal("base fixture already exceeds boundary")
				}
				req.AgentBytes = append(req.AgentBytes, strings.Repeat("r", padding/2)...)
				req.TestsBytes = append(req.TestsBytes, strings.Repeat("p", padding%2)...)
				pair = compose()
				if mode != "create" {
					baseline := bytes.Clone(req.AgentBytes)
					if mode == "edit" {
						baseline[len(baseline)-1] = 's' // the entire inbound change is one byte
					}
					sourceWrite(t, path, string(baseline))
					sourceWrite(t, req.Tests.Path, string(req.TestsBytes))
					checked := nativePublishRequest(t, "edit", path)
					checked.AgentBytes, checked.TestsBytes = req.AgentBytes, req.TestsBytes
					req = checked
				}
				before := sourceTree(t, root)
				infos, originals := map[string]os.FileInfo{}, map[string][]byte{}
				for _, p := range []string{root, filepath.Dir(path), filepath.Dir(req.Tests.Path), path, req.Tests.Path} {
					if info, err := os.Stat(p); err == nil {
						infos[p] = info
						if !info.IsDir() {
							originals[p], err = os.ReadFile(p)
							must(t, err)
						}
					} else if !errors.Is(err, fs.ErrNotExist) {
						t.Fatal(err)
					}
				}
				veto, calls := errors.New("complete acknowledgment exceeds 64 KiB"), 0
				req.Preflight = func(tuple SourcePairTuple, ack SourcePairPublishResult) error {
					calls++
					candidate, err := agentfile.ComposeOrkaSourcePair(tuple.AgentPath, tuple.AgentBytes, tuple.Tests, tuple.TestsBytes, tuple.TestsPresent)
					must(t, err)
					prospective = ack
					raw, err := nativeBudgetRecord(candidate, ack, intent)
					must(t, err)
					var decoded struct {
						SchemaVersion                                  int
						Kind, Operation, OperationID, Outcome, Verdict string
						Seq, ElapsedMs                                 json.Number
						ExitCode                                       *int
						AgentPath                                      string
						TestsSelection                                 agentfile.TestsSelection
						TestsState                                     string
						TestsPresent                                   bool
						Agent                                          struct{ Source, Instructions, Revision string }
						Tests                                          struct {
							Source, Revision string
							Examples         []agentfile.Example
						}
						Writes SourcePairPublishResult
					}
					must(t, json.Unmarshal(raw, &decoded))
					if len(raw) != size || raw[len(raw)-1] != '\n' || bytes.Count(raw, []byte{'\n'}) != 1 ||
						decoded.SchemaVersion != 1 || decoded.Kind != "kmx.result" || decoded.Operation != "file."+intent ||
						len(decoded.OperationID) != 36 || uuid.Validate(decoded.OperationID) != nil ||
						decoded.Seq != "18446744073709551615" || decoded.ElapsedMs != "9223372036854775807" ||
						decoded.Outcome != "complete" || decoded.Verdict != "not_applicable" || decoded.ExitCode == nil || *decoded.ExitCode != 0 ||
						decoded.AgentPath != path || len(path) < 250 || decoded.TestsSelection != candidate.Tests.Selection || decoded.TestsState != "external" || !decoded.TestsPresent ||
						decoded.Agent.Source != string(req.AgentBytes) || decoded.Agent.Instructions != "Keep whitespace.  \n"+role+strings.Repeat("r", padding/2) ||
						decoded.Tests.Source != string(req.TestsBytes) || len(decoded.Tests.Examples) != 1 || decoded.Tests.Examples[0].Input != input ||
						decoded.Agent.Revision != pair.Agent.Revision || decoded.Tests.Revision != pair.Tests.Revision || decoded.Writes != ack ||
						ack.Agent != (SourcePairWrite{agentState, pair.Agent.Revision}) || ack.Tests != (SourcePairWrite{testsState, pair.Tests.Revision}) || ack.Revision != pair.SnapshotRevision {
						t.Fatalf("incomplete native ACK fixture: bytes=%d, writes=%+v", len(raw), ack)
					}
					if !bytes.Contains(raw, []byte(`\"quoted\"`)) || !bytes.Contains(raw, []byte(`C:\\native\\source`)) || !bytes.Contains(raw, []byte(`C:\\native\\tests`)) || !bytes.Contains(raw, []byte("— ✓")) {
						t.Fatal("fixture did not exercise JSON escaping and raw UTF-8")
					}
					if len(raw) > 65536 {
						return veto
					}
					return nil
				}
				got, err := PublishSourcePair(t.Context(), req)
				if calls != 1 {
					t.Fatalf("preflight calls = %d", calls)
				}
				if size == 65537 {
					facts := SourcePairPublishResult{Agent: SourcePairWrite{State: "not_written"}, Tests: SourcePairWrite{State: "not_written"}}
					if mode != "create" {
						facts.Tests = SourcePairWrite{"unchanged", pair.Tests.Revision}
					}
					if mode == "no-op" {
						facts.Agent = SourcePairWrite{"unchanged", pair.Agent.Revision}
					}
					if !errors.Is(err, veto) || got != facts {
						t.Fatalf("over-budget = %+v, %v; want intact veto and %+v", got, err, facts)
					}
				} else {
					if err != nil || got != prospective {
						t.Fatalf("at-budget = %+v, %v; want %+v", got, err, prospective)
					}
					loaded, err := agentfile.LoadOrkaPair(path, "")
					must(t, err)
					if loaded.SnapshotRevision != got.Revision || !bytes.Equal(loaded.Agent.Source, req.AgentBytes) || !bytes.Equal(loaded.Tests.Suite.Source, req.TestsBytes) {
						t.Fatal("allowed record did not publish its exact sources")
					}
				}
				if size == 65537 || mode == "no-op" {
					if !sameNames(before, sourceTree(t, root)) {
						t.Fatal("refusal/no-op left new parents, stages or sources")
					}
					for p, info := range infos {
						after, err := os.Stat(p)
						must(t, err)
						if !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
							t.Fatal("refusal/no-op created a stage or renamed a source", p)
						}
					}
					for p, original := range originals {
						raw, err := os.ReadFile(p)
						must(t, err)
						if !bytes.Equal(raw, original) {
							t.Fatal("refusal/no-op changed original bytes", p)
						}
					}
				}
			})
		}
	}
}

// Equal bytes do not prove checked currentness. Test-only swaps in callbacks
// also pin the held-name check after Current and core's second check after Prepare.
func TestPublishSourcePairCurrentnessBeforeStaging(t *testing.T) {
	for _, mode := range []string{"before-acquire", "in-current", "in-preflight"} {
		t.Run(mode, func(t *testing.T) {
			root := sourceRoot(t)
			path := filepath.Join(root, "a.agent.md")
			sourceWrite(t, path, nativePublishSource)
			foreign := filepath.Join(root, "foreign")
			sourceWrite(t, foreign, nativePublishSource)
			req := nativePublishRequest(t, "edit", path)
			req.AgentBytes = append(req.AgentBytes, []byte("Candidate.\n")...)
			swap := func() { must(t, os.Rename(foreign, path)) }
			current := req.Current
			if mode == "before-acquire" {
				swap()
			}
			if mode == "in-current" {
				req.Current = func(ctx context.Context) error { err := current(ctx); swap(); return err }
			}
			if mode == "in-preflight" {
				req.Preflight = func(SourcePairTuple, SourcePairPublishResult) error { swap(); return nil }
			}
			got, err := PublishSourcePair(t.Context(), req)
			wantRefusal(t, err, "stale_revision")
			raw, readErr := os.ReadFile(path)
			must(t, readErr)
			if got.Revision != "" || got.Agent.State != "not_written" || string(raw) != nativePublishSource || len(sourceTree(t, root)) != 2 {
				t.Fatal("stale publication changed sources or left stages", got)
			}
		})
	}
}

func TestPublishSourcePairCancelled(t *testing.T) {
	root := sourceRoot(t)
	req := nativePublishRequest(t, "create", filepath.Join(root, "new", "a.agent.md"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := PublishSourcePair(ctx, req)
	wantRefusal(t, err, "cancelled")
	if got.Revision != "" || len(sourceTree(t, root)) != 1 {
		t.Fatal("cancelled call made effects", got)
	}
}

func TestLegacyPairBindingInitializesFixedPhysicalRoles(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, "", base)
	agent := filepath.Join(root, "a.agent.md")
	tests := filepath.Join(root, "checks.md")
	sourceWrite(t, agent, "Prompt\n")
	sourceWrite(t, tests, historyTests)
	bound, err := f.bindPair(t.Context(), agent, tests)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.close()
	if bound.agentPath != agent || bound.testsPath != tests || bound.agent == nil || bound.tests == nil {
		t.Fatalf("legacy roles = %q %q", bound.agentPath, bound.testsPath)
	}
	// Value embedding keeps the clone checkPublishing rebinds independent.
	clone := *bound
	clone.agent = nil
	if bound.agent == nil {
		t.Fatal("clone aliased the binding's slots")
	}
	created, err := f.createPairBinding(t.Context(), filepath.Join(root, "new", "n.agent.md"), []byte("Prompt\n"), []byte(historyTests))
	if err != nil {
		t.Fatal(err)
	}
	defer created.close()
	if created.agentPath != filepath.Join(root, "new", "n.agent.md") || created.testsPath != filepath.Join(root, "new", "n.agent.test.md") {
		t.Fatalf("legacy create roles = %q %q", created.agentPath, created.testsPath)
	}
}
