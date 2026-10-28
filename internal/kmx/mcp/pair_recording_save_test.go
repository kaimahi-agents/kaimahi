package mcp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

const pairPendingTests = "---\nschemaVersion: 1\n---\n# Keep this prose\n\n```yaml agent-test\nid: greeting\ninput: Ada\nmocks:\n  tools: [{name: ping, mode: recorded, calls: []}]\nassertions: [{id: greeting-text, type: contains, value: Ada}]\n```\n"

func saverPair(t *testing.T, layout string) (*agentfile.Pair, []byte, []byte) {
	t.Helper()
	dir := physicalDir(t)
	agentPath := filepath.Join(dir, "greeter.agent.md")
	if layout == "repo" || layout == "inline-missing-parent" {
		agentPath = filepath.Join(dir, ".github", "agents", "greeter.agent.md")
	}
	agent := pendingRecordingSource(t)
	if !strings.HasPrefix(layout, "inline") && layout != "legacy" {
		agent = []byte(strings.SplitN(string(agent), "## Examples", 2)[0])
	}
	if layout == "legacy" {
		agentPath = filepath.Join(dir, "notes.md")
	}
	writeSource(t, agentPath, agent)
	var tests []byte
	override := ""
	if !strings.HasPrefix(layout, "inline") && layout != "legacy" {
		tests = []byte(pairPendingTests)
		target, _ := agentfile.DefaultTestsPath(agentPath)
		if layout == "override" {
			target = filepath.Join(dir, "selected", "anything.md")
			override = target
			writeSource(t, filepath.Join(dir, "greeter.agent.test.md"), []byte("not selected"))
		}
		writeSource(t, target, tests)
	}
	pair, err := agentfile.LoadPair(agentPath, override)
	if err != nil {
		t.Fatal(err)
	}
	return pair, agent, tests
}

func reloadSaverPair(t *testing.T, pair *agentfile.Pair) *agentfile.Pair {
	t.Helper()
	override := ""
	if pair.Tests.Selection.Explicit {
		override = pair.Tests.Selection.Path
	}
	current, err := agentfile.LoadPair(pair.AgentPath, override)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func noPairResidue(t *testing.T, pair *agentfile.Pair) {
	t.Helper()
	root := filepath.Dir(pair.AgentPath)
	for _, p := range []string{root, filepath.Dir(pair.Tests.Selection.Path)} {
		if p == "." {
			continue
		}
		err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".kmx-") || strings.Contains(d.Name(), "history") {
				t.Errorf("unexpected retained file %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPairRecordingSaverPublishesSelectedSource(t *testing.T) {
	for _, layout := range []string{"beside", "repo", "override", "inline", "inline-missing-parent", "legacy"} {
		t.Run(layout, func(t *testing.T) {
			pair, agent, tests := saverPair(t, layout)
			save, err := FilePairRecordingSaver(t.Context(), pair)
			if err != nil {
				t.Fatal(err)
			}
			ack, err := save(t.Context(), pingRecording("captured"))
			if err != nil {
				t.Fatal(err)
			}
			current := reloadSaverPair(t, pair)
			if ack.SnapshotRevision != current.SnapshotRevision || ack.SnapshotRevision == pair.SnapshotRevision || ack.AgentRevision != current.Agent.Revision {
				t.Fatalf("bad acknowledgement: %+v", ack)
			}
			if pair.Tests.State == "external" {
				if ack.Target != (app.FileRecordingTarget{Role: "tests", Path: pair.Tests.Selection.Path, SelectionKey: pair.Tests.Selection.Key}) || ack.Revision != current.Tests.Revision || ack.AgentRevision != pair.Agent.Revision {
					t.Fatalf("wrong external target: %+v", ack)
				}
				requireBytes(t, pair.AgentPath, agent)
				got, _ := os.ReadFile(ack.Target.Path)
				if !strings.Contains(string(got), "# Keep this prose") || !strings.Contains(string(got), "captured") || string(got) == string(tests) {
					t.Fatal("capture or prose missing")
				}
				if layout == "override" {
					requireBytes(t, filepath.Join(filepath.Dir(pair.AgentPath), "greeter.agent.test.md"), []byte("not selected"))
				}
			} else {
				if ack.Target != (app.FileRecordingTarget{Role: "agent", Path: pair.AgentPath, SelectionKey: pair.Tests.Selection.Key}) || ack.Revision != current.Agent.Revision || ack.Revision == pair.Agent.Revision {
					t.Fatalf("wrong inline target: %+v", ack)
				}
				if pair.Tests.Selection.Path != "" {
					if _, err := os.Stat(pair.Tests.Selection.Path); !os.IsNotExist(err) {
						t.Fatalf("created companion: %v", err)
					}
				}
			}
			_, err = save(t.Context(), pingRecording("retry"))
			requireRecordingError(t, err, agentfile.ErrRecordingSave)
			noPairResidue(t, pair)
		})
	}
}

func TestPairRecordingSaverRejectsWholePairChanges(t *testing.T) {
	for _, when := range []string{"preparation", "callback"} {
		for _, role := range []string{"agent", "tests"} {
			for _, mutation := range []string{"bytes", "file", "parent", "deleted"} {
				t.Run(when+"/"+role+"/"+mutation, func(t *testing.T) {
					pair, agent, tests := saverPair(t, "override")
					var save app.PairRecordingSaver
					var err error
					if when == "callback" {
						save, err = FilePairRecordingSaver(t.Context(), pair)
						if err != nil {
							t.Fatal(err)
						}
					}
					target, source := pair.AgentPath, agent
					if role == "tests" {
						target, source = pair.Tests.Selection.Path, tests
					}
					switch mutation {
					case "bytes":
						writeSource(t, target, append(append([]byte(nil), source...), '\n'))
					case "file":
						if err := os.Rename(target, target+".original"); err != nil {
							t.Fatal(err)
						}
						writeSource(t, target, source)
					case "parent":
						if err := os.Rename(filepath.Dir(target), filepath.Dir(target)+".original"); err != nil {
							t.Fatal(err)
						}
						writeSource(t, target, source)
					case "deleted":
						if err := os.Remove(target); err != nil {
							t.Fatal(err)
						}
					}
					if when == "preparation" {
						_, err = FilePairRecordingSaver(t.Context(), pair)
					} else {
						_, err = save(t.Context(), pingRecording("must-not-save"))
					}
					requireRecordingError(t, err, agentfile.ErrRecordingChanged)
					if mutation != "deleted" {
						got, _ := os.ReadFile(target)
						if strings.Contains(string(got), "must-not-save") {
							t.Fatal("stale capture published")
						}
					}
					noPairResidue(t, pair)
				})
			}
		}
	}
}

func TestPairRecordingSaverInlineFencesDefaultAppearance(t *testing.T) {
	for _, layout := range []string{"inline", "inline-missing-parent"} {
		for _, when := range []string{"preparation", "callback"} {
			t.Run(layout+"/"+when, func(t *testing.T) {
				pair, agent, _ := saverPair(t, layout)
				var save app.PairRecordingSaver
				var err error
				if when == "callback" {
					save, err = FilePairRecordingSaver(t.Context(), pair)
					if err != nil {
						t.Fatal(err)
					}
				}
				writeSource(t, pair.Tests.Selection.Path, []byte(pairPendingTests))
				if when == "preparation" {
					_, err = FilePairRecordingSaver(t.Context(), pair)
				} else {
					_, err = save(t.Context(), pingRecording("late"))
				}
				requireRecordingError(t, err, agentfile.ErrRecordingChanged)
				requireBytes(t, pair.AgentPath, agent)
				noPairResidue(t, pair)
			})
		}
	}
}

func TestPairRecordingSaverCancellationAndInvalidCandidate(t *testing.T) {
	for _, layout := range []string{"beside", "inline"} {
		for _, attempt := range []string{"cancel", "invalid", "oversize"} {
			t.Run(layout+"/"+attempt, func(t *testing.T) {
				pair, agent, tests := saverPair(t, layout)
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if _, err := FilePairRecordingSaver(ctx, pair); !errors.Is(err, context.Canceled) {
					t.Fatalf("preparation: %v", err)
				}
				save, err := FilePairRecordingSaver(t.Context(), pair)
				if err != nil {
					t.Fatal(err)
				}
				recordings := pingRecording("late")
				want := agentfile.ErrRecordingInvalid
				if attempt == "cancel" {
					_, err = save(ctx, recordings)
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("save: %v", err)
					}
				} else {
					if attempt == "invalid" {
						recordings[0].Tool.Name = "undeclared"
					} else {
						recordings[0].Tool.Calls[0].Result = map[string]any{"value": strings.Repeat("x", maxFileBytes)}
					}
					_, err = save(t.Context(), recordings)
					requireRecordingError(t, err, want)
				}
				_, err = save(t.Context(), pingRecording("retry"))
				requireRecordingError(t, err, agentfile.ErrRecordingSave)
				requireBytes(t, pair.AgentPath, agent)
				if tests != nil {
					requireBytes(t, pair.Tests.Selection.Path, tests)
				}
				noPairResidue(t, pair)
			})
		}
	}
}

func TestPairRecordingSaverConcurrentOneWinner(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "two-callbacks", true: "one-callback"}[shared], func(t *testing.T) {
			pair, agent, _ := saverPair(t, "override")
			saves := make([]app.PairRecordingSaver, 2)
			for i := range saves {
				var err error
				saves[i], err = FilePairRecordingSaver(t.Context(), pair)
				if err != nil {
					t.Fatal(err)
				}
			}
			if shared {
				saves[1] = saves[0]
			}
			errs := make([]error, 2)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range saves {
				wg.Go(func() { <-start; _, errs[i] = saves[i](t.Context(), pingRecording("winner")) })
			}
			close(start)
			wg.Wait()
			winners := 0
			for _, err := range errs {
				if err == nil {
					winners++
				} else {
					requireRecordingError(t, err, agentfile.ErrRecordingChanged, agentfile.ErrRecordingSave)
				}
			}
			if winners != 1 {
				t.Fatalf("winners = %d, errors %v", winners, errs)
			}
			requireBytes(t, pair.AgentPath, agent)
			noPairResidue(t, pair)
		})
	}
}

func TestPairRecordingSaverPreparationRetainsNoDescriptors(t *testing.T) {
	pair, _, _ := saverPair(t, "override")
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("descriptor inventory unavailable")
	}
	callbacks := make([]app.PairRecordingSaver, 40)
	for i := range callbacks {
		callbacks[i], err = FilePairRecordingSaver(t.Context(), pair)
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before)+1 {
		t.Fatalf("preparation leaked %d descriptors", len(after)-len(before))
	}
	noPairResidue(t, pair)
}

// Exercise the real checked publisher's post-stage, locked hook. Mutating here
// catches a saver that checks the pair only outside the publication boundary.
func TestPairRecordingPublicationFinalChecks(t *testing.T) {
	for _, mutation := range []string{"own-stage", "agent-bytes", "agent-file", "agent-parent", "tests-bytes", "tests-file", "tests-parent", "default-file", "default-directory", "publication-failure"} {
		t.Run(mutation, func(t *testing.T) {
			layout := "override"
			if strings.HasPrefix(mutation, "default-") {
				layout = "inline-missing-parent"
			}
			pair, _, _ := saverPair(t, layout)
			agent, err := holdPairSource(pair.AgentPath, pair.Agent.Revision)
			if err != nil {
				t.Fatal(err)
			}
			defer agent.close()
			testsRevision := ""
			if pair.Tests.State == "external" {
				testsRevision = pair.Tests.Revision
			}
			tests, err := holdPairSource(pair.Tests.Selection.Path, testsRevision)
			if err != nil {
				t.Fatal(err)
			}
			defer tests.close()
			want := map[string][]byte{agent.path: agent.source, tests.path: tests.source}
			if !pair.SameSourceVersion(reloadSaverPair(t, pair)) {
				t.Fatal("source changed before stage")
			}
			check := func() error {
				if err := agent.check(); err != nil {
					return err
				}
				return tests.check()
			}
			target, patch := tests, agentfile.ApplyTestRecordings
			if pair.Tests.State == "inline" {
				target, patch = agent, agentfile.ApplyRecordings
			}
			candidate, err := patch(target.source, pingRecording("must-not-save"))
			if err != nil {
				t.Fatal(err)
			}
			f := &Files{exact: target.path, validate: func(p string, b []byte) error {
				if pair.Tests.State == "inline" {
					return parseValidator(p, b)
				}
				_, err := agentfile.ComposePair(pair.AgentPath, pair.Agent, pair.Tests.Selection, b, true)
				return err
			}}
			beforePublish := func() error {
				staged, err := filepath.Glob(filepath.Join(target.parentPath, ".kmx-mcp-*.tmp"))
				if err != nil || len(staged) != 1 {
					t.Fatalf("hook did not run after stage: %v %v", staged, err)
				}
				source := agent
				if strings.HasPrefix(mutation, "tests-") {
					source = tests
				}
				switch {
				case mutation == "default-file":
					want[tests.path] = []byte(pairPendingTests)
					writeSource(t, tests.path, want[tests.path])
				case mutation == "default-directory":
					if err := os.Mkdir(filepath.Dir(tests.path), 0700); err != nil {
						t.Fatal(err)
					}
				case mutation == "publication-failure":
					if err := os.Remove(staged[0]); err != nil {
						t.Fatal(err)
					}
				case strings.HasSuffix(mutation, "-bytes"):
					// Same-size writes plus restored mtime force the exact hash check.
					changed := strings.Replace(string(source.source), "ping", "pong", 1)
					if changed == string(source.source) {
						t.Fatal("fixture did not change")
					}
					want[source.path] = []byte(changed)
					writeSource(t, source.path, want[source.path])
					if err := os.Chtimes(source.path, source.identity.file.ModTime(), source.identity.file.ModTime()); err != nil {
						t.Fatal(err)
					}
				case strings.HasSuffix(mutation, "-file"):
					if err := os.Rename(source.path, source.path+".original"); err != nil {
						t.Skip("open-file rename unavailable")
					}
					want[source.path+".original"] = source.source
					writeSource(t, source.path, source.source)
				case strings.HasSuffix(mutation, "-parent"):
					if err := os.Rename(source.parentPath, source.parentPath+".original"); err != nil {
						t.Skip("open-parent rename unavailable")
					}
					for path, data := range want {
						if relative, err := filepath.Rel(source.parentPath, path); err == nil && filepath.IsLocal(relative) {
							want[filepath.Join(source.parentPath+".original", relative)] = data
							want[path] = nil
						}
					}
					want[source.path] = source.source
					writeSource(t, source.path, source.source)
				}
				return check()
			}
			ack, err := f.editObservedChecked(t.Context(), target.path, target.expected, candidate, &target.identity, check, beforePublish)
			if mutation == "own-stage" {
				if err != nil || ack.Revision != revision(candidate) {
					t.Fatalf("own staging refused: %+v %v", ack, err)
				}
				want[target.path] = candidate
			} else {
				code := "stale_revision"
				if mutation == "publication-failure" {
					code = "publication_failed"
				}
				requireCode(t, err, code)
				if ack.Revision != "" {
					t.Fatalf("fabricated publication acknowledgement: %+v", ack)
				}
			}
			for path, source := range want {
				if source == nil {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("absent tests source changed: %v", err)
					}
				} else {
					requireBytes(t, path, source)
				}
			}
			// The held old parent also needs its staging file removed after a rename.
			dir, err := target.parent.Open(".")
			if err != nil {
				t.Fatal(err)
			}
			entries, err := dir.ReadDir(-1)
			dir.Close()
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".kmx-") {
					t.Fatalf("staging residue: %s", entry.Name())
				}
			}
			noPairResidue(t, pair)
		})
	}
}
