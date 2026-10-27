package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

type recordingSave = func(context.Context, []agentfile.Recording) (string, error)

func parseValidator(p string, b []byte) error { _, err := agentfile.Parse(p, b); return err }

// Physical child directories avoid macOS /var aliases and inherited Windows
// ACL differences without weakening the production source checks.
func physicalDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "work")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pendingRecordingSource(t *testing.T) []byte {
	t.Helper()
	_, _, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	source = strings.Replace(source, "    assertions:", "    mocks:\n      tools: [{name: ping, mode: recorded, calls: []}]\n    assertions:", 1)
	if _, err := agentfile.Parse("greeter.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	return []byte(source)
}

func pingRecording(value string) []agentfile.Recording {
	return []agentfile.Recording{{ExampleID: "greeting", Tool: agentfile.MockTool{Name: "ping", Mode: "recorded", Description: "Ping the service",
		Parameters: map[string]any{"type": "object"}, Calls: []agentfile.MockCall{{Arguments: map[string]any{}, Result: map[string]any{"value": value}}}}}}
}

func writeSource(t *testing.T, target string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func requireRecordingError(t *testing.T, err error, allowed ...error) {
	t.Helper()
	for _, want := range allowed {
		if err == want {
			return
		}
	}
	t.Fatalf("want fixed %v, got %v", allowed, err)
}

func requireBytes(t *testing.T, target string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(want) {
		t.Fatalf("%s content changed: %v", filepath.Base(target), err)
	}
}

// recordingTarget prepares one source and returns the caller-selected path,
// the physical file and a freeze function for each supported authority.
type recordingTarget struct {
	name    string
	prepare func(t *testing.T, source []byte) (physical string, freeze func(context.Context, string) (recordingSave, error))
}

func recordingTargets() []recordingTarget {
	return []recordingTarget{
		{name: "mcp-rooted-squad", prepare: func(t *testing.T, source []byte) (string, func(context.Context, string) (recordingSave, error)) {
			root := physicalDir(t)
			f, err := OpenFiles(root, parseValidator)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.Close() })
			rel := ".squad/agents/greeter/charter.md"
			physical := filepath.Join(root, filepath.FromSlash(rel))
			writeSource(t, physical, source)
			return physical, func(ctx context.Context, rev string) (recordingSave, error) { return f.RecordingSaver(ctx, rel, rev) }
		}},
		{name: "mcp-rootless", prepare: func(t *testing.T, source []byte) (string, func(context.Context, string) (recordingSave, error)) {
			f, err := OpenFiles("", parseValidator)
			if err != nil {
				t.Fatal(err)
			}
			physical := filepath.Join(physicalDir(t), "agents", "greeter.agent.md")
			writeSource(t, physical, source)
			return physical, func(ctx context.Context, rev string) (recordingSave, error) {
				return f.RecordingSaver(ctx, physical, rev)
			}
		}},
		{name: "cli-arbitrary-md", prepare: func(t *testing.T, source []byte) (string, func(context.Context, string) (recordingSave, error)) {
			physical := filepath.Join(physicalDir(t), "agents", "notes.md")
			writeSource(t, physical, source)
			return physical, func(ctx context.Context, rev string) (recordingSave, error) {
				return FileRecordingSaver(ctx, physical, rev)
			}
		}},
	}
}

func TestRecordingSaverPublishesCheckedSource(t *testing.T) {
	source := pendingRecordingSource(t)
	want, err := agentfile.ApplyRecordings(source, pingRecording("first"))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range recordingTargets() {
		t.Run(target.name, func(t *testing.T) {
			physical, freeze := target.prepare(t, source)
			if err := os.Chmod(physical, 0640); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(physical)
			if err != nil {
				t.Fatal(err)
			}
			save, err := freeze(t.Context(), revision(source))
			if err != nil {
				t.Fatal(err)
			}
			saved, err := save(t.Context(), pingRecording("first"))
			if err != nil {
				t.Fatal(err)
			}
			if saved != revision(want) {
				t.Fatal("saved revision does not identify the published bytes")
			}
			requireBytes(t, physical, want)
			after, err := os.Stat(physical)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && after.Mode().Perm() != before.Mode().Perm() {
				t.Fatalf("mode %v, want %v", after.Mode().Perm(), before.Mode().Perm())
			}
			entries, err := os.ReadDir(filepath.Dir(physical))
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging residue: %v %v", entries, err)
			}
			// One callback publishes at most once, even after success.
			_, err = save(t.Context(), pingRecording("again"))
			requireRecordingError(t, err, agentfile.ErrRecordingSave)
			requireBytes(t, physical, want)
		})
	}
}

func TestRecordingSaverRetainsBestEffortHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private source history is not qualified on Windows")
	}
	source := pendingRecordingSource(t)
	base := filepath.Join(physicalDir(t), "state", "local-preview", "versions")
	root := physicalDir(t)
	f, err := OpenFilesWithHistory(root, parseValidator, func() (string, error) { return base, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	writeSource(t, filepath.Join(root, "greeter.agent.md"), source)
	save, err := f.RecordingSaver(t.Context(), "greeter.agent.md", revision(source))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := save(t.Context(), pingRecording("first")); err != nil {
		t.Fatal(err)
	}
	view := f.historyView(t.Context(), "greeter.agent.md")
	if view == nil || view.Status != "available" || len(view.Versions) != 1 || view.Versions[0].Revision != revision(source) {
		t.Fatalf("previous source not retained: %+v", view)
	}
}

func TestRecordingSaverWindowsCaseNormalization(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path spelling")
	}
	source := pendingRecordingSource(t)
	filename := filepath.Join(physicalDir(t), "Selected File.md")
	writeSource(t, filename, source)
	selected := strings.ToUpper(filename)
	parsed, err := agentfile.Load(selected)
	if err != nil {
		t.Fatal(err)
	}
	save, err := FileRecordingSaver(t.Context(), selected, parsed.Revision)
	if err != nil {
		t.Fatal("ordinary path spelling refused", err)
	}
	if _, err := save(t.Context(), pingRecording("saved")); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingSaverPathAuthority(t *testing.T) {
	source := pendingRecordingSource(t)
	rev := revision(source)
	dir := physicalDir(t)
	notes := filepath.Join(dir, "notes.md")
	sibling := filepath.Join(dir, "other.agent.md")
	writeSource(t, notes, source)
	writeSource(t, sibling, source)

	t.Run("mcp-rootless-keeps-agent-suffix", func(t *testing.T) {
		f, err := OpenFiles("", parseValidator)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.RecordingSaver(t.Context(), notes, rev); err == nil {
			t.Fatal("inbound rootless MCP gained arbitrary filename authority")
		}
	})
	t.Run("mcp-rooted-keeps-layout", func(t *testing.T) {
		root := physicalDir(t)
		f, err := OpenFiles(root, parseValidator)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		writeSource(t, filepath.Join(root, "notes.md"), source)
		for _, p := range []string{"notes.md", "../work/other.agent.md", sibling} {
			if _, err := f.RecordingSaver(t.Context(), p, rev); err == nil {
				t.Fatalf("rooted MCP accepted %q", p)
			}
		}
	})
	t.Run("cli-exact-file-only", func(t *testing.T) {
		f := &Files{validate: parseValidator, exact: notes}
		if _, err := f.rootRelative(sibling); err == nil {
			t.Fatal("exact capability resolved a different agent file")
		}
		if f.allowedPath(sibling) || !f.allowedPath(notes) {
			t.Fatal("exact capability is not limited to one physical path")
		}
		if _, err := f.RecordingSaver(t.Context(), sibling, rev); err == nil {
			t.Fatal("exact capability froze a sibling")
		}
	})
	t.Run("cli-relative-filename", func(t *testing.T) {
		t.Chdir(dir)
		save, err := FileRecordingSaver(t.Context(), "notes.md", rev)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := save(t.Context(), pingRecording("relative")); err != nil {
			t.Fatal(err)
		}
		requireBytes(t, sibling, source)
	})
	t.Run("cli-alias-refuses", func(t *testing.T) {
		target := filepath.Join(physicalDir(t), "real.md")
		writeSource(t, target, source)
		alias := filepath.Join(filepath.Dir(target), "alias.md")
		if err := os.Symlink(target, alias); err != nil {
			t.Skip("symlinks unavailable")
		}
		_, err := FileRecordingSaver(t.Context(), alias, rev)
		requireRecordingError(t, err, agentfile.ErrRecordingChanged)
		requireBytes(t, target, source)
	})
}

func TestRecordingSaverRefusesChangedSource(t *testing.T) {
	source := pendingRecordingSource(t)
	edited := append([]byte(nil), source...)
	edited = append(edited, '\n')
	mutations := map[string]func(t *testing.T, physical string) []byte{
		"stale-edit": func(t *testing.T, physical string) []byte {
			if err := os.WriteFile(physical, edited, 0600); err != nil {
				t.Fatal(err)
			}
			return edited
		},
		"same-byte-file-replacement": func(t *testing.T, physical string) []byte {
			next := physical + ".next"
			writeSource(t, next, source)
			if err := os.Rename(next, physical); err != nil {
				t.Fatal(err)
			}
			return source
		},
		"same-byte-parent-replacement": func(t *testing.T, physical string) []byte {
			parent := filepath.Dir(physical)
			if err := os.Rename(parent, parent+"-old"); err != nil {
				t.Fatal(err)
			}
			writeSource(t, physical, source)
			return source
		},
	}
	for _, target := range recordingTargets() {
		for name, mutate := range mutations {
			t.Run(target.name+"/"+name, func(t *testing.T) {
				physical, freeze := target.prepare(t, source)
				save, err := freeze(t.Context(), revision(source))
				if err != nil {
					t.Fatal(err)
				}
				current := mutate(t, physical)
				_, err = save(t.Context(), pingRecording("late"))
				requireRecordingError(t, err, agentfile.ErrRecordingChanged)
				requireBytes(t, physical, current)
			})
		}
		t.Run(target.name+"/expected-revision", func(t *testing.T) {
			physical, freeze := target.prepare(t, source)
			_, err := freeze(t.Context(), revision(edited))
			requireRecordingError(t, err, agentfile.ErrRecordingChanged)
			requireBytes(t, physical, source)
		})
	}
}

// A parent renamed after its descriptor was pinned must refuse at the final
// locked check, even though the pinned descriptor itself is unchanged.
func TestObservedParentRootlessRefusesRenamedParent(t *testing.T) {
	f := &Files{validate: parseValidator}
	target := filepath.Join(physicalDir(t), "agents", "greeter.agent.md")
	writeSource(t, target, pendingRecordingSource(t))
	parent, err := f.parent(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	file, _, info, err := readTarget(parent, filepath.Base(target))
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	pinned, err := parent.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	observed := &sourceIdentity{file: info, parent: pinned}
	if !f.observedParent(target, parent, observed) {
		t.Fatal("unchanged rootless parent refused")
	}
	if err := os.Rename(filepath.Dir(target), filepath.Dir(target)+"-old"); err != nil {
		t.Skip("platform keeps an open directory from being renamed")
	}
	if err := os.Mkdir(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if f.observedParent(target, parent, observed) {
		t.Fatal("replacement parent name accepted")
	}
}

func TestRecordingSaverCancellation(t *testing.T) {
	source := pendingRecordingSource(t)
	for _, target := range recordingTargets() {
		t.Run(target.name, func(t *testing.T) {
			physical, freeze := target.prepare(t, source)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := freeze(ctx, revision(source)); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled freeze: %v", err)
			}
			save, err := freeze(t.Context(), revision(source))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := save(ctx, pingRecording("late")); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled save: %v", err)
			}
			requireBytes(t, physical, source)
			// A failed attempt never authorizes an ambiguous retry.
			_, err = save(t.Context(), pingRecording("retry"))
			requireRecordingError(t, err, agentfile.ErrRecordingSave)
			requireBytes(t, physical, source)
		})
	}
}

func TestRecordingSaverCompetingCapturesOneWinner(t *testing.T) {
	source := pendingRecordingSource(t)
	for _, target := range recordingTargets() {
		t.Run(target.name, func(t *testing.T) {
			physical, freeze := target.prepare(t, source)
			values := []string{"a", "b"}
			saves := make([]recordingSave, len(values))
			for i := range saves {
				var err error
				if saves[i], err = freeze(t.Context(), revision(source)); err != nil {
					t.Fatal(err)
				}
			}
			errs := make([]error, len(values))
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range saves {
				wg.Go(func() {
					<-start
					_, errs[i] = saves[i](t.Context(), pingRecording(values[i]))
				})
			}
			close(start)
			wg.Wait()
			winners := 0
			for i, err := range errs {
				if err == nil {
					winners++
					want, applyErr := agentfile.ApplyRecordings(source, pingRecording(values[i]))
					if applyErr != nil {
						t.Fatal(applyErr)
					}
					requireBytes(t, physical, want)
					continue
				}
				requireRecordingError(t, err, agentfile.ErrRecordingChanged, agentfile.ErrRecordingSave)
			}
			if winners != 1 {
				t.Fatalf("winners = %d: %v", winners, errs)
			}
		})
	}
}
