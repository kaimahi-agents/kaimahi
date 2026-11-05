//go:build linux || darwin

package mcp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
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
