package app

// The workflow driver's step kinds, driven end to end against a fake
// plane and a fake agent.
//
// The selector and summary properties are pinned by pure functions
// elsewhere. These tests are about the DRIVERS: what pollStep,
// boundedStep and turnStep do with what the agent and the plane actually
// say. Each one is the difference between a run that reports a result
// and a run that refuses to, so none of them may be exercised by nothing.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/blueprint"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// A fake kubectl for the driver tests. It answers the four things a step
// reaches the cluster for: the admin bearer, the admin forward, the
// agent's existence and its A2A card, and the controller forward a turn
// is invoked through. Both forwards must announce the bind, because
// neither kmx nor the admin package sends a byte before kubectl has said
// which socket it opened.
const fakeDriverKubectl = `#!/bin/sh
case "$*" in
  *"get secret kaimahi-admin"*) printf '%s' "$KMX_TEST_ADMIN_B64"; exit 0 ;;
  *"deploy/kaimahi-proxy"*)
    printf 'Forwarding from 127.0.0.1:%s -> 9091\n' "$KMX_TEST_ADMIN_PORT"
    exec sleep 30 ;;
  *"svc/kagent-controller"*)
    printf 'Forwarding from 127.0.0.1:%s -> 8083\n' "$KMX_TEST_CHAT_PORT"
    exec sleep 30 ;;
  *"get agents.kagent.dev "*) printf 'agent.kagent.dev/hello-world\n'; exit 0 ;;
  *"get --raw"*) printf '{"name":"hello-world"}\n'; exit 0 ;;
esac
exit 0
`

// A fake kagent CLI. It records the task text of every turn and answers
// with the reply queued for that turn — an agent whose answer changes
// between turns is the whole subject of a poll step, and a fake that
// always said the same thing could not tell a finished build from a
// running one.
const fakeKagent = `#!/bin/sh
n=$(cat "$KMX_TEST_TURNS" 2>/dev/null || echo 0)
n=$((n + 1))
printf '%s' "$n" > "$KMX_TEST_TURNS"
task=""
while [ $# -gt 0 ]; do
  case "$1" in --task) task="$2" ;; esac
  shift
done
printf '%s' "$task" > "$KMX_TEST_PROMPTS/$n"
if [ -f "$KMX_TEST_REPLIES/$n" ]; then
  cat "$KMX_TEST_REPLIES/$n"
else
  cat "$KMX_TEST_REPLIES/default"
fi
exit "${KMX_TEST_KAGENT_EXIT:-0}"
`

// fakePlane answers the admin reads a step makes.
//
// The tool audit is served as a QUEUE of pages rather than one fixed
// answer, because the reads a bounded step makes are meant to straddle a
// call: one before, one after. A static audit view can only express "the
// call left no row", which is one of the four answers under test and not
// the others.
type fakePlane struct {
	mu                sync.Mutex
	auditPages        [][]map[string]any
	auditReads        int
	grants            []map[string]any
	pending           []map[string]any
	requests          int
	grantAfterRequest []map[string]any
	approveOnRead     bool
	// asked counts every admin call that got past the liveness and
	// version probes, so a test can say that a step reached the plane
	// for nothing at all.
	asked int
}

func (p *fakePlane) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked++
	switch r.URL.Path {
	case "/admin/tool-audit":
		i := p.auditReads
		p.auditReads++
		if i >= len(p.auditPages) {
			i = len(p.auditPages) - 1
		}
		entries := []map[string]any{}
		if i >= 0 {
			entries = p.auditPages[i]
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	case "/admin/grants":
		grants := p.grants
		if p.requests > 0 && p.grantAfterRequest != nil {
			grants = p.grantAfterRequest
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"grants": grants})
	case "/admin/approvals":
		writeFakeJSON(w, http.StatusOK, map[string]any{"pending": p.pending})
		if p.approveOnRead {
			p.pending = nil
		}
	case "/admin/requests":
		p.requests++
		writeFakeJSON(w, http.StatusCreated, map[string]any{"deduped": false})
	default:
		http.NotFound(w, r)
	}
}

// asks is how many admin calls the plane has answered.
func (p *fakePlane) asks() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked
}

func writeFakeJSON(w http.ResponseWriter, status int, doc map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

// auditEntry is one row of the plane's tool audit, in the shape the
// driver reads. `detail` is the plane's own word for WHY, and it is the
// field a bounded step's verdict turns on.
func auditEntry(created, tool, decision, detail string) map[string]any {
	return map[string]any{
		"created_at": created, "tool": tool,
		"decision": decision, "detail": detail, "arg_summary": "",
	}
}

type driverFixture struct {
	app     *App
	plane   *fakePlane
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	dir     string
	replies string
	prompts string
	turns   string
}

// newDriverFixture stands up everything one step reaches: a kubectl, a
// kagent CLI, and a plane on a real HTTP server behind a real forward.
func newDriverFixture(t *testing.T, plane *fakePlane) *driverFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	replies := filepath.Join(dir, "replies")
	prompts := filepath.Join(dir, "prompts")
	for _, d := range []string{binDir, replies, prompts} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kubectl := filepath.Join(binDir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(fakeDriverKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	kagent := filepath.Join(binDir, "kagent")
	if err := os.WriteFile(kagent, []byte(fakeKagent), 0o755); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(planePreamble(admin.Speaks, plane.handle))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	f := &driverFixture{
		plane: plane, out: &bytes.Buffer{}, errOut: &bytes.Buffer{},
		dir: dir, replies: replies, prompts: prompts,
		turns: filepath.Join(dir, "turns"),
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ADMIN_B64", base64.StdEncoding.EncodeToString([]byte("admin-bearer")))
	t.Setenv("KMX_TEST_ADMIN_PORT", u.Port())
	// The controller forward is announced but never connected to: the
	// fake kagent ignores the URL it is given. A fixed port keeps the
	// announcement kmx waits for predictable.
	t.Setenv("KMX_TEST_CHAT_PORT", "18083")
	t.Setenv("KMX_TEST_REPLIES", replies)
	t.Setenv("KMX_TEST_PROMPTS", prompts)
	t.Setenv("KMX_TEST_TURNS", f.turns)

	cfg := &config.Config{
		KindCluster: "no-such-cluster-kmx-test",
		// A context that does not exist. Every kubectl these tests make
		// is answered by the fake on PATH, so the name is never
		// resolved — and if one ever escaped the fake it would fail
		// cleanly rather than aim at whatever cluster the machine's
		// kubeconfig happens to be pointing at.
		KubeContext: "kind-no-such-cluster-kmx-test", ContextSource: config.SourceKubeCtx,
		AdminPort:     u.Port(),
		ChatPort:      "18083",
		Credential:    "release-agent",
		KagentBin:     kagent,
		KagentVersion: config.DefaultKagentVersion,
	}
	r := run.Default()
	r.Echo = false
	r.Stdout, r.Stderr = f.out, f.errOut
	f.app = &App{Cfg: cfg, Run: r, Out: f.out, Err: f.errOut}
	return f
}

// reply queues what the agent says on turn n (1-based); turn 0 queues the
// answer every unqueued turn gets.
func (f *driverFixture) reply(t *testing.T, turn int, state, text string) {
	t.Helper()
	task := map[string]any{
		"status":    map[string]any{"state": state},
		"artifacts": []any{map[string]any{"parts": []any{map[string]any{"kind": "text", "text": text}}}},
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	name := "default"
	if turn > 0 {
		name = strconv.Itoa(turn)
	}
	if err := os.WriteFile(filepath.Join(f.replies, name), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// turnCount is how many times the agent was actually invoked. A driver
// that reported an outcome without asking anybody is the failure this
// number catches.
func (f *driverFixture) turnCount(t *testing.T) int {
	t.Helper()
	body, err := os.ReadFile(f.turns)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatalf("turn counter is %q", body)
	}
	return n
}

// promptFor is the task text the agent was given on turn n.
func (f *driverFixture) promptFor(t *testing.T, n int) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.prompts, strconv.Itoa(n)))
	if err != nil {
		t.Fatalf("turn %d never happened: %v", n, err)
	}
	return string(body)
}

// runner opens the admin session and builds the run a driver method is
// called on — the same struct RunWorkflow builds, minus the blueprint
// load, which these tests supply directly.
func (f *driverFixture) runner(t *testing.T) *workflowRun {
	t.Helper()
	client, err := admin.Open(f.app, f.app.Cfg.AdminPort, f.app.Err)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	bundle := &blueprint.Bundle{Blueprint: &blueprint.Blueprint{
		Version: blueprint.Version, Name: "driver-test",
		Summary: "a workflow that exists only to drive the step kinds",
		// The credential every audit and grant read is keyed by. The
		// name is a fixture's, not anybody's.
		Credential: "release-agent",
		Agent:      "hello-world",
	}}
	dir := t.TempDir()
	return &workflowRun{
		app: f.app, bundle: bundle, client: client,
		rendered: &blueprint.Rendered{},
		captures: map[string]string{}, files: map[string]string{},
		dir: dir,
	}
}

// pollStepSpec is a poll step with the smallest bounds that still poll:
// one turn, then one interval, then the deadline. A driver test must not
// cost a build's worth of wall clock to find out what it does.
func pollStepSpec(done, failed string) blueprint.RenderedStep {
	return blueprint.RenderedStep{
		Name: "watch", Kind: blueprint.KindPoll, Label: "watch the builds",
		Prompt: "Report the state of the builds.",
		Poll: &blueprint.PollSpec{
			IntervalSeconds: 1, TimeoutSeconds: 1, Done: done, Failed: failed,
		},
	}
}

// TestThePollDriverStopsOnlyWhenTheMarkerIsOnALineOfItsOwn.
//
// The prompt asks the agent to end with the marker on its own line, and
// the driver's answer to "is the build finished" is that line and
// nothing else. An agent that quotes the instruction back — "I will
// print STATE: DONE when it finishes" — is still waiting, and a driver
// that read that as an answer would report a running build as a finished
// one and let the next step publish it.
func TestThePollDriverStopsOnlyWhenTheMarkerIsOnALineOfItsOwn(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	f.reply(t, 0, "completed", "Two runs succeeded.\nSTATE: DONE\n")
	r := f.runner(t)

	if err := r.pollStep(pollStepSpec("STATE: DONE", "STATE: FAILED")); err != nil {
		t.Fatalf("a marker on its own line did not end the poll: %v", err)
	}
	if n := f.turnCount(t); n != 1 {
		t.Fatalf("the poll took %d turns; the first answer already said DONE", n)
	}
	if !strings.Contains(f.errOut.String(), "every build finished successfully") {
		t.Fatalf("the driver did not report the finished build:\n%s", f.errOut.String())
	}
}

// TestThePollDriverKeepsWaitingWhenTheAgentOnlyQuotesTheMarker is the
// same property from the other side, and the one that matters: the
// quoted marker must produce the give-up message, not a result. The
// driver may say "I do not know yet"; it may not say "finished" or
// "failed".
func TestThePollDriverKeepsWaitingWhenTheAgentOnlyQuotesTheMarker(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	f.reply(t, 0, "completed", "I will print STATE: DONE when it finishes, or STATE: FAILED if it breaks.\nStill building.")
	r := f.runner(t)

	err := r.pollStep(pollStepSpec("STATE: DONE", "STATE: FAILED"))
	if err == nil {
		t.Fatal("the agent quoting the instruction was read as a finished build")
	}
	for _, want := range []string{"still running after 1s", "resume with --step watch"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the driver gave up with %q; want it to contain %q", err, want)
		}
	}
	if strings.Contains(f.errOut.String(), "every build finished successfully") {
		t.Fatalf("the driver reported an outcome it did not have:\n%s", f.errOut.String())
	}
	if strings.Contains(err.Error(), "a build failed") {
		t.Fatalf("a build that is still running was reported as failed: %v", err)
	}
}

// TestThePollDriverGivesUpWithoutReportingEitherOutcome. A poll that runs
// out of time knows nothing: the build is neither finished nor failed,
// and the honest answer names the wait that expired and the flag that
// resumes it. A driver that returned nil here would let the publish step
// attach builds that were still running.
func TestThePollDriverGivesUpWithoutReportingEitherOutcome(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	f.reply(t, 0, "completed", "Both runs are in progress.")
	r := f.runner(t)

	err := r.pollStep(pollStepSpec("STATE: DONE", "STATE: FAILED"))
	if err == nil {
		t.Fatal("a poll that timed out returned success")
	}
	if !strings.Contains(err.Error(), "still running after 1s") {
		t.Fatalf("the give-up message was %q", err)
	}
	if f.turnCount(t) == 0 {
		t.Fatal("the driver gave up without asking the agent anything")
	}
}

// TestThePollDriverFailsTheStepWhenTheAgentReportsAFailedBuild, and
// reads the log before it does. The failure marker is a result, unlike
// the deadline: the step fails, it says so, and `on_failure` gets one
// more turn so the operator sees WHY in the same output rather than
// having to go and look.
func TestThePollDriverFailsTheStepWhenTheAgentReportsAFailedBuild(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	f.reply(t, 0, "completed", "One run failed.\nSTATE: FAILED\n")
	f.reply(t, 2, "completed", "The compile step could not resolve a dependency.")
	r := f.runner(t)

	s := pollStepSpec("STATE: DONE", "STATE: FAILED")
	s.OnFailure = "Read the failing build's log and summarise it."
	err := r.pollStep(s)
	if err == nil {
		t.Fatal("a failed build was not reported as a failure")
	}
	if !strings.Contains(err.Error(), "a build failed") {
		t.Fatalf("the failure message was %q", err)
	}
	if strings.Contains(err.Error(), "still running") {
		t.Fatalf("a reported failure was described as a timeout: %v", err)
	}
	if n := f.turnCount(t); n != 2 {
		t.Fatalf("the driver took %d turns; the failure turn should be followed by the log-reading turn", n)
	}
	if got := f.promptFor(t, 2); !strings.Contains(got, "Read the failing build's log") {
		t.Fatalf("the second turn asked %q, not the on_failure prompt", got)
	}
}

// boundedStepSpec is one call a standing constraint is supposed to
// admit.
func boundedStepSpec() blueprint.RenderedStep {
	return blueprint.RenderedStep{
		Name: "build", Kind: blueprint.KindBounded, Label: "start the pipeline",
		Prompt: "Start the pipeline.", Upstream: "ado", Tool: "pipelines_write",
		Args:         map[string]string{"action": "run_pipeline", "pipelineId": "41"},
		ArgTypes:     map[string]string{"pipelineId": "int"},
		PolicyFields: []string{"action", "pipelineId"},
	}
}

// TestABoundedStepRefusesACallThePlaneDidNotAdmit. The step's whole
// claim is that the call happened inside a declared bound; a denial is
// the plane saying it did not happen at all, and carrying on would
// report work that was refused.
func TestABoundedStepRefusesACallThePlaneDidNotAdmit(t *testing.T) {
	plane := &fakePlane{auditPages: [][]map[string]any{
		{auditEntry("2026-09-07T10:00:00Z", "pipelines_write", "allowed", "within standing constraint")},
		{auditEntry("2026-09-07T10:05:00Z", "pipelines_write", "denied", "tool call not permitted"),
			auditEntry("2026-09-07T10:00:00Z", "pipelines_write", "allowed", "within standing constraint")},
	}}
	f := newDriverFixture(t, plane)
	f.reply(t, 0, "completed", "The tool refused the call.")
	r := f.runner(t)

	err := r.boundedStep(boundedStepSpec())
	if err == nil {
		t.Fatal("a denied call was reported as a bounded success")
	}
	if !strings.Contains(err.Error(), "was not admitted") {
		t.Fatalf("the refusal was %q", err)
	}
}

// TestABoundedStepRefusesACallAdmittedForSomeOtherReason is the
// distinction the kind exists for. "Allowed" is not the claim — the
// claim is "allowed by the standing bound this blueprint declared". A
// call admitted by an allowlist entry somebody widened, or by a grant
// somebody left live, ran under a posture nobody reviewed, and reporting
// it as bounded would hide exactly the change worth noticing.
func TestABoundedStepRefusesACallAdmittedForSomeOtherReason(t *testing.T) {
	plane := &fakePlane{auditPages: [][]map[string]any{
		{},
		{auditEntry("2026-09-07T10:05:00Z", "pipelines_write", "allowed", "granted")},
	}}
	f := newDriverFixture(t, plane)
	f.reply(t, 0, "completed", "Started the pipeline.")
	r := f.runner(t)

	err := r.boundedStep(boundedStepSpec())
	if err == nil {
		t.Fatal("a call admitted by a grant was accepted as a bounded call")
	}
	for _, want := range []string{"not by the standing bound", "granted"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal was %q; want it to contain %q", err, want)
		}
	}
}

// TestABoundedStepAcceptsTheCallTheStandingConstraintAdmitted — the one
// answer that is a pass, so that the refusals above are not simply a
// driver that refuses everything.
func TestABoundedStepAcceptsTheCallTheStandingConstraintAdmitted(t *testing.T) {
	plane := &fakePlane{auditPages: [][]map[string]any{
		{auditEntry("2026-09-07T09:00:00Z", "pipelines_write", "allowed", "within standing constraint")},
		{auditEntry("2026-09-07T10:05:00Z", "pipelines_write", "allowed", "within standing constraint"),
			auditEntry("2026-09-07T09:00:00Z", "pipelines_write", "allowed", "within standing constraint")},
	}}
	f := newDriverFixture(t, plane)
	f.reply(t, 0, "completed", "Started the pipeline.")
	r := f.runner(t)

	if err := r.boundedStep(boundedStepSpec()); err != nil {
		t.Fatalf("a call the standing constraint admitted was refused: %v", err)
	}
	if !strings.Contains(f.errOut.String(), "Admitted inside the standing bound") {
		t.Fatalf("the driver did not say what it proved:\n%s", f.errOut.String())
	}
}

// TestABoundedStepRefusesWhenTheCallLeftNoRowAtAll. "The plane denied
// it" and "the plane never saw it" are different facts, and the second
// is the one that happens when a seam's credential expired and kagent
// dropped the tool: the agent cannot attempt a tool it cannot see, so
// there is no denial to find. Both must stop the run, and the message
// must not claim a denial that does not exist.
func TestABoundedStepRefusesWhenTheCallLeftNoRowAtAll(t *testing.T) {
	unchanged := []map[string]any{
		auditEntry("2026-09-07T09:00:00Z", "pipelines_write", "allowed", "within standing constraint"),
	}
	plane := &fakePlane{auditPages: [][]map[string]any{unchanged, unchanged}}
	f := newDriverFixture(t, plane)
	f.reply(t, 0, "completed", "There is no pipelines_write tool in my toolset.")
	r := f.runner(t)

	err := r.boundedStep(boundedStepSpec())
	if err == nil {
		t.Fatal("a call that produced no audit row was reported as admitted")
	}
	if !strings.Contains(err.Error(), "produced no tool-audit row at all") {
		t.Fatalf("the refusal was %q", err)
	}
	if strings.Contains(err.Error(), "was not admitted") {
		t.Fatalf("a call the plane never saw was reported as a denial: %v", err)
	}
}

// TestAStepThatCapturesRefusesToCaptureNothing. `capture:` makes the
// agent's reply an input to a later step — the release notes a publish
// step attaches. An empty reply captured as an empty string would
// publish a release with no body and report success; the step has to
// fail instead, and nothing may be recorded as captured.
func TestAStepThatCapturesRefusesToCaptureNothing(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	f.reply(t, 0, "completed", "   \n")
	r := f.runner(t)

	s := blueprint.RenderedStep{
		Name: "notes", Kind: blueprint.KindPropose, Label: "draft the notes",
		Prompt: "Draft the release notes.", Capture: "notes",
	}
	err := r.turnStep(s)
	if err == nil {
		t.Fatal("an empty reply was captured as release notes")
	}
	if _, ok := r.captures["notes"]; ok {
		t.Fatalf("the driver recorded a capture from an empty reply: %q", r.captures["notes"])
	}
	if _, err := os.Stat(filepath.Join(r.dir, "notes.txt")); err == nil {
		t.Fatal("the driver wrote a capture file for a reply it refused")
	}
}

// TestACapturedValueReachesTheNextStepsPrompt. A capture is only worth
// making if a later step can read it, and it is offered two ways: the
// text itself, and a file holding it — the file is what an ungoverned
// action gets, because a release body does not belong on a command line.
func TestACapturedValueReachesTheNextStepsPrompt(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	f.reply(t, 1, "completed", "Fixed the poll marker.\nAdded the audit check.")
	f.reply(t, 2, "completed", "Looks right.")
	r := f.runner(t)

	first := blueprint.RenderedStep{
		Name: "notes", Kind: blueprint.KindPropose, Label: "draft the notes",
		Prompt: "Draft the release notes.", Capture: "notes",
	}
	if err := r.turnStep(first); err != nil {
		t.Fatalf("the draft step failed: %v", err)
	}
	second := blueprint.RenderedStep{
		Name: "review", Kind: blueprint.KindRead, Label: "review the notes",
		Prompt: "Review these notes:\n${capture.notes}\nThey are also in ${capture.notes.file}.",
	}
	if err := r.turnStep(second); err != nil {
		t.Fatalf("the review step failed: %v", err)
	}

	got := f.promptFor(t, 2)
	if !strings.Contains(got, "Fixed the poll marker.") {
		t.Fatalf("the captured text did not reach the next prompt:\n%s", got)
	}
	if strings.Contains(got, "${capture.") {
		t.Fatalf("a capture reference was passed through unresolved:\n%s", got)
	}
	path := filepath.Join(r.dir, "notes.txt")
	if !strings.Contains(got, path) {
		t.Fatalf("the capture file was not named in the prompt:\n%s", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the capture file was not written: %v", err)
	}
	if !strings.Contains(string(body), "Added the audit check.") {
		t.Fatalf("the capture file holds %q", body)
	}
}

// TestEveryStepKindTheVocabularyDeclaresReachesADriver.
//
// The list is read from the blueprint package rather than written out
// here, so a kind added to the vocabulary without a driver fails at this
// test rather than at "internal: no driver for step kind" in front of an
// operator halfway through a release. What each driver then DOES is the
// subject of the tests above; all this asks is that the dispatch found
// one.
func TestEveryStepKindTheVocabularyDeclaresReachesADriver(t *testing.T) {
	if len(blueprint.StepKinds) == 0 {
		t.Fatal("the step vocabulary is empty; this test would prove nothing")
	}
	for _, kind := range blueprint.StepKinds {
		t.Run(kind, func(t *testing.T) {
			plane := &fakePlane{auditPages: [][]map[string]any{{}, {}}}
			f := newDriverFixture(t, plane)
			f.reply(t, 0, "completed", "Done.")
			r := f.runner(t)

			s := blueprint.RenderedStep{
				Name: "step", Kind: kind, Label: "a " + kind + " step",
				Prompt: "Do the thing.", Upstream: "ado", Tool: "pipelines_write",
				Args:         map[string]string{"action": "run_pipeline"},
				PolicyFields: []string{"action"},
				// A zero timeout makes the poll driver reach its
				// deadline without asking anybody, so this test costs
				// no wall clock to prove the dispatch.
				Poll: &blueprint.PollSpec{TimeoutSeconds: 0, IntervalSeconds: 1, Done: "DONE", Failed: "FAILED"},
			}
			err := r.do(s)
			// Most kinds fail against a plane holding no rows, and
			// that is fine: the only answer this test refuses is the
			// one that means no driver ran.
			if err != nil && strings.Contains(err.Error(), "no driver for step kind") {
				t.Fatalf("kind %q reached no driver: %v", kind, err)
			}
		})
	}
}

// TestADryRunStopsBeforeTheStepDoesAnythingAtAll.
//
// `--dry-run` is what an operator runs to see the shape of a workflow
// without creating anything, so the stop has to come before the step
// acts, not after: a bounded step that read the audit and took its turn
// and only then declined to report would have started the pipeline. The
// two kinds with consequences are checked together because they make the
// same promise, and the evidence is that nobody was asked anything — no
// agent turn, and no call to the plane.
//
// This says nothing about WHEN a credential is refreshed relative to the
// stop; the drivers are called directly, so nothing here depends on that
// order.
func TestADryRunStopsBeforeTheStepDoesAnythingAtAll(t *testing.T) {
	for _, tc := range []struct {
		kind string
		step blueprint.RenderedStep
	}{
		{blueprint.KindBounded, boundedStepSpec()},
		{blueprint.KindConsequential, func() blueprint.RenderedStep {
			s := boundedStepSpec()
			s.Kind, s.Name, s.Label = blueprint.KindConsequential, "publish", "publish the release"
			return s
		}()},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			plane := &fakePlane{}
			f := newDriverFixture(t, plane)
			f.reply(t, 0, "completed", "Started the pipeline.")
			r := f.runner(t)
			r.opt.DryRun = true
			// The admin session is opened by the fixture, so anything
			// the plane is asked from here is the step's doing.
			before := plane.asks()

			var err error
			if tc.kind == blueprint.KindBounded {
				err = r.boundedStep(tc.step)
			} else {
				err = r.consequentialStep(tc.step)
			}
			if _, stopped := err.(dryRunStop); !stopped {
				t.Fatalf("a dry run of a %s step returned %v, not the stop the driver reports as \"nothing was created\"", tc.kind, err)
			}
			if n := f.turnCount(t); n != 0 {
				t.Fatalf("a dry run took %d agent turns", n)
			}
			if n := plane.asks() - before; n != 0 {
				t.Fatalf("a dry run made %d admin calls before stopping", n)
			}
		})
	}
}

// TestAStepKindWithNoDriverIsRefusedByName. The dispatch's default is
// not a no-op: a step whose kind nothing handles must stop the run and
// say which kind, because silently skipping it would report a workflow
// as run while part of it never happened.
func TestAStepKindWithNoDriverIsRefusedByName(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	r := f.runner(t)

	err := r.do(blueprint.RenderedStep{Name: "step", Kind: "deploy", Label: "a step of an unknown kind"})
	if err == nil {
		t.Fatal("a step of an unknown kind was silently skipped")
	}
	if !strings.Contains(err.Error(), `no driver for step kind "deploy"`) {
		t.Fatalf("the refusal was %q", err)
	}
	if n := f.turnCount(t); n != 0 {
		t.Fatalf("a step with no driver still invoked the agent %d times", n)
	}
	// Guard against the vocabulary quietly gaining the kind this test
	// uses as its unknown one, which would turn the test into a
	// tautology about a kind that now has a driver.
	for _, kind := range blueprint.StepKinds {
		if kind == "deploy" {
			t.Fatal(`"deploy" is now a declared step kind; pick another unknown one`)
		}
	}
}

func TestConsequentialStepRequiresMatchingRequestGrantAndAuditDigests(t *testing.T) {
	for _, tc := range []struct {
		name, requestDigest, grantDigest, auditDigest, approver, decidedBy string
		wantOK                                                             bool
		wantTurns                                                          int
	}{
		{"matching", "abc", "abc", "abc", "", "", true, 1},
		{"missing request", "", "abc", "abc", "", "", false, 0},
		{"missing grant", "abc", "", "abc", "", "", false, 0},
		{"wrong grant", "abc", "def", "abc", "", "", false, 0},
		{"missing audit", "abc", "abc", "", "", "", false, 1},
		{"wrong audit", "abc", "abc", "def", "", "", false, 1},
		{"required approver", "abc", "abc", "abc", "U123", "U123", true, 1},
		{"wrong approver", "abc", "abc", "abc", "U123", "U456", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := boundedStepSpec()
			s.Kind = blueprint.KindConsequential
			row := auditEntry("now", s.Tool, "allowed", "granted")
			row["arg_digest"] = tc.auditDigest
			p := &fakePlane{
				pending:           []map[string]any{{"id": "request-123", "credential": "release-agent", "kind": "tool", "subject": s.Tool, "arg_summary": s.Summary(), "arg_digest": tc.requestDigest}},
				grantAfterRequest: []map[string]any{{"id": "grant-123", "request_id": "request-123", "kind": "tool", "subject": s.Tool, "live": true, "arg_digest": tc.grantDigest, "decided_by": tc.decidedBy}},
				approveOnRead:     true,
				auditPages:        [][]map[string]any{{}, {row}},
			}
			f := newDriverFixture(t, p)
			f.reply(t, 0, "completed", "Done")
			r := f.runner(t)
			r.opt.HumanSeconds = 1
			r.opt.Approver = tc.approver
			err := r.consequentialStep(s)
			if (err == nil) != tc.wantOK {
				t.Fatalf("error=%v, want success=%v\n%s", err, tc.wantOK, f.errOut.String())
			}
			if got := f.turnCount(t); got != tc.wantTurns {
				t.Fatalf("took %d turns, want %d", got, tc.wantTurns)
			}
			log := f.errOut.String()
			if strings.Contains(log, "carry the same digest") != tc.wantOK {
				t.Fatalf("unjustified or missing digest claim:\n%s", log)
			}
			if tc.requestDigest == "" {
				return
			}
			if tc.approver != "" {
				if !strings.Contains(log, "Required approver: U123") || strings.Contains(log, "Approve it with:") {
					t.Fatalf("approval advice bypassed the identity constraint:\n%s", log)
				}
			} else if !strings.Contains(log, "kmx --context "+f.app.Cfg.KubeContext+" approve request-123 --uses 1 --ttl 10m") {
				t.Fatalf("approval command lost context:\n%s", log)
			}
			if strings.Contains(log, "Slack") || strings.Contains(log, "@kaimahi") {
				t.Fatalf("approval advice suggests a retired notification path:\n%s", log)
			}
		})
	}
}

func TestConsequentialStepDoesNotSuggestDenyingALiveGrant(t *testing.T) {
	s := boundedStepSpec()
	p := &fakePlane{grants: []map[string]any{{"kind": "tool", "subject": s.Tool, "live": true}}}
	f := newDriverFixture(t, p)
	err := f.runner(t).consequentialStep(s)
	if err == nil || strings.Contains(err.Error(), "or deny it") || !strings.Contains(err.Error(), "does not revoke a live grant") {
		t.Fatalf("unsafe live-grant advice: %v", err)
	}
	if p.requests != 0 || f.turnCount(t) != 0 {
		t.Fatal("acted while a previous grant was live")
	}
}

func TestWorkflowTurnRejectsNonzeroExitWithCompletedTask(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	f.reply(t, 0, "completed", "Looks successful")
	t.Setenv("KMX_TEST_KAGENT_EXIT", "7")
	_, err := f.runner(t).turn(blueprint.RenderedStep{Kind: blueprint.KindRead, Label: "read", Prompt: "Read"})
	if err == nil || !strings.Contains(err.Error(), "exited 7") {
		t.Fatalf("nonzero exit was accepted: %v", err)
	}
}

func TestWorkflowResumeRetainsFileParametersContextAndApprover(t *testing.T) {
	f := newDriverFixture(t, &fakePlane{})
	r := f.runner(t)
	r.opt.File = "team's workflow.yaml"
	r.opt.Set = map[string]string{"repo": "org/repo", "branch": "release's $(false)"}
	r.opt.Approver = "U123"
	want := "kmx --context " + f.app.Cfg.KubeContext + " workflow run --file " + shellArg(r.opt.File) +
		" --set " + shellArg("branch="+r.opt.Set["branch"]) + " --set repo=org/repo --step publish --approver U123"
	if got := r.resumeCommand("publish"); got != want {
		t.Fatalf("resume lost invocation settings: %s, want %s", got, want)
	}
}
