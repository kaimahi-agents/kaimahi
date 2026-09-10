package app

// The driver's safety properties, tested where they can be tested
// without a cluster. The rest — that a denial is really a denial, that a
// grant is really welded to a digest — is the plane's, and CI exercises
// it against the synthetic hosted upstream with no credential anywhere:
// this repository is public and fork-exposed, so CI holds no hosted
// credential of any kind.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/blueprint"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestWorkflowShowOnlyTreatsMissingBindingsAsExploratory(t *testing.T) {
	for _, tc := range []struct {
		name          string
		set           map[string]string
		invalid, help bool
	}{
		{"no parameters", nil, false, true},
		{"valid partial", map[string]string{"version": "v1.2.3"}, false, true},
		{"missing dependency", map[string]string{"repo": "org/repo", "ado_pipelines": "41"}, false, true},
		{"unknown", map[string]string{"typo": "value"}, true, false},
		{"invalid repo", map[string]string{"repo": "invalid"}, true, false},
		{"invalid list and missing", map[string]string{"ado_pipelines": "bad"}, true, false},
		{"valid review", map[string]string{"repo": "org/repo"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			a := &App{Out: &out}
			err := a.ShowWorkflow("release", WorkflowOptions{Set: tc.set})
			if tc.invalid {
				var binding *blueprint.BindingError
				if !errors.As(err, &binding) || !binding.Invalid || out.Len() != 0 {
					t.Fatalf("invalid review returned %v and output %s", err, out.String())
				}
			} else if err != nil || !strings.Contains(out.String(), "PARAMETERS") || (tc.help && strings.Contains(out.String(), "STEPS\n")) {
				t.Fatalf("valid/exploratory review returned %v and output %s", err, out.String())
			}
		})
	}
}

// TestTheDriverWaitsForTheRequestItFiledAndNotAnotherOne pins, at the
// selector, the property the accounts-payable demo paid for.
//
// The driver files a request for the call the operator's parameters
// name. If the agent's own turn had filed a request too — for a
// different branch, at the same tool — the two would sit side by side in
// `kmx approvals`. What tells them apart is the summary, which names
// every policy-relevant field. A selector that matched on tool name, or
// on position, would hand a human the wrong one.
func TestTheDriverWaitsForTheRequestItFiledAndNotAnotherOne(t *testing.T) {
	mine := "create_branch: owner Contoso, repo widget, branch release/v1.2.3, from_branch main"
	theirs := "create_branch: owner Contoso, repo widget, branch release/v9.9.9, from_branch main"
	requests := []map[string]any{
		// The agent's, filed first, and it must not be chosen.
		{"id": "aaaa", "credential": "release-agent", "kind": "tool", "subject": "create_branch", "arg_summary": theirs},
		{"id": "bbbb", "credential": "release-agent", "kind": "tool", "subject": "create_branch", "arg_summary": mine},
	}
	if got := selectRequest(requests, "release-agent", "create_branch", mine); got != "bbbb" {
		t.Fatalf("selected %q; the request naming this exact call is bbbb", got)
	}
	// And when only the other one is there, nothing is selected. Picking
	// the "closest" would be picking a call nobody described.
	if got := selectRequest(requests[:1], "release-agent", "create_branch", mine); got != "" {
		t.Fatalf("selected %q for a call that was never filed", got)
	}
	// Another credential's request for the same call is not this one.
	other := []map[string]any{
		{"id": "cccc", "credential": "ap-agent", "kind": "tool", "subject": "create_branch", "arg_summary": mine},
	}
	if got := selectRequest(other, "release-agent", "create_branch", mine); got != "" {
		t.Fatalf("selected another credential's request: %q", got)
	}
}

// TestARunThatDidNothingNamesTheFlagThatWouldHaveRunSomething.
//
// `--step publish` with no --set ado_builds asks for a step whose `when:`
// guard nobody met, and the honest answer is that nothing ran. "Nothing
// ran" is only usable if it says what would have made something run —
// the fix turns a refusal-to-start into an empty run, and an empty run
// that did not name the flag would be the same dead end wearing a
// different message.
func TestARunThatDidNothingNamesTheFlagThatWouldHaveRunSomething(t *testing.T) {
	b, err := blueprint.Load(kaimahi.Blueprints, "release")
	if err != nil {
		t.Fatal(err)
	}
	got := guardList(b, []string{"publish", "build-ado"})
	for _, want := range []string{"publish — needs --set ado_builds=…", "build-ado — needs --set ado_pipelines=…"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the driver said:\n%s\nwant a line reading %q", got, want)
		}
	}
	// A step with no guard has no flag to name, and inventing one would
	// be worse than listing what was asked for.
	if got := guardList(b, []string{"cut"}); !strings.Contains(got, "Steps: cut") {
		t.Fatalf("for an unguarded step the driver said: %s", got)
	}
}

// TestTheSummaryReadsTheDeclaredFieldsInTheDeclaredOrder keeps the
// driver's selector and the plane's audit line in step. The order is the
// table's, not the blueprint's, because the audit renders the declared
// order and that is what the driver has to match on.
func TestTheSummaryReadsTheDeclaredFieldsInTheDeclaredOrder(t *testing.T) {
	s := blueprint.RenderedStep{
		Tool:         "actions_run_trigger",
		PolicyFields: []string{"method", "owner", "repo", "workflow_id", "ref"},
		Args: map[string]string{
			"ref": "release/v1.2.3", "owner": "Contoso", "method": "run_workflow",
			"workflow_id": "build.yml", "repo": "widget",
		},
	}
	want := "actions_run_trigger: method run_workflow, owner Contoso, repo widget, workflow_id build.yml, ref release/v1.2.3"
	if got := s.Summary(); got != want {
		t.Fatalf("summary is\n %s\nwant\n %s", got, want)
	}
}

// TestAnIntegerArgumentIsSentAsAnInteger. The gateway canonicalises what
// it receives and the digest is over that, so a pipeline id filed as
// "41" and called as 41 are two different calls — and the approval a
// human gave for one would not admit the other.
func TestAnIntegerArgumentIsSentAsAnInteger(t *testing.T) {
	s := blueprint.RenderedStep{
		Tool:     "pipelines_write",
		Args:     map[string]string{"action": "run_pipeline", "pipelineId": "41"},
		ArgTypes: map[string]string{"pipelineId": "int"},
	}
	got, err := s.ArgsJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"pipelineId":41`) {
		t.Fatalf("pipelineId was not sent as an integer: %s", got)
	}
	bad := blueprint.RenderedStep{
		Tool: "pipelines_write", Args: map[string]string{"pipelineId": "not-a-number"},
		ArgTypes: map[string]string{"pipelineId": "int"},
	}
	if _, err := bad.ArgsJSON(); err == nil {
		t.Fatal("a non-integer was accepted for an argument declared an integer")
	}
}

// TestTheDriverKeepsItsOwnAdminPort. The operator's `kmx approve` needs
// the default one while the driver is waiting for them; on the same port
// they collide and the operator meets "address already in use" on the
// one command they were just told to run.
func TestTheDriverKeepsItsOwnAdminPort(t *testing.T) {
	if DefaultWorkflowAdminPort == "19091" {
		t.Fatal("the driver's admin port is the default one; `kmx approve` would collide with a waiting run")
	}
}

// TestAResumedRunCannotUseACaptureItDidNotMake. `--step publish` skips
// the step that composes the notes, and a driver that silently passed
// the literal "${capture.notes.file}" to a publish script would create a
// release whose body is that string.
func TestAResumedRunCannotUseACaptureItDidNotMake(t *testing.T) {
	r := &workflowRun{captures: map[string]string{}, files: map[string]string{}}
	_, err := r.resolveCaptures("--notes ${capture.notes.file}")
	if err == nil || !strings.Contains(err.Error(), "no step in THIS run captured it") {
		t.Fatalf("an unresolved capture was passed through: %v", err)
	}
	r.captures["notes"] = "hello"
	r.files["notes"] = "/tmp/notes.txt"
	got, err := r.resolveCaptures("--notes ${capture.notes.file} says ${capture.notes}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "--notes /tmp/notes.txt says hello" {
		t.Fatalf("resolved to %q", got)
	}
}

// TestAPollMarkerIsMatchedOnItsOwnLine. The prompt asks for the marker on
// a line of its own; a substring match anywhere would fire on the agent
// quoting the instruction back at it, and report a running build done.
func TestAPollMarkerIsMatchedOnItsOwnLine(t *testing.T) {
	if hasMarker("I will end with STATE: DONE when it finishes.\nStill building.", "STATE: DONE") {
		t.Fatal("the agent quoting the instruction was read as a finished build")
	}
	if !hasMarker("Two runs succeeded.\nSTATE: DONE\n", "STATE: DONE") {
		t.Fatal("a marker on its own line was not recognised")
	}
}

// TestTheCarriedBlueprintsAllParse. A blueprint that ships broken is a
// command that fails on somebody's first use of it, and `go test ./...`
// is where that should be found.
func TestTheCarriedBlueprintsAllParse(t *testing.T) {
	bundles, err := blueprint.Carried(kaimahi.Blueprints)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) == 0 {
		t.Fatal("kmx carries no blueprints; the test proves nothing")
	}
	for _, b := range bundles {
		if len(b.Scripts()) == 0 {
			continue
		}
		for _, name := range b.Scripts() {
			body, err := b.Script(name)
			if err != nil {
				t.Fatalf("blueprint %q: %v", b.Name, err)
			}
			if len(body) == 0 {
				t.Fatalf("blueprint %q carries an empty %s", b.Name, name)
			}
		}
	}
}

// TestAPrefixOfAnotherCallIsNotThisCall.
//
// The summary has no terminator, so `pipelineId 4` is a substring of a
// pending `pipelineId 41`, and `tag v1.0` of `tag v1.0.1`. A stale
// request left pending by an earlier run that timed out — which the
// driver explicitly tells the operator to expect and resume from — is
// the ordinary way a longer summary comes to be sitting there.
func TestAPrefixOfAnotherCallIsNotThisCall(t *testing.T) {
	stale := "release_publish: owner o, repo r, tag v1.0.1"
	mine := "release_publish: owner o, repo r, tag v1.0"
	pending := []map[string]any{
		{"id": "stale", "credential": "release-agent", "kind": "tool", "subject": "release_publish", "arg_summary": stale},
	}
	if got := selectRequest(pending, "release-agent", "release_publish", mine); got != "" {
		t.Fatalf("selected %q — a request whose summary merely CONTAINS this call's is a different call", got)
	}
	pending = append(pending, map[string]any{
		"id": "mine", "credential": "release-agent", "kind": "tool",
		"subject": "release_publish", "arg_summary": mine,
	})
	if got := selectRequest(pending, "release-agent", "release_publish", mine); got != "mine" {
		t.Fatalf("selected %q; the exact match is mine", got)
	}
}

// TestAnApprovalOfSomeOtherRequestIsNotThisApproval.
//
// A run whose own request was denied must not find a colleague's live
// grant on the same tool and carry on. For the publish step that would
// be 1.28 GB onto a public release, on a denial.
func TestAnApprovalOfSomeOtherRequestIsNotThisApproval(t *testing.T) {
	grants := []map[string]any{
		{"id": "g-other", "request_id": "req-other", "kind": "tool", "subject": "release_publish",
			"live": true, "decided_by": "slack:U123", "arg_digest": "beef"},
	}
	if _, _, err := selectGrant(grants, "req-mine", "release_publish"); err == nil {
		t.Fatal("another request's live grant was accepted as this request's approval")
	} else if !strings.Contains(err.Error(), "DENIED") {
		t.Fatalf("the message does not say what happened: %v", err)
	}

	grants = append(grants, map[string]any{
		"id": "g-mine", "request_id": "req-mine", "kind": "tool", "subject": "release_publish",
		"live": true, "decided_by": "slack:U999", "arg_digest": "cafe",
	})
	got, by, err := selectGrant(grants, "req-mine", "release_publish")
	if err != nil {
		t.Fatal(err)
	}
	if got.id != "g-mine" || got.digest != "cafe" || by != "slack:U999" {
		t.Fatalf("selected %+v decided by %q", got, by)
	}

	// A grant that has lapsed is not an approval to act on now.
	lapsed := []map[string]any{
		{"id": "g", "request_id": "req-mine", "kind": "tool", "subject": "release_publish", "live": false},
	}
	if _, _, err := selectGrant(lapsed, "req-mine", "release_publish"); err == nil ||
		!strings.Contains(err.Error(), "no longer live") {
		t.Fatalf("a spent grant was accepted: %v", err)
	}
}

// TestAnUnparseableOverlayFragmentIsUnknownNotAbsent. The collision
// guard exists so a merge refusal does not arrive as a proxy that will
// not roll; a hand edit that broke the JSON is exactly that case.
func TestAnUnparseableOverlayFragmentIsUnknownNotAbsent(t *testing.T) {
	if _, err := fragmentConstrains("{not json", "release-agent"); err == nil {
		t.Fatal("a fragment that does not parse was read as constraining nothing")
	}
	ok, err := fragmentConstrains(`{"standing_constraints": {"release-agent": {}}}`, "release-agent")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ok, err = fragmentConstrains(`{"standing_constraints": {"ap-agent": {}}}`, "release-agent")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

// TestANewAuditRowIsIdentifiedNotCounted.
//
// The audit view is capped, so on a busy credential a successful call
// can evict an older row of the same tool and leave the COUNT unchanged.
// A driver counting rows would then report that a call which did happen
// produced no audit row, and fail a release for a paging artefact.
func TestANewAuditRowIsIdentifiedNotCounted(t *testing.T) {
	old := auditRow{Created: "2026-09-04T10:00:00", Tool: "create_branch", Decision: "denied", Summary: "a"}
	fresh := auditRow{Created: "2026-09-04T10:00:05", Tool: "create_branch", Decision: "allowed", Summary: "a"}
	if old.id() == fresh.id() {
		t.Fatal("two different rows share an identity")
	}
	// Same second, same summary, different verdict: still two rows.
	sameSecond := auditRow{Created: old.Created, Tool: old.Tool, Decision: "allowed", Summary: "a"}
	if old.id() == sameSecond.id() {
		t.Fatal("a decision change did not change the row identity")
	}
}

// TestAGovernedCallIsNotRetriedAfterAnAmbiguousDrop.
//
// The two transport classes are not interchangeable. REFUSED means nothing
// reached the agent, so re-invoking is free. AMBIGUOUS means the request may
// have arrived and been acted on before the connection went — and a bounded
// step's turn IS the governed call, admitted by a standing constraint that
// has no use count, so a retry runs the pipeline again. A consequential step
// is protected by its uses-bounded grant; a bounded one is protected by
// nothing but this choice.
//
// Read and draft turns keep retrying both: re-asking a question costs a
// sample, and the flake this was built for is real.
func TestAGovernedCallIsNotRetriedAfterAnAmbiguousDrop(t *testing.T) {
	const (
		refused   = `Error invoking session: x failed to send HTTP request: Post "http://h": dial tcp 127.0.0.1:8083: connect: connection refused`
		ambiguous = `Error invoking session: x failed to send HTTP request: Post "http://h": EOF`
	)
	for _, tc := range []struct {
		kind             string
		retriesAmbiguous bool
	}{
		{blueprint.KindBounded, false},
		{blueprint.KindConsequential, false},
		{blueprint.KindRead, true},
		{blueprint.KindPropose, true},
		{blueprint.KindPoll, true},
	} {
		policy := retryPolicyFor(tc.kind)
		// Every kind retries the refused class: nothing reached the agent,
		// so nothing can have happened twice.
		if !policy.MatchString(refused) {
			t.Errorf("%s: a refused connection must be retried — nothing reached the agent", tc.kind)
		}
		if got := policy.MatchString(ambiguous); got != tc.retriesAmbiguous {
			t.Errorf("%s: retries an AMBIGUOUS drop = %v, want %v. A step whose turn makes the "+
				"governed call must fail rather than risk making it twice", tc.kind, got, tc.retriesAmbiguous)
		}
	}
	// And the safe class really is the narrower of the two, so the table
	// above cannot pass by both matchers being the same thing.
	if ChatRetryableSafe.MatchString(ambiguous) {
		t.Fatal("ChatRetryableSafe matched an ambiguous drop; it is supposed to match only the refused class")
	}
	if !ChatRetryable.MatchString(ambiguous) {
		t.Fatal("ChatRetryable no longer matches an ambiguous drop, so chat lost its retry")
	}
}

// TestADryRunRefreshesNoCredentialAndWritesNoSecret.
//
// `--dry-run` promises the operator that nothing is created. The refresh
// path breaks that promise in the most expensive way available to it: it
// shells out to mint a token and then `kubectl apply`s a Secret into the
// plane's custody. Because a turn step declares no upstream, every seam
// with a `refresh:` is re-minted before the FIRST step of a run — so a
// dry run of a workflow whose opening step only reads and drafts still
// rotated a live credential before it stopped.
//
// The property pinned here is the flag's, not the refresh's: under
// --dry-run the refresh command is never executed at all. The marker
// file is the proof, because a refresh that ran leaves one behind
// whether or not the kubectl that follows succeeds.
func TestADryRunRefreshesNoCredentialAndWritesNoSecret(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "minted")
	seams := map[string]blueprint.Seam{
		"ado": {Refresh: &blueprint.Refresh{
			// `sh` stands in for `az`: argv, no shell string, exactly as
			// a real refresh command is spelled.
			Command:  []string{"sh", "-c", "printf token > " + marker + "; printf token"},
			Requires: "sh",
			Secret:   "ado-token", Key: "api-key",
			Why: "the token lives about an hour",
		}},
	}
	// A turn step: no upstream, which is what makes it refresh EVERY seam.
	step := blueprint.RenderedStep{Kind: blueprint.KindRead, Label: "read the release notes"}

	newRun := func(dry bool) *workflowRun {
		var out strings.Builder
		return &workflowRun{
			app: &App{Out: &out, Err: &out, Run: &run.Runner{Stdout: &out, Stderr: &out},
				// Named so the kubectl a live refresh reaches is aimed at a
				// context that does not exist, rather than at whatever this
				// machine's current-context happens to be.
				Cfg: &config.Config{KubeContext: "kind-no-such-cluster-kmx-test"}},
			bundle: &blueprint.Bundle{Blueprint: &blueprint.Blueprint{Seams: seams}},
			opt:    RunOptions{DryRun: dry},
			dir:    t.TempDir(),
		}
	}

	if err := newRun(true).refreshFor(step); err != nil {
		t.Fatalf("dry run refreshFor: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("--dry-run ran the refresh command; a dry run that mints a credential and applies a " +
			"Secret is not a dry run, however the message describes it")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// The other side, so the assertion above cannot pass because the
	// fixture never refreshes anything: a live run DOES execute it. It
	// then fails at the kubectl that has no cluster, which is fine — the
	// marker is written before that and is what is being proved.
	_ = newRun(false).refreshFor(step)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("a live run did not execute the refresh command either, so the dry-run assertion "+
			"proves nothing: %v", err)
	}
}

// TestADryRunNeedsNoneOfTheToolsItWillNotUse.
//
// The preflight names every binary a run will need and warns about the
// missing ones. A dry run refreshes nothing, so naming `az` as absent
// sends the reader to install a tool for work this invocation will not
// do — and a preflight that cries wolf is one an operator learns to skim.
func TestADryRunNeedsNoneOfTheToolsItWillNotUse(t *testing.T) {
	seams := map[string]blueprint.Seam{
		"ado": {Refresh: &blueprint.Refresh{
			Requires: "definitely-not-on-path-kmx",
			Command:  []string{"definitely-not-on-path-kmx"},
			Secret:   "ado-token", Key: "api-key", Why: "expiry",
		}},
	}
	for _, tc := range []struct {
		dry   bool
		named bool
	}{{dry: true, named: false}, {dry: false, named: true}} {
		var out strings.Builder
		r := &workflowRun{
			app:      &App{Out: &out, Err: &out, Run: &run.Runner{Stdout: &out, Stderr: &out}},
			bundle:   &blueprint.Bundle{Blueprint: &blueprint.Blueprint{Seams: seams}},
			rendered: &blueprint.Rendered{},
			opt:      RunOptions{DryRun: tc.dry},
		}
		if err := r.preflightRequirements(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out.String(), "definitely-not-on-path-kmx"); got != tc.named {
			t.Fatalf("dry-run=%v: preflight named the refresh binary=%v, want %v.\n%s",
				tc.dry, got, tc.named, out.String())
		}
	}
}

func TestBundledScriptPreflightAutomaticallyRequiresBash(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := &workflowRun{
		app:    &App{},
		bundle: &blueprint.Bundle{Blueprint: &blueprint.Blueprint{}},
		rendered: &blueprint.Rendered{Steps: []blueprint.RenderedStep{{
			Label: "publish", Exec: &blueprint.RenderedExec{Script: "publish.sh"},
		}}},
	}
	err := r.preflightRequirements()
	if err == nil || !strings.Contains(err.Error(), "bash — needed for publish") {
		t.Fatalf("preflight error = %v", err)
	}
}

func TestWorkflowRefreshRefusesABlueprintWithNothingToRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.yaml")
	if err := os.WriteFile(path, []byte(`blueprint: v1
name: plain
summary: no expiring seams
credential: plain
agent: plain
parameters: {}
seams:
  demo:
    requires:
      read: []
    allow: [read]
steps:
  - name: read
    kind: read
    prompt: read
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	a := &App{Out: &out, Err: &out}
	err := a.RefreshWorkflow("", WorkflowOptions{File: path})
	if err == nil || !strings.Contains(err.Error(), "declares no expiring seam credentials") {
		t.Fatalf("RefreshWorkflow error = %v", err)
	}
}

func TestStandaloneWorkflowRefreshFailsWhenItsMintingToolIsMissing(t *testing.T) {
	r := &workflowRun{
		app: &App{},
		bundle: &blueprint.Bundle{Blueprint: &blueprint.Blueprint{Seams: map[string]blueprint.Seam{
			"demo": {Refresh: &blueprint.Refresh{
				Requires: "definitely-not-on-path-kmx", Command: []string{"definitely-not-on-path-kmx"},
				Secret: "demo-token", Key: "token",
			}},
		}}},
		strictRefresh: true,
	}
	err := r.refreshSeam("demo")
	if err == nil || !strings.Contains(err.Error(), "cannot refresh the demo credential") {
		t.Fatalf("strict refresh error = %v", err)
	}
}
