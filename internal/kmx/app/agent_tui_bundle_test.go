package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// consoleBundleFixture reuses the shared status fixture (the same fake
// kubectl, cluster identity and golden bundle `kmx agent status` is tested
// against), so the pane is proved against exactly the report status prints,
// never against a parallel fake of its own.
//
// The status fixture writes its bundle at <dir>/portable. The console finds a
// bundle at <root>/<agent-name>, so the root returned here holds a directory
// named after the agent that IS that bundle.
func consoleBundleFixture(t *testing.T) (a *App, root, bundle, dir, name string, git string, rendered func() error) {
	t.Helper()
	gitPath, _ := exec.LookPath("git")
	originalPATH := os.Getenv("PATH")
	app, opt, fixtureDir, r, agentName := bundleStatusFixture(t)
	// The status fixture pins PATH to its fake kubectl; git must stay reachable.
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+originalPATH)
	root = filepath.Join(fixtureDir, "bundles")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(opt.BundleDir, filepath.Join(root, agentName)); err != nil {
		t.Fatal(err)
	}
	seed := func() error { seedBundleLiveResources(t, fixtureDir, r, agentName, nil); return nil }
	return app, root, opt.BundleDir, fixtureDir, agentName, gitPath, seed
}

// A missing bundle is the only condition that permits an explicit live-copy lift.
// An existing directory with a missing or foreign portable definition must stop.
func TestConsoleLiftBundleResolutionDistinguishesMissingFromInvalid(t *testing.T) {
	root := t.TempDir()
	if dir, found, err := consoleAgentBundle(root, "demo"); err != nil || found || dir != filepath.Join(root, "demo") {
		t.Fatalf("missing bundle: dir=%q found=%t err=%v", dir, found, err)
	}
	if err := os.Mkdir(filepath.Join(root, "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, found, err := consoleAgentBundle(root, "demo"); err == nil || found {
		t.Fatalf("incomplete bundle treated as live-copy candidate: found=%t err=%v", found, err)
	}
	if _, found, err := consoleAgentBundle(root, "../demo"); err == nil || found {
		t.Fatalf("unsafe live name accepted: found=%t err=%v", found, err)
	}
}

func TestConsoleLiftRejectsInvalidBundleBeforeCreatingTarget(t *testing.T) {
	a, root, bundle, _, name, _, _ := consoleBundleFixture(t)
	if err := os.Remove(filepath.Join(bundle, "bindings.yaml")); err != nil {
		t.Fatal(err)
	}
	action := agentTUIAction{kind: "lift", agent: consoleBundleRow(name), source: consoleBundleEnv(),
		bundles: root, create: &lift.Options{Cluster: "not-created", ResourceGroup: "not-created"}}
	_, err := a.runAgentTUIAction(action)
	if err == nil || !strings.Contains(err.Error(), "bindings.yaml") {
		t.Fatalf("invalid bundle did not stop target creation first: %v", err)
	}
}

func consoleBundleEnv() agentTUIEnvironment {
	return agentTUIEnvironment{Name: "kind-test", Local: true}
}

func consoleBundleRow(name string) agentTUIAgent {
	return agentTUIAgent{Name: name, Namespace: "orka-system", Runtime: "orka"}
}

// The pane reports the same state status reports, for the selected row's
// own context and namespace.
func TestConsoleBundleReportsInSyncThroughTheSharedStatusReport(t *testing.T) {
	a, root, _, _, name, _, seed := consoleBundleFixture(t)
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow(name), root)
	if snapshot.Err != "" {
		t.Fatalf("Err = %q", snapshot.Err)
	}
	if snapshot.Target == nil || snapshot.Target.State != bundleStateInSync {
		t.Fatalf("target = %+v, want in sync", snapshot.Target)
	}
	if snapshot.Target.Context != "kind-test" || snapshot.Target.Namespace != "orka-system" {
		t.Fatalf("the pane reported a different target from the row: %+v", snapshot.Target)
	}
	if len(snapshot.Diff) != 0 || snapshot.DiffNote != "" {
		t.Errorf("an in-sync target produced a diff: %v %q", snapshot.Diff, snapshot.DiffNote)
	}
}

// Behind is the case this pane exists for: status says THAT a lift would
// change something, and the pane shows WHAT, from Git.
func TestConsoleBundleShowsWhatALiftWouldChangeFromGit(t *testing.T) {
	a, root, bundle, _, name, git, seed := consoleBundleFixture(t)
	if git == "" {
		t.Skip("git unavailable")
	}
	runBundleStatusGit(t, git, bundle, "init", "--quiet")
	runBundleStatusGit(t, git, bundle, "add", "agent.yaml")
	runBundleStatusGit(t, git, bundle, "commit", "--quiet", "-m", "deployed revision")
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundle, "agent.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(original, []byte("# a later revision\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	runBundleStatusGit(t, git, bundle, "commit", "--quiet", "-am", "later revision")

	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow(name), root)
	if snapshot.Target == nil || snapshot.Target.State != bundleStateBehind {
		t.Fatalf("target = %+v (Err %q), want behind", snapshot.Target, snapshot.Err)
	}
	if !bundleDiffHas(snapshot.Diff, "+# a later revision") {
		t.Fatalf("the diff does not show the added line:\n%s", strings.Join(snapshot.Diff, "\n"))
	}
	for _, line := range snapshot.Diff {
		if strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			t.Errorf("git's file header reached the pane: %q", line)
		}
	}
}

// Status searches history once. A behind pane must reuse that revision
// instead of searching the same commits again before drawing the diff.
func TestConsoleBundleBehindSearchesHistoryOnlyOnce(t *testing.T) {
	a, root, _, _, name, git := consoleBundleBehind(t, true, "# later\n")
	log := filepath.Join(t.TempDir(), "git-calls")
	shim := filepath.Join(t.TempDir(), "git")
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellArg(log) + "\nexec " + shellArg(git) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(shim)+string(os.PathListSeparator)+os.Getenv("PATH"))
	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow(name), root)
	if snapshot.Target == nil || snapshot.Target.State != bundleStateBehind || !bundleDiffHas(snapshot.Diff, "+# later") {
		t.Fatalf("behind pane: %+v", snapshot)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	shows := 0
	for _, call := range strings.Split(string(calls), "\n") {
		if strings.Contains(" "+call+" ", " show ") && !strings.Contains(call, " show HEAD:") {
			shows++
		}
	}
	if shows != 2 { // HEAD and the deployed revision, once each
		t.Fatalf("searched Git history %d show calls, want 2: %s", shows, calls)
	}
}

// The working copy is what `kmx agent lift` reads, so the pane diffs against
// it, including edits not yet committed.
func TestConsoleBundleDiffIncludesUncommittedEdits(t *testing.T) {
	a, root, bundle, _, name, git, seed := consoleBundleFixture(t)
	if git == "" {
		t.Skip("git unavailable")
	}
	runBundleStatusGit(t, git, bundle, "init", "--quiet")
	runBundleStatusGit(t, git, bundle, "add", "agent.yaml")
	runBundleStatusGit(t, git, bundle, "commit", "--quiet", "-m", "deployed revision")
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundle, "agent.yaml")
	original, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(original, []byte("# not yet committed\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow(name), root)
	if snapshot.Target == nil || snapshot.Target.State != bundleStateBehind {
		t.Fatalf("target = %+v, want behind", snapshot.Target)
	}
	if !bundleDiffHas(snapshot.Diff, "+# not yet committed") {
		t.Fatalf("an uncommitted edit a lift would deploy is missing from the diff:\n%s", strings.Join(snapshot.Diff, "\n"))
	}
}

// A console row is a live agent. Without its bundle there is nothing to
// compare, and the pane says where it looked instead of guessing.
func TestConsoleBundleSaysWhereItLookedWhenThereIsNoBundle(t *testing.T) {
	a, root, _, _, _, _, _ := consoleBundleFixture(t)
	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow("someone-else"), root)
	if snapshot.Target != nil {
		t.Fatalf("a missing bundle produced a verdict: %+v", snapshot.Target)
	}
	for _, want := range []string{"No local bundle", filepath.Join(root, "someone-else", "agent.yaml"), "--bundles"} {
		if !strings.Contains(snapshot.Err, want) {
			t.Errorf("Err omits %q: %q", want, snapshot.Err)
		}
	}
}

// A directory named after this agent that defines another one is a
// different agent; comparing it would report on objects this row does not
// show.
func TestConsoleBundleRefusesABundleThatDefinesADifferentAgent(t *testing.T) {
	a, root, bundle, _, name, _, _ := consoleBundleFixture(t)
	if err := os.Symlink(bundle, filepath.Join(root, "impostor")); err != nil {
		t.Fatal(err)
	}
	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow("impostor"), root)
	if snapshot.Target != nil {
		t.Fatalf("a bundle for %q was compared against row impostor: %+v", name, snapshot.Target)
	}
	if !strings.Contains(snapshot.Err, "refusing to compare different agents") {
		t.Fatalf("Err = %q", snapshot.Err)
	}
	if calls := orkaCalls(t, filepath.Dir(root)); len(calls) != 0 {
		t.Fatalf("mismatched bundle read the cluster %d times: %+v", len(calls), calls)
	}
}

// An unreachable row cannot be called in sync, nor can it trigger a diff.
func TestConsoleBundleUnreachableClusterHasNoVerdictOrGitDiff(t *testing.T) {
	a, root, _, _, name, git, _ := consoleBundleFixture(t)
	if git == "" {
		t.Skip("git unavailable")
	}
	log := filepath.Join(t.TempDir(), "git-calls")
	shim := filepath.Join(t.TempDir(), "git")
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellArg(log) + "\nexec " + shellArg(git) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(shim)+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := consoleBundleEnv()
	env.Name = "unreachable-context"
	snapshot := a.consoleBundleStatus(t.Context(), env, consoleBundleRow(name), root)
	if snapshot.Err != "" || snapshot.Target == nil || snapshot.Target.State != bundleStateUnknown || snapshot.Target.Detail == "" {
		t.Fatalf("unreachable row: %+v", snapshot)
	}
	pane := &consoleBundlePane{env: env, agent: consoleBundleRow(name), snapshot: snapshot}
	view := ansi.Strip(strings.Join(pane.lines(200), "\n"))
	if !strings.Contains(view, "State: unknown") || !strings.Contains(view, snapshot.Target.Detail) || strings.Contains(view, "State: in sync") {
		t.Fatalf("unreachable pane: %s", view)
	}
	if len(snapshot.Diff) != 0 || snapshot.DiffNote != "" {
		t.Fatalf("unreachable row produced a diff: %+v", snapshot)
	}
	calls, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, call := range strings.Split(string(calls), "\n") {
		if strings.Contains(" "+call+" ", " diff ") {
			t.Fatalf("unreachable row ran git diff: %s", call)
		}
	}
}

// The row's name comes from a live cluster and is about to become a path.
func TestConsoleBundleRefusesANameThatCannotBeADirectory(t *testing.T) {
	a := &App{}
	for _, name := range []string{"../escape", "a/b", ""} {
		snapshot := a.consoleBundleStatus(context.Background(), consoleBundleEnv(), consoleBundleRow(name), t.TempDir())
		if snapshot.Err == "" || snapshot.Target != nil {
			t.Errorf("name %q: snapshot = %+v, want a refusal", name, snapshot)
		}
	}
}

// Every value in a diff is screened against the shared credential list, and
// the withheld line keeps its +/- marker so the reader still sees a change.
func TestConsoleBundleDiffWithholdsEveryCredentialShape(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			raw := "diff --git a/agent.yaml b/agent.yaml\n@@ -1 +1 @@\n-  note: safe\n+  note: " + shape.Example + "\n"
			lines := consoleBundleDiffLines(raw)
			joined := strings.Join(lines, "\n")
			if strings.Contains(joined, shape.Example) {
				t.Fatalf("%s reached the pane", shape.What)
			}
			if !bundleDiffHas(lines, "+ "+consoleBundleWithheld) {
				t.Errorf("the withheld line lost its marker or label:\n%s", joined)
			}
		})
	}
}

// A diff is file content, and file content can carry escape sequences that a
// terminal would execute.
//
// The payloads avoid anything credential-shaped on purpose. A line the
// credential screen withholds never reaches the control-character strip, so
// a test built from such a payload would pass even with the strip removed.
func TestConsoleBundleDiffStripsTerminalControls(t *testing.T) {
	for name, payload := range map[string]string{
		"clear screen": "\x1b[2J",
		"cursor home":  "\x1b[H",
		"window title": "\x1b]0;owned\x07",
		"hyperlink":    "\x1b]8;;http://x.invalid\x1b\\",
		"bell":         "\x07",
		// ESC followed by any byte is a complete two-byte sequence, so the
		// byte after it is consumed with it. A space keeps "after" intact.
		"bare escape": "\x1b ",
		"c1 csi":      "\u009b2J",
	} {
		t.Run(name, func(t *testing.T) {
			raw := "@@ -1 +1 @@\n+  instructions: before" + payload + "after\n"
			lines := consoleBundleDiffLines(raw)
			if len(lines) != 2 {
				t.Fatalf("lines = %q", lines)
			}
			if bundleDiffHas(lines, "+ "+consoleBundleWithheld) {
				t.Fatal("the payload was withheld as a credential, so this case does not test the strip")
			}
			for _, r := range lines[1] {
				if r == 0x1b || r == 0x07 || r == 0x9b {
					t.Fatalf("a control character survived: %q", lines[1])
				}
			}
			if !strings.Contains(lines[1], "before") || !strings.Contains(lines[1], "after") {
				t.Errorf("the surrounding text was lost: %q", lines[1])
			}
		})
	}
}

// Anything git prints before the first hunk, including a header that looks
// like a line of content, is not part of the change.
func TestConsoleBundleDiffKeepsOnlyHunks(t *testing.T) {
	raw := "diff --git a/agent.yaml b/agent.yaml\nindex 1..2 100644\n--- a/agent.yaml\n+++ b/agent.yaml\n@@ -1,2 +1,2 @@\n context\n-old\n+new\n\\ No newline at end of file\n"
	got := consoleBundleDiffLines(raw)
	want := []string{"@@ -1,2 +1,2 @@", " context", "-old", "+new", "\\ No newline at end of file"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// A revision that began with "-" would be read by git as an option, and an
// abbreviated one is not an identity: it can be ambiguous, or name a branch.
func TestConsoleBundleDiffOnlyPassesAFullCommitIdToGit(t *testing.T) {
	for _, commit := range []string{"--output=/tmp/x", "-p", "HEAD", "main", "3f9c2e1 --stat", "", "3f9c2e1",
		strings.Repeat("a", 39), strings.Repeat("A", 40), strings.Repeat("a", 41)} {
		lines, note := consoleBundleDiff(context.Background(), t.TempDir(), commit)
		if lines != nil || !strings.Contains(note, "not a full commit id") {
			t.Errorf("commit %q: lines=%v note=%q", commit, lines, note)
		}
	}
}

// With several receipts for one context (a context that has named more than
// one cluster), the pane reports the cluster the context names now.
func TestConsoleBundleTargetPrefersTheClusterTheContextNamesNow(t *testing.T) {
	report := bundleStatusReport{Targets: []bundleTargetStatus{
		{Context: "prod", Namespace: "orka-system", State: bundleStateChanged},
		{Context: "prod", Namespace: "orka-system", State: bundleStateBehind},
		{Context: "prod", Namespace: "other", State: bundleStateInSync},
	}}
	target, ok := consoleBundleTarget(report, "prod", "orka-system")
	if !ok || target.State != bundleStateBehind {
		t.Fatalf("target = %+v, want the current cluster's behind", target)
	}
	only := bundleStatusReport{Targets: []bundleTargetStatus{{Context: "prod", Namespace: "orka-system", State: bundleStateChanged}}}
	if target, ok := consoleBundleTarget(only, "prod", "orka-system"); !ok || target.State != bundleStateChanged {
		t.Fatalf("a lone changed target was dropped: %+v", target)
	}
	if _, ok := consoleBundleTarget(report, "staging", "orka-system"); ok {
		t.Fatal("a target for a different context was reported")
	}
}

// b opens the pane over the dashboard; the dashboard's own navigation keys
// then belong to the pane until it closes.
func TestConsoleBundlePaneOpensScrollsAndCloses(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height = 100, 20
	m = tuiKey(m, 'b', "b")
	if m.bundle == nil || m.bundle.loading {
		t.Fatalf("b did not open a loaded demo pane: %+v", m.bundle)
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"BUNDLE · assistant", "State: behind", "What kmx agent lift would change"} {
		if !strings.Contains(view, want) {
			t.Errorf("the pane omits %q:\n%s", want, view)
		}
	}
	// A short terminal shows the top of the pane; the rest is reached by
	// scrolling rather than cut off.
	const deep = "+    You are a careful cluster assistant."
	if strings.Contains(view, deep) {
		t.Fatal("the fixture no longer needs scrolling; use a shorter terminal")
	}
	selection := m.columns[0].Selection
	m = tuiKey(m, 'j', "j")
	if m.columns[0].Selection != selection {
		t.Fatal("j moved the dashboard selection behind an open pane")
	}
	if m.bundle.scroll != 1 {
		t.Fatalf("j did not scroll the pane: scroll=%d", m.bundle.scroll)
	}
	m = tuiKey(m, 'G', "G")
	if m.bundle.scroll == 0 {
		t.Fatal("G did not reach the end of a long pane")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, deep) {
		t.Fatalf("scrolling to the end did not reveal the rest of the diff:\n%s", view)
	}
	m = tuiKey(m, tea.KeyEsc, "")
	if m.bundle != nil {
		t.Fatal("escape did not close the pane")
	}
}

// ctrl+c still quits the console from inside the pane.
func TestConsoleBundlePaneDoesNotSwallowQuit(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m = tuiKey(m, 'b', "b")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not quit from inside the bundle pane")
	}
}

// A read that finishes after the operator closed or reopened the pane must
// not overwrite what is on screen.
func TestConsoleBundleDropsAResultForAPaneNoLongerOpen(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.opt.Demo = false
	m.loadBundle = func(context.Context, agentTUIEnvironment, agentTUIAgent) consoleBundleSnapshot {
		return consoleBundleSnapshot{}
	}
	m = tuiKey(m, 'b', "b")
	stale := m.bundle
	m = tuiKey(m, 'r', "r")
	if m.bundle == stale || !m.bundle.loading {
		t.Fatal("reload did not open a fresh pane")
	}
	next, _ := m.Update(consoleBundleLoaded{pane: stale, snapshot: consoleBundleSnapshot{Err: "stale"}})
	m = next.(agentTUIModel)
	if m.bundle.snapshot.Err == "stale" || !m.bundle.loading {
		t.Fatal("a result for a replaced pane was drawn")
	}
	next, _ = m.Update(consoleBundleLoaded{pane: m.bundle, snapshot: consoleBundleSnapshot{Err: "current"}})
	if got := next.(agentTUIModel).bundle.snapshot.Err; got != "current" {
		t.Fatalf("the current pane's result was dropped: %q", got)
	}
}

// Every state the report can return says what it means, and never lets a
// missing verdict read as agreement.
func TestConsoleBundleExplainsEveryState(t *testing.T) {
	for state, want := range map[string]string{
		bundleStateInSync:      "What is running matches",
		bundleStateBehind:      "What kmx agent lift would change",
		bundleStateDrifted:     "Values are not shown",
		bundleStateNotDeployed: "Lift the bundle to deploy it",
		bundleStateForeign:     "belong to a different bundle",
		bundleStateChanged:     "names a different cluster",
		bundleStateUnknown:     "That is not the same as in sync",
	} {
		p := &consoleBundlePane{env: consoleBundleEnv(), agent: consoleBundleRow("sample"),
			snapshot: consoleBundleSnapshot{Dir: "agents/sample", Target: &bundleTargetStatus{State: state}}}
		text := ansi.Strip(strings.Join(p.lines(200), "\n"))
		if !strings.Contains(text, want) {
			t.Errorf("state %q: missing %q in:\n%s", state, want, text)
		}
		if !strings.Contains(text, "State: "+state) {
			t.Errorf("state %q is not printed as a word; colour must not carry it alone", state)
		}
	}
}

// Drift shows paths only. Status's rule is that live values are untrusted, and
// the pane must not become a way around it.
func TestConsoleBundleDriftShowsPathsNeverValues(t *testing.T) {
	p := &consoleBundlePane{env: consoleBundleEnv(), agent: consoleBundleRow("sample"), snapshot: consoleBundleSnapshot{
		Dir: "agents/sample", Target: &bundleTargetStatus{State: bundleStateDrifted, ChangedFields: []string{"Agent.spec.systemPrompt.inline"}}}}
	text := ansi.Strip(strings.Join(p.lines(200), "\n"))
	if !strings.Contains(text, "• Agent.spec.systemPrompt.inline") {
		t.Fatalf("the changed path is missing:\n%s", text)
	}
}

// External runtimes have no bundle; the key says so rather than opening an
// empty pane.
func TestConsoleBundleIsNotOfferedForExternalRuntimes(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.columns[0].Selection = 2 // the demo's external runtime
	m = tuiKey(m, 'b', "b")
	if m.bundle != nil {
		t.Fatal("a bundle pane opened for an external runtime")
	}
	if !strings.Contains(m.status, "external runtime") {
		t.Fatalf("status = %q", m.status)
	}
	for _, action := range m.agentActions(*m.selected()) {
		if action.key == "b" {
			t.Fatal("the actions menu offers a bundle comparison for an external runtime")
		}
	}
}

// The pane fits small terminals and never writes a line wider than the screen.
func TestConsoleBundlePaneFitsTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{64, 18}, {80, 24}, {160, 50}} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		m.width, m.height = size[0], size[1]
		m = tuiKey(m, 'b', "b")
		for i, line := range strings.Split(m.View().Content, "\n") {
			if w := ansi.StringWidth(line); w > size[0] {
				t.Fatalf("%dx%d: line %d is %d wide", size[0], size[1], i, w)
			}
		}
	}
}

// The pane reads and never writes: no cluster object, no receipt, no
// remembered selection. And every read is pinned to the selected row's own
// context, because the pane aims status at a different environment per row.
func TestConsoleBundleWritesNothingAndPinsTheRowsContext(t *testing.T) {
	a, root, bundle, dir, name, _, seed := consoleBundleFixture(t)
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	writeBundleReceipt(t, bundle, "kind-test", "orka-system", "cluster-uid", name, "uncommitted")
	receipts, err := os.ReadDir(filepath.Join(bundle, "receipts"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipt setup: %v %v", receipts, err)
	}
	receiptPath := filepath.Join(bundle, "receipts", receipts[0].Name())
	receiptBefore, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := bundleLiftSelectionPath(bundle)
	if err != nil {
		t.Fatal(err)
	}
	before := len(orkaCalls(t, dir))

	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow(name), root)
	if snapshot.Target == nil || snapshot.Target.State != bundleStateInSync {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	calls := orkaCalls(t, dir)[before:]
	if len(calls) == 0 {
		t.Fatal("the pane read nothing; this test would prove nothing")
	}
	for _, call := range calls {
		if slices.Contains(call.Args, "patch") || call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("the pane wrote a resource: %+v", call)
		}
		if !slices.Equal(call.Args[:2], []string{"--context", "kind-test"}) {
			t.Fatalf("a read was not pinned to the row's context: %+v", call)
		}
	}
	receiptAfter, err := os.ReadFile(receiptPath)
	if err != nil || !bytes.Equal(receiptBefore, receiptAfter) {
		t.Fatalf("the pane modified a receipt: %v", err)
	}
	if _, err := os.Stat(selection); !os.IsNotExist(err) {
		t.Fatalf("the pane wrote a remembered selection: %v", err)
	}
}

func bundleDiffHas(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

// consoleBundleBehind commits the deployed revision, deploys it, then commits
// `later` on top, so status reports the target behind. With repoAbove the
// repository is rooted one level above the bundle (the real
// <repo>/agents/<name> layout), so the path handed to git is not simply
// agent.yaml.
func consoleBundleBehind(t *testing.T, repoAbove bool, later string) (a *App, root, bundle, repo, name, git string) {
	t.Helper()
	a, root, bundle, dir, name, git, seed := consoleBundleFixture(t)
	if git == "" {
		t.Skip("git unavailable")
	}
	repo, rel := bundle, "agent.yaml"
	if repoAbove {
		repo, rel = dir, filepath.Join(filepath.Base(bundle), "agent.yaml")
	}
	runBundleStatusGit(t, git, repo, "init", "--quiet")
	runBundleStatusGit(t, git, repo, "add", rel)
	runBundleStatusGit(t, git, repo, "commit", "--quiet", "-m", "deployed revision")
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundle, "agent.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(original, []byte(later)...), 0o600); err != nil {
		t.Fatal(err)
	}
	runBundleStatusGit(t, git, repo, "commit", "--quiet", "-am", "later revision")
	return a, root, bundle, repo, name, git
}

// The real layout: the repository holds agents/<name>/agent.yaml, so the path
// git is given, and the history status searches, are one directory down.
func TestConsoleBundleDiffWorksWhenTheRepositoryIsAboveTheBundle(t *testing.T) {
	a, root, _, _, name, _ := consoleBundleBehind(t, true, "# edited one level down\n")
	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow(name), root)
	if snapshot.Target == nil || snapshot.Target.State != bundleStateBehind {
		t.Fatalf("target = %+v (Err %q), want behind", snapshot.Target, snapshot.Err)
	}
	if !bundleDiffHas(snapshot.Diff, "+# edited one level down") {
		t.Fatalf("diff = %q, note = %q", snapshot.Diff, snapshot.DiffNote)
	}
}

// git refreshes stat data in .git/index, taking index.lock, when it diffs a
// commit against a working file whose timestamps moved but whose content
// matches that commit. The pane only diffs a target that is behind, where the
// contents differ, so its own flow does not reach that case. But the diff
// function promises to be a read, so it is held to that directly, under the
// exact condition that makes git write.
func TestConsoleBundleDiffDoesNotWriteTheGitIndex(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	bundle := filepath.Join(repo, "agents", "sample")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundle, "agent.yaml")
	if err := os.WriteFile(path, []byte("name: sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runBundleStatusGit(t, git, repo, "init", "--quiet")
	runBundleStatusGit(t, git, repo, "add", "--all")
	runBundleStatusGit(t, git, repo, "commit", "--quiet", "-m", "deployed")
	commit := runBundleStatusGit(t, git, repo, "rev-parse", "HEAD")
	// Same content, moved timestamp: the state in which git rewrites stat
	// data unless told not to.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, note := consoleBundleDiff(t.Context(), bundle, commit); strings.Contains(note, "could not") {
		t.Fatalf("git did not run, so this test would prove nothing: %q", note)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the diff rewrote .git/index")
	}
}

// A long diff is cut short, and the command the note offers must actually
// show the rest from where the console runs, not an empty diff.
func TestConsoleBundleCutShortNoteNamesACommandThatWorks(t *testing.T) {
	a, root, _, _, name, _ := consoleBundleBehind(t, true, strings.Repeat("# filler line\n", consoleBundleDiffMaxLines+50))
	// Make the displayed bundle path require shell quoting.
	quotedRoot := filepath.Join(filepath.Dir(root), "bundles with 'quotes'")
	if err := os.Rename(root, quotedRoot); err != nil {
		t.Fatal(err)
	}
	root = quotedRoot
	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow(name), root)
	if len(snapshot.Diff) != consoleBundleDiffMaxLines {
		t.Fatalf("diff has %d lines, want it cut at %d", len(snapshot.Diff), consoleBundleDiffMaxLines)
	}
	dir := filepath.Join(root, name)
	prefix := "Run git -C " + shellArg(dir) + " diff "
	start := strings.Index(snapshot.DiffNote, prefix)
	if start < 0 {
		t.Fatalf("note = %q", snapshot.DiffNote)
	}
	fields := strings.Fields(snapshot.DiffNote[start+len(prefix):])
	if len(fields) < 3 || fields[1] != "--" || fields[2] != "agent.yaml" || !consoleBundleCommitRE.MatchString(fields[0]) {
		t.Fatalf("note does not use a full SHA: %q", snapshot.DiffNote)
	}
	out, err := exec.Command("sh", "-c", strings.TrimSuffix(strings.TrimPrefix(snapshot.DiffNote, "The diff was cut short. Run "), " to see all of it.")).Output()
	if err != nil || !strings.Contains(string(out), "+# filler line") {
		t.Fatalf("the suggested command did not show the diff: %v %q", err, out)
	}
}

// Screening only the raw line would miss a token split by an escape sequence,
// which removing the sequence then rejoins. Every shape, split in the middle.
func TestConsoleBundleDiffWithholdsATokenSplitByAControlSequence(t *testing.T) {
	for _, shape := range secretshapes.All() {
		for name, splitter := range map[string]string{"sgr": "\x1b[0m", "del": "\x7f", "bell": "\x07"} {
			t.Run(shape.Name+"/"+name, func(t *testing.T) {
				half := len(shape.Example) / 2
				split := shape.Example[:half] + splitter + shape.Example[half:]
				lines := consoleBundleDiffLines("@@ -1 +1 @@\n+  note: " + split + "\n")
				if strings.Contains(strings.Join(lines, "\n"), shape.Example) {
					t.Fatalf("%s was rejoined and drawn", shape.What)
				}
				if !bundleDiffHas(lines, "+ "+consoleBundleWithheld) {
					t.Fatalf("lines = %q", lines)
				}
			})
		}
	}
}

// Closing or reloading the pane cancels the read it no longer needs, instead
// of letting it run its kubectl and git calls to completion.
func TestConsoleBundleCancelsAReadItNoLongerNeeds(t *testing.T) {
	for name, key := range map[string]tea.KeyPressMsg{
		"close":  {Code: tea.KeyEsc},
		"reload": {Code: 'r', Text: "r"},
	} {
		t.Run(name, func(t *testing.T) {
			m := newAgentTUIModel(AgentTUIOptions{Demo: true})
			m.opt.Demo = false
			m.loadBundle = func(ctx context.Context, _ agentTUIEnvironment, _ agentTUIAgent) consoleBundleSnapshot {
				<-ctx.Done()
				return consoleBundleSnapshot{Err: "cancelled"}
			}
			next, cmd := m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
			m = next.(agentTUIModel)
			done := make(chan tea.Msg, 1)
			go func() { done <- cmd() }()
			next, _ = m.Update(key)
			m = next.(agentTUIModel)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the abandoned read was not cancelled")
			}
			m.bundle.stop() // the reload's own read, never started here
		})
	}
}

// Reload compares the pane's own agent. An inventory refresh that lands
// behind the pane can move the selection to a neighbour, and reloading must
// not silently switch to it.
func TestConsoleBundleReloadKeepsThePanesOwnAgent(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.opt.Demo = false
	var loaded []string
	m.loadBundle = func(_ context.Context, _ agentTUIEnvironment, agent agentTUIAgent) consoleBundleSnapshot {
		loaded = append(loaded, agent.Name)
		return consoleBundleSnapshot{}
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	m = next.(agentTUIModel)
	cmd()
	m.columns[0].Selection = 1 // the refresh moved the selection
	next, cmd = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = next.(agentTUIModel)
	cmd()
	if m.bundle.agent.Name != "assistant" || len(loaded) != 2 || loaded[1] != "assistant" {
		t.Fatalf("reload compared %v, pane shows %q; want assistant both times", loaded, m.bundle.agent.Name)
	}
	m.bundle.stop()
}

// The new entry goes last, so every entry that existed before keeps its
// position in both columns: an operator who reaches an action by position
// still reaches it.
func TestConsoleBundleMenuEntryMovesNoExistingEntry(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	keys := func() string {
		var out []string
		for _, action := range m.agentActions(*m.selected()) {
			out = append(out, action.key)
		}
		return strings.Join(out, " ")
	}
	if got := keys(); got != "i c f t L b" {
		t.Errorf("local actions = %q, want the previous order with b appended", got)
	}
	m.focus = 1
	if got := keys(); got != "i c f t b" {
		t.Errorf("remote actions = %q, want the previous order with b appended", got)
	}
}

// Git reads a path after "--" as a pattern. A bundle directory named x[1]
// would also match a sibling x1, whose changes would then be drawn as this
// agent's. The pane passes the path literally.
func TestConsoleBundleDiffTreatsTheBundlePathLiterally(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	bundle, sibling := filepath.Join(repo, "x[1]"), filepath.Join(repo, "x1")
	for _, dir := range []string{bundle, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("name: "+filepath.Base(dir)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runBundleStatusGit(t, git, repo, "init", "--quiet")
	runBundleStatusGit(t, git, repo, "add", "--all")
	runBundleStatusGit(t, git, repo, "commit", "--quiet", "-m", "deployed")
	commit := runBundleStatusGit(t, git, repo, "rev-parse", "HEAD")
	for _, dir := range []string{bundle, sibling} {
		if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("name: edited "+filepath.Base(dir)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lines, note := consoleBundleDiff(t.Context(), bundle, commit)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "+name: edited x[1]") {
		t.Fatalf("the bundle's own change is missing: %q %q", lines, note)
	}
	if strings.Contains(joined, "x1\n") || strings.Contains(joined, "edited x1") {
		t.Fatalf("a sibling directory's change was drawn as this bundle's:\n%s", joined)
	}
}
