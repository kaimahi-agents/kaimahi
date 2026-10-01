// The console's bundle pane: what a live agent is, against what its local
// bundle says it should be.
//
// It adds no comparison of its own. `kmx agent status` already decides the
// state (in sync, behind, drifted and the rest), and this pane asks that same
// report about the one target the selected console row lives on, then draws
// it. A second comparison here could disagree with the command an operator
// would run to check it.
//
// What it adds is the answer status deliberately stops short of for a target
// that is behind: WHAT a lift would change. That diff is read from Git (the
// deployed commit against the working copy of agent.yaml) and never from the
// cluster. Status's own rule is that live values are untrusted and are never
// shown, so a drifted target shows field paths only, exactly as status does.
//
// The pane reads and never writes: no receipt, no remembered selection, no
// cluster object, no Git state.
package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// consoleBundleRootDefault is where `kmx agent create` writes bundles, so it
// is where a console opened from the same repository finds them.
const consoleBundleRootDefault = "agents"

// consoleBundleWithheld replaces a diff line that looks like a credential.
// It names why, so a reader never mistakes it for an empty or deleted line.
const consoleBundleWithheld = "«withheld: credential-shaped»"

// Bounds on what the diff may contribute to a terminal pane. agent.yaml is a
// small document, so hitting either is itself worth saying.
const (
	consoleBundleDiffMaxBytes = 256 << 10
	consoleBundleDiffMaxLines = 2000
)

// consoleBundleCommitRE is the only shape of revision passed to git: a full
// SHA-1 or SHA-256 commit id. Status reports a 7-character abbreviation for
// display, and an abbreviation is not an identity: it can be ambiguous in a
// large repository or resolve to a branch or tag of the same name. So the
// full id is recovered before git is asked for anything. The shape also
// guarantees the argument cannot begin with "-" and be read as an option.
var consoleBundleCommitRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// consoleBundleSnapshot is everything the pane draws, gathered off the UI
// goroutine. Err is set only when no report could be produced at all; every
// per-target outcome, including an unreadable cluster, is the target's own
// State, as it is in `kmx agent status`.
type consoleBundleSnapshot struct {
	Dir    string
	Target *bundleTargetStatus
	// Diff is the screened, terminal-safe body of `git diff` for a target
	// that is behind: hunk headers and +/-/context lines only.
	Diff []string
	// DiffNote explains why a behind target has no diff, or that the diff
	// was cut short. It is empty when Diff is complete.
	DiffNote string
	Err      string
}

type consoleBundlePane struct {
	env      agentTUIEnvironment
	agent    agentTUIAgent
	loading  bool
	snapshot consoleBundleSnapshot
	scroll   int
	// cancel stops this pane's read. Closing or reloading the pane calls it,
	// so an abandoned read stops issuing kubectl and git calls instead of
	// running to completion only to be dropped as stale.
	cancel context.CancelFunc
}

// stop cancels this pane's read, if one is running.
func (p *consoleBundlePane) stop() {
	if p != nil && p.cancel != nil {
		p.cancel()
	}
}

// consoleBundleLoaded carries its own pane so a result that arrives after the
// operator closed or reopened the pane is recognised as stale and dropped.
type consoleBundleLoaded struct {
	pane     *consoleBundlePane
	snapshot consoleBundleSnapshot
}

// consoleAgentBundle resolves the same local Orka bundle for comparison and
// lift. Only an absent agent directory means there is no bundle to lift.
func consoleAgentBundle(root, name string) (string, bool, error) {
	if strings.TrimSpace(root) == "" {
		root = consoleBundleRootDefault
	}
	if err := scaffold.ValidateName(name); err != nil {
		return "", false, fmt.Errorf("agent name %q cannot name a bundle directory", name)
	}
	dir := filepath.Join(root, name)
	if _, err := os.Lstat(dir); err != nil {
		if os.IsNotExist(err) {
			return dir, false, nil
		}
		return dir, false, fmt.Errorf("cannot read local bundle: %w", err)
	}
	resolved, err := resolveOrkaPath(dir)
	if err != nil {
		return dir, false, fmt.Errorf("resolve bundle: %w", err)
	}
	if err := checkLiftBundle(resolved); err != nil {
		return dir, false, err
	}
	actual, _, _, err := readBundlePortableAgent(resolved)
	if err != nil {
		return dir, false, err
	}
	if actual != name {
		return dir, false, fmt.Errorf("%s defines agent %q, not %q; refusing to compare different agents", filepath.Join(dir, "agent.yaml"), actual, name)
	}
	return dir, true, nil
}

// consoleBundleStatus resolves the selected row's local bundle and asks the
// shared status report about exactly this row's context and namespace.
func (a *App) consoleBundleStatus(ctx context.Context, env agentTUIEnvironment, agent agentTUIAgent, root string) consoleBundleSnapshot {
	dir, found, err := consoleAgentBundle(root, agent.Name)
	snapshot := consoleBundleSnapshot{Dir: dir}
	if err != nil {
		snapshot.Err = err.Error()
		return snapshot
	}
	if !found {
		snapshot.Err = fmt.Sprintf("No local bundle at %s. Open the console from the repository that holds it, or pass --bundles <dir>.", filepath.Join(dir, "agent.yaml"))
		return snapshot
	}

	// Aim at this row's environment, including its own kubeconfig, and bind
	// to the console's context so closing the console stops these reads.
	worker := env.app(a).withRunContext(ctx)
	report, err := worker.bundleStatusReport(BundleStatusOptions{BundleDir: dir, Context: env.Name, Namespace: agent.Namespace})
	if err != nil {
		snapshot.Err = err.Error()
		return snapshot
	}
	// The local file may have changed while status assembled its report.
	if report.Bundle != agent.Name {
		snapshot.Err = fmt.Sprintf("%s defines agent %q, not %q; refusing to compare different agents", filepath.Join(dir, "agent.yaml"), report.Bundle, agent.Name)
		return snapshot
	}
	target, ok := consoleBundleTarget(report, env.Name, agent.Namespace)
	if !ok {
		snapshot.Err = fmt.Sprintf("status reported nothing for %s in %s", env.Name, agent.Namespace)
		return snapshot
	}
	snapshot.Target = &target
	if target.State == bundleStateBehind {
		snapshot.Diff, snapshot.DiffNote = consoleBundleBehindDiff(ctx, dir, target)
	}
	return snapshot
}

// consoleBundleBehindDiff uses the full id status already found. It must
// extend the reported abbreviation, or the two disagree about the revision.
func consoleBundleBehindDiff(ctx context.Context, dir string, target bundleTargetStatus) ([]string, string) {
	if target.DeployedCommit == "" {
		return nil, "The deployed revision is not in this repository's recent history of agent.yaml, so there is nothing to diff against."
	}
	if !strings.HasPrefix(target.deployedCommitFull, target.DeployedCommit) {
		return nil, "The deployed commit does not match the reported revision, so no diff is shown."
	}
	return consoleBundleDiff(ctx, dir, target.deployedCommitFull)
}

// consoleBundleTarget picks this row's target from the report. An explicit
// context and namespace can still match several receipts when one context
// has named more than one cluster over time; the one that is not "target
// changed" is the cluster the context names now.
func consoleBundleTarget(report bundleStatusReport, context, namespace string) (bundleTargetStatus, bool) {
	var fallback *bundleTargetStatus
	for i := range report.Targets {
		target := report.Targets[i]
		if target.Context != context || target.Namespace != namespace {
			continue
		}
		if target.State != bundleStateChanged {
			return target, true
		}
		if fallback == nil {
			fallback = &report.Targets[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return bundleTargetStatus{}, false
}

// consoleBundleDiff is `git diff <deployed> -- agent.yaml` against the WORKING
// COPY, because the working copy is what `kmx agent lift` reads. It shows what
// a lift would change, including edits not yet committed.
//
// Both sides come from Git, never from the cluster. The working copy has
// already passed the portable schema's credential refusal (status parsed it),
// and every line is screened again anyway before it can be drawn.
func consoleBundleDiff(ctx context.Context, dir, commit string) ([]string, string) {
	if !consoleBundleCommitRE.MatchString(commit) {
		return nil, "The deployed revision is not a full commit id, so it was not passed to git."
	}
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resolved, err := resolveOrkaPath(dir)
	if err != nil {
		return nil, "Cannot resolve the bundle directory, so no diff is shown."
	}
	rootRaw, err := exec.CommandContext(gitCtx, "git", "-C", resolved, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, "The bundle is not in a Git repository, so no diff is shown."
	}
	root := strings.TrimSpace(string(rootRaw))
	rel, err := filepath.Rel(root, filepath.Join(resolved, "agent.yaml"))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, "agent.yaml is outside the repository Git reports, so no diff is shown."
	}
	// --no-ext-diff and --no-textconv keep git from running a configured
	// program over these files; colour is off because the pane styles the
	// lines itself.
	//
	// diff.autoRefreshIndex=false keeps this a read. Diffing a commit
	// against the working tree otherwise refreshes stat data in .git/index
	// whenever agent.yaml's timestamps moved (a save, a touch, a checkout),
	// taking index.lock to do it, and an operator's own concurrent commit
	// would then fail on that lock. --no-optional-locks does not prevent it.
	//
	// --literal-pathspecs makes the path a path. A directory name holding
	// *, ? or [ would otherwise be a glob that matches other files.
	out, err := exec.CommandContext(gitCtx, "git", "-C", root, "--literal-pathspecs",
		"-c", "color.ui=false", "-c", "diff.autoRefreshIndex=false",
		"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--unified=3", commit, "--", filepath.ToSlash(rel)).Output()
	if err != nil {
		return nil, "git could not produce the diff, so none is shown."
	}
	truncated := false
	if len(out) > consoleBundleDiffMaxBytes {
		out, truncated = out[:consoleBundleDiffMaxBytes], true
	}
	lines := consoleBundleDiffLines(string(out))
	if len(lines) > consoleBundleDiffMaxLines {
		lines, truncated = lines[:consoleBundleDiffMaxLines], true
	}
	if len(lines) == 0 {
		return nil, "Git shows no textual difference in agent.yaml; the revisions differ only in bytes a diff does not display, such as line endings."
	}
	if truncated {
		// -C names the bundle, so the path is right from wherever the
		// console was started.
		return lines, fmt.Sprintf("The diff was cut short. Run git -C %s diff %s -- agent.yaml to see all of it.", shellArg(dir), commit)
	}
	return lines, ""
}

// consoleBundleDiffLines keeps hunks and drops git's file headers, then makes
// every kept line safe to draw: tabs expanded, terminal controls removed, and
// anything credential-shaped withheld behind its marker.
//
// The credential screen runs on the line both before and after control
// characters are removed. Screening only the raw line would miss a token
// split by an escape sequence, which removing the sequence then rejoins.
func consoleBundleDiffLines(raw string) []string {
	var lines []string
	inHunk := false
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunk = true
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '@', '+', '-', ' ', '\\':
		default:
			continue
		}
		marker, body := line[:1], line[1:]
		clean := safeTerminal(strings.ReplaceAll(body, "\t", "    "))
		if secretshapes.Match(body) != nil || secretshapes.Match(clean) != nil {
			lines = append(lines, marker+" "+consoleBundleWithheld)
			continue
		}
		lines = append(lines, marker+clean)
	}
	return lines
}

func (m agentTUIModel) openBundle() (tea.Model, tea.Cmd) {
	agent := m.selected()
	if agent == nil {
		return m, nil
	}
	if agent.External {
		m.status = "Bundles describe native Orka agents; this agent uses an external runtime"
		return m, nil
	}
	return m.openBundleFor(m.columns[m.focus].Env, *agent)
}

// openBundleFor opens a pane for one named environment and agent. Reload
// calls it with the pane's own pair rather than the current selection, which
// an inventory refresh landing behind the pane may have moved to another row.
func (m agentTUIModel) openBundleFor(env agentTUIEnvironment, agent agentTUIAgent) (tea.Model, tea.Cmd) {
	m.bundle.stop()
	p := &consoleBundlePane{env: env, agent: agent, loading: true}
	m.bundle = p
	if m.opt.Demo {
		p.loading, p.snapshot = false, agentTUIDemoBundle(p.env, p.agent)
		return m, nil
	}
	if m.loadBundle == nil {
		p.loading, p.snapshot = false, consoleBundleSnapshot{Err: "bundle comparison is unavailable in this session"}
		return m, nil
	}
	// A child of the console session, so quitting the console still stops
	// it, and closing this one pane stops it too.
	parent := m.inferenceContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	load := m.loadBundle
	return m, func() tea.Msg { return consoleBundleLoaded{p, load(ctx, p.env, p.agent)} }
}

func (m agentTUIModel) updateBundle(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.bundle
	lines := p.lines(m.bundleInnerWidth())
	page := m.bundlePage()
	maxScroll := max(0, len(lines)-page)
	switch key.String() {
	case "esc", "q", "enter":
		p.stop()
		m.bundle = nil
		return m, nil
	case "r":
		// A fresh pane identity makes any read still in flight stale, and
		// openBundleFor cancels it.
		return m.openBundleFor(p.env, p.agent)
	case "j", "down":
		p.scroll++
	case "k", "up":
		p.scroll--
	case "pgdown", "ctrl+d":
		p.scroll += page
	case "pgup", "ctrl+u":
		p.scroll -= page
	case "home", "g":
		p.scroll = 0
	case "end", "G":
		p.scroll = maxScroll
	}
	p.scroll = max(0, min(p.scroll, maxScroll))
	return m, nil
}

func (m agentTUIModel) bundleWidth() int      { return min(96, max(24, m.width-6)) }
func (m agentTUIModel) bundleInnerWidth() int { return m.bundleWidth() - 4 }
func (m agentTUIModel) bundlePage() int       { return max(1, min(40, m.height-4)-4) }

func (m agentTUIModel) bundleView() string {
	p := m.bundle
	width, inner := m.bundleWidth(), m.bundleInnerWidth()
	background := lipgloss.Color("#18232D")
	fit := func(s string) string { return ansi.Truncate(tuiOneLine(s), inner, "…") }
	title := lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black).Bold(true).Width(inner).
		Render(fit("BUNDLE · " + p.agent.Name))
	lines := p.lines(inner)
	page := m.bundlePage()
	start := max(0, min(p.scroll, max(0, len(lines)-page)))
	end := min(len(lines), start+page)
	rows := append([]string{title}, lines[start:end]...)
	rows = append(rows, agentTUIKeyHints(fit(fmt.Sprintf("↑/↓ scroll · r reload · <esc> / <enter> close · %d/%d", end, len(lines)))))
	return lipgloss.NewStyle().Width(width).Padding(0, 1).Background(background).Foreground(lipgloss.Color("#E6EDF3")).
		Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Cyan).BorderBackground(background).Render(strings.Join(rows, "\n"))
}

// consoleBundleStateStyle colours a state by what it asks of the operator.
// The state word is always printed too, so colour never carries the meaning
// alone.
func consoleBundleStateStyle(state string) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	switch state {
	case bundleStateInSync:
		return style.Foreground(lipgloss.Green)
	case bundleStateDrifted, bundleStateForeign, bundleStateChanged:
		return style.Foreground(lipgloss.Red)
	case bundleStateBehind, bundleStateUnknown:
		return style.Foreground(lipgloss.Yellow)
	}
	return style
}

func (p *consoleBundlePane) lines(inner int) []string {
	inner = max(1, inner)
	label := lipgloss.NewStyle().Foreground(lipgloss.Cyan).Bold(true)
	value := lipgloss.NewStyle().Foreground(lipgloss.Color("#E6EDF3"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#93A4B5"))
	var rows []string
	field := func(key, text string, style lipgloss.Style) {
		prefix := key + ": "
		indent := min(lipgloss.Width(prefix), inner/2)
		for i, line := range strings.Split(ansi.Wrap(tuiOneLine(text), max(1, inner-indent), ""), "\n") {
			if i == 0 {
				rows = append(rows, label.Render(prefix)+style.Render(line))
			} else {
				rows = append(rows, strings.Repeat(" ", indent)+style.Render(line))
			}
		}
	}
	text := func(s string, style lipgloss.Style, indent int) {
		for _, line := range agentTUIIndentedText(s, inner, indent) {
			rows = append(rows, style.Render(line))
		}
	}

	field("Environment", p.env.Name+" · "+p.agent.Namespace, value)
	if p.snapshot.Dir != "" {
		field("Bundle", p.snapshot.Dir, value)
	}
	if p.loading {
		rows = append(rows, "")
		text("Reading the bundle and the cluster…", muted, 0)
		return rows
	}
	if p.snapshot.Err != "" {
		rows = append(rows, "")
		text(p.snapshot.Err, value, 0)
		return rows
	}
	t := p.snapshot.Target
	if t == nil {
		return rows
	}
	state := t.State
	if t.Detail != "" {
		state += " · " + t.Detail
	}
	field("State", state, consoleBundleStateStyle(t.State))
	switch {
	case t.DeployedCommit != "" && t.Behind > 0:
		field("Deployed", fmt.Sprintf("%s · %d commit(s) behind HEAD", t.DeployedCommit, t.Behind), value)
	case t.DeployedCommit != "":
		field("Deployed", t.DeployedCommit, value)
	case t.GitNote != "":
		field("Deployed", t.GitNote, muted)
	}
	if t.Provider.Found || t.Agent.Found {
		field("Ready", fmt.Sprintf("Provider %s · Agent %s", yesNo(t.Provider.Ready), yesNo(t.Agent.Ready)), value)
	}
	rows = append(rows, "")

	file := filepath.Join(p.snapshot.Dir, "agent.yaml")
	switch t.State {
	case bundleStateInSync:
		text("What is running matches "+file+".", value, 0)
		text("A matching definition is not proof the agent answers; run a Task or evaluation for that.", muted, 0)
	case bundleStateBehind:
		rows = append(rows, label.Render("What kmx agent lift would change"))
		if t.DeployedCommit != "" {
			text(t.DeployedCommit+" → your working copy of "+file, muted, 0)
		}
		if len(p.snapshot.Diff) > 0 {
			rows = append(rows, "")
			rows = append(rows, consoleBundleDiffView(p.snapshot.Diff, inner)...)
		}
		if p.snapshot.DiffNote != "" {
			rows = append(rows, "")
			text(p.snapshot.DiffNote, muted, 0)
		}
	case bundleStateDrifted:
		text("These live fields changed after the last lift:", value, 0)
		for _, path := range t.ChangedFields {
			text("• "+path, value, 2)
		}
		rows = append(rows, "")
		text("Values are not shown: live objects are untrusted and may hold credentials. Lifting "+p.snapshot.Dir+" again re-applies the definition.", muted, 0)
	case bundleStateNotDeployed:
		text("Nothing named "+p.agent.Name+" is deployed in "+p.agent.Namespace+" on this cluster. Lift the bundle to deploy it.", value, 0)
	case bundleStateForeign:
		text("The objects named "+p.agent.Name+" here belong to a different bundle, so they were not compared.", value, 0)
	case bundleStateChanged:
		text("This context now names a different cluster from the one this bundle was lifted to, so nothing was compared.", value, 0)
	case bundleStateUnknown:
		text("No verdict was reached. That is not the same as in sync.", value, 0)
		if t.Detail == "missing ownership marker" {
			text("Objects created before ownership markers existed are not bundle-owned; lifting the bundle marks them.", muted, 0)
		}
	}
	return rows
}

// consoleBundleDiffView colours each line by its diff marker and wraps long
// lines, carrying the colour onto continuations so a wrapped removal never
// reads as context.
func consoleBundleDiffView(diff []string, inner int) []string {
	added := lipgloss.NewStyle().Foreground(lipgloss.Green)
	removed := lipgloss.NewStyle().Foreground(lipgloss.Red)
	hunk := lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	context := lipgloss.NewStyle().Foreground(lipgloss.Color("#93A4B5"))
	var rows []string
	for _, line := range diff {
		style := context
		switch {
		case strings.HasPrefix(line, "@@"):
			style = hunk
		case strings.HasPrefix(line, "+"):
			style = added
		case strings.HasPrefix(line, "-"):
			style = removed
		}
		for _, part := range strings.Split(ansi.Hardwrap(line, max(1, inner), true), "\n") {
			rows = append(rows, style.Render(part))
		}
	}
	return rows
}

func yesNo(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}

// agentTUIDemoBundle gives each demo agent a different state, so the demo
// shows the pane's cases without a cluster, Git or files. Every value is
// invented here, rather than half-read from the demo row, so no case mixes
// sample data with made-up data.
func agentTUIDemoBundle(env agentTUIEnvironment, agent agentTUIAgent) consoleBundleSnapshot {
	dir := filepath.Join(consoleBundleRootDefault, agent.Name)
	ready := bundleResourceStatus{Found: true, Ready: true, Marked: true}
	base := bundleTargetStatus{Context: env.Name, Namespace: agent.Namespace, Provider: ready, Agent: ready}
	switch {
	case env.Local && agent.Name == "assistant":
		base.State, base.DeployedCommit, base.Behind = bundleStateBehind, "3f9c2e1", 2
		return consoleBundleSnapshot{Dir: dir, Target: &base, Diff: []string{
			"@@ -4,5 +4,7 @@ metadata:",
			" spec:",
			"-  instructions: You are a helpful cluster assistant.",
			"+  instructions: |-",
			"+    You are a careful cluster assistant.",
			"+    Inspect resources before answering.",
			"   model:",
			"     name: qwen3:8b",
			" extensions:",
			"@@ -16,2 +18,3 @@ extensions:",
			"       tools:",
			"         - name: k8s-get-resources",
			"+        - name: web-fetch",
		}}
	case env.Local:
		// Objects created before ownership markers existed: found and
		// ready, but not bundle-owned.
		base.State, base.Detail = bundleStateUnknown, "missing ownership marker"
		base.Provider.Marked, base.Agent.Marked = false, false
		return consoleBundleSnapshot{Dir: dir, Target: &base}
	default:
		base.State, base.DeployedCommit = bundleStateDrifted, "3f9c2e1"
		base.ChangedFields = []string{"Agent.spec.systemPrompt.inline", "Provider.spec.defaultModel"}
		return consoleBundleSnapshot{Dir: dir, Target: &base}
	}
}
