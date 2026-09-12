package app

// ONE driver, for every blueprint.
//
// The former release-specific driver established a set of properties that any
// governed workflow needs, and this is those properties, once:
//
//   - THE DRIVER FILES THE APPROVAL REQUEST, for the call the operator's
//     parameters name, and refuses to go on if the plane did not file
//     exactly that call. A model that proposed a different branch would
//     file a request too, and it would look identical in `kmx approvals`.
//     The accounts-payable demo paid for that lesson: on its first live
//     run the agent filed a payment for a different invoice at the same
//     amount, and the approval landed on the wrong one.
//   - A LIVE GRANT IS NOT RIDDEN. If the tool already carries a grant
//     somebody gave earlier, this run would spend an approval for a call
//     it never described. Refused.
//   - THE PLANE'S RECORD IS THE PROOF. After the call, the tool audit
//     must show it admitted under the grant. Nothing is reported as done
//     that the plane did not record — which is the rule this repository
//     gives its agent, applied to the code that enforces it.
//   - ITS OWN ADMIN PORT. The driver polls the admin API while it waits
//     for a human, and the human's own `kmx approve` needs the same port.
//     On the default the two collide and the operator meets "address
//     already in use" on the one command they were just told to run. The
//     release workflow found that the hard way, on its first real approval.
//   - BOUNDED WAITS, RESUMPTION AND CREDENTIAL REFRESH. A build is
//     minutes and a turn is request/response; an access token can be
//     shorter than the process it authorises.
//
// What is NOT here, deliberately: any way for a blueprint to say "the
// agent decides". The step vocabulary has no such kind, `call.args` may
// not read anything an agent produced (the parser refuses it), and the
// request is built from the rendered call. If a model wants a different
// call it can ask for one — and it will be denied, because the grant is
// welded to the digest of the call a human actually saw.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/blueprint"
)

// DefaultWorkflowAdminPort is the driver's own local admin port. It is
// NOT config.DefaultAdminPort, and that is the whole point: the operator
// needs the default free for `kmx approve` while this is waiting for
// them.
const DefaultWorkflowAdminPort = "19291"

// RunOptions are `kmx workflow run`'s knobs.
type RunOptions struct {
	WorkflowOptions
	// Steps limits the run to these steps, in blueprint order. It is how a
	// run is resumed after an expired credential, a failed build, or a
	// person going home.
	Steps []string
	// DryRun reads and drafts and stops before the first consequential
	// or bounded call. Nothing is created and nothing in the cluster is
	// written to — including the seam credentials a live run re-mints,
	// which is why a dry run rides whatever is already in custody.
	DryRun bool
	// Approver requires the identity recorded in the approval trail.
	// Empty means anyone the plane admits; CLI approvals record "admin",
	// not a verified person's identity.
	Approver string
	// AdminPort is the driver's own port-forward.
	AdminPort string
	// HumanSeconds is how long a step waits for a person.
	HumanSeconds int
}

// RunWorkflow executes a blueprint.
func (a *App) RunWorkflow(name string, opt RunOptions) error {
	b, err := loadBlueprint(name, opt.WorkflowOptions)
	if err != nil {
		return err
	}
	steps := b.StepNames()
	if len(opt.Steps) > 0 {
		selected := map[string]bool{}
		for _, step := range opt.Steps {
			if !containsString(steps, step) {
				return fmt.Errorf("blueprint %q has no step %q (it has: %s)", b.Name, step, strings.Join(steps, ", "))
			}
			selected[step] = true
		}
		steps = steps[:0]
		for _, step := range b.StepNames() {
			if selected[step] {
				steps = append(steps, step)
			}
		}
	}
	// BindRun, not Bind: which steps are in this run is decided by the
	// same parameters that are being bound, and binding every step
	// regardless of its `when:` is what left this command with no first
	// command at all. `active` is what `kmx workflow show` shows.
	values, active, err := b.BindRun(opt.Set, steps)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		return fmt.Errorf("nothing to run: every step asked for is conditional on a parameter that was not "+
			"supplied. Reporting a workflow as run when it did nothing is the one thing this driver must not "+
			"do.\n%s", guardList(b, steps))
	}
	if opt.AdminPort == "" {
		opt.AdminPort = DefaultWorkflowAdminPort
	}
	if opt.HumanSeconds == 0 {
		opt.HumanSeconds = 900
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}

	// ONCE, before the port-forward and before the first request is
	// filed. This is the command that files approvals, drives turns that
	// cut branches and dispatch builds, and runs whatever an
	// `ungoverned:` step names, so it is the last command that should be
	// able to land on a cluster nobody looked at.
	//
	// Once rather than per step, deliberately. Every consequential step
	// already stops and asks a human to approve the exact call, which is
	// where a second banner's work is already being done; a banner before
	// each of them would be four or five identical screens in one run,
	// and a screen people learn to scroll past protects nothing. What the
	// single banner costs is that it can scroll away before the step that
	// matters — so the step that matters names the cluster again, next to
	// the call being approved.
	action := fmt.Sprintf("run the %q workflow: file approval requests and perform the calls a human approves", b.Name)
	if opt.DryRun {
		action = fmt.Sprintf("read and draft the %q workflow (--dry-run: nothing is created)", b.Name)
	}
	if err := a.Guard(action, "kmx workflow run "+name); err != nil {
		return err
	}

	client, err := admin.Open(a, opt.AdminPort, a.Err)
	if err != nil {
		return err
	}
	defer client.Close()

	// The declared policy fields come from the RUNNING table, because
	// they are the order the audit summary reads and therefore the order
	// a pending request has to be matched on. The cluster's own overlay
	// fragments go with the question: a seam somebody onboarded with
	// `kmx tools add` lives there, and validating without them would
	// answer about a table the plane is not running.
	fragments, _, err := a.readOverlay()
	if err != nil {
		return err
	}
	upstreams, declared, err := a.validateWorkflowOverlay(client, fragments)
	if err != nil {
		return err
	}
	// The SAME assertion `kmx workflow govern` makes, and it belongs here
	// more than there: govern ran days ago, and what binds an approval is
	// the table as it stands NOW. A `policy_fields` list narrowed since —
	// someone dropping `tag` from release_publish because "approvals keep
	// not matching" — would otherwise file a request naming less than the
	// call does, and the grant a human gave would admit every tag on that
	// repository.
	if err := checkSeams(b, upstreams, declared); err != nil {
		return err
	}
	rendered, err := b.Render(values, active, declared)
	if err != nil {
		return err
	}
	if len(rendered.Steps) == 0 {
		// A backstop: `active` was not empty, so a step was dropped
		// somewhere between the guard and the render. Nothing is
		// reported as run.
		return fmt.Errorf("nothing to run: %s resolved to no work at all. Nothing was done",
			strings.Join(active, ", "))
	}

	run := &workflowRun{
		app: a, bundle: b, rendered: rendered, client: client, opt: opt,
		captures: map[string]string{}, files: map[string]string{},
	}
	dir, err := os.MkdirTemp("", "kmx-run")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	run.dir = dir

	a.notef("%s", a.presenter().Heading(fmt.Sprintf("workflow %s — %s", b.Name, b.Summary)))
	a.notef("source: %s", b.Source)
	if opt.DryRun {
		a.notef("--dry-run: the agent will read and draft, and stop before the first call with consequences.")
		a.notef("Nothing is created and nothing in the cluster is written to.")
		// Said here rather than discovered later: the credentials a live
		// run re-mints are left alone, so this run rides what is already
		// in custody. If one has expired, the agent will report the
		// seam's tools missing from its toolset rather than say so —
		// naming the cause up front is what makes that legible.
		if names := refreshingSeams(b); len(names) > 0 {
			a.notef("The %s credential is NOT refreshed (that would write a Secret). This run uses the one",
				strings.Join(names, ", "))
			a.notef("already in custody; if it has expired the agent will report those tools missing.")
		}
	}
	if err := run.preflightRequirements(); err != nil {
		return err
	}

	for _, step := range rendered.Steps {
		if err := run.do(step); err != nil {
			if _, stopped := err.(dryRunStop); stopped {
				a.notef("")
				a.notef("--dry-run stopped at %q. Nothing was created.", step.Label)
				return nil
			}
			return err
		}
	}

	a.notef("")
	a.notef("The record: every decision this credential got")
	if err := client.ToolAudit(a.Out, b.Credential); err != nil {
		return err
	}
	a.notef("Who approved what, and which call")
	return client.ApprovalAudit(a.Out, b.Credential)
}

// RefreshWorkflow refreshes the expiring credentials declared by a
// blueprint without inventing a synthetic workflow step or running an agent.
func (a *App) RefreshWorkflow(name string, opt WorkflowOptions) error {
	b, err := loadBlueprint(name, opt)
	if err != nil {
		return err
	}
	names := refreshingSeams(b)
	if len(names) == 0 {
		return fmt.Errorf("blueprint %q declares no expiring seam credentials to refresh", b.Name)
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if err := a.Guard(fmt.Sprintf("refresh the expiring seam credentials for the %q workflow", b.Name),
		"kmx workflow refresh "+name); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kmx-refresh")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	r := &workflowRun{app: a, bundle: b, dir: dir, strictRefresh: true}
	for _, seam := range names {
		if err := r.refreshSeam(seam); err != nil {
			return err
		}
	}
	return nil
}

// guardList names each step asked for that a `when:` left out, with the
// flag that turns it on. "Nothing ran" is only a usable message if it
// says what would have made something run.
func guardList(b *blueprint.Bundle, steps []string) string {
	var lines []string
	for _, name := range steps {
		if g := b.StepGuard(name); g != "" {
			lines = append(lines, fmt.Sprintf("  %s — needs --set %s=…", name, g))
		}
	}
	if len(lines) == 0 {
		return "  Steps: " + strings.Join(steps, ", ")
	}
	return strings.Join(lines, "\n")
}

type workflowRun struct {
	app      *App
	bundle   *blueprint.Bundle
	rendered *blueprint.Rendered
	client   *admin.Client
	opt      RunOptions
	dir      string
	captures map[string]string
	files    map[string]string
	// refreshed remembers which seams have been refreshed in this run,
	// so a five-step workflow does not mint five tokens for one seam
	// inside a minute.
	refreshed map[string]time.Time
	// strictRefresh is set by `workflow refresh`: unlike a workflow run,
	// that command has no useful fallback work if refreshing does nothing.
	strictRefresh bool
}

// preflightRequirements names every binary a step will need, before any
// of them runs. kmx does NOT fetch these: internal/kmx/toolchain fetches
// pinned, checksum-verified release artifacts whose identity IS the
// checksum, and `az` and `gh` are the operator's own logged-in CLIs. A
// freshly downloaded copy would have nobody logged into it, which buys
// nothing and adds a supply chain. They are required and named.
func (r *workflowRun) preflightRequirements() error {
	want := map[string][]string{}
	execWant := map[string]bool{}
	for _, s := range r.rendered.Steps {
		if s.Exec != nil {
			if s.Exec.Script != "" {
				want["bash"] = append(want["bash"], s.Label)
				execWant["bash"] = true
			}
			for _, bin := range s.Exec.Requires {
				want[bin] = append(want[bin], s.Label)
				execWant[bin] = true
			}
		}
	}
	// A dry run does not refresh anything, so it does not need the tools
	// that would: naming `az` as missing for work this run will not do
	// sends the reader to fix the wrong thing.
	if !r.opt.DryRun {
		for _, seam := range sortedAny(r.bundle.Seams) {
			if ref := r.bundle.Seams[seam].Refresh; ref != nil {
				want[ref.Requires] = append(want[ref.Requires], "refreshing the "+seam+" credential")
			}
		}
	}
	var missing, warnings []string
	for _, bin := range sortedAny(want) {
		if _, err := exec.LookPath(bin); err != nil {
			message := fmt.Sprintf("%s — needed for %s", bin, strings.Join(want[bin], ", "))
			if execWant[bin] {
				missing = append(missing, message)
			} else {
				warnings = append(warnings, message)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("workflow dependencies are not on PATH; no step was run:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(warnings) > 0 {
		r.app.notef("NOTE: not on PATH:")
		for _, warning := range warnings {
			r.app.notef("  %s", warning)
		}
		r.app.notef("Credential refresh is best-effort during a workflow run; the stored credential remains in place.")
	}
	return nil
}

func (r *workflowRun) do(s blueprint.RenderedStep) error {
	if err := r.refreshFor(s); err != nil {
		return err
	}
	switch s.Kind {
	case blueprint.KindRead, blueprint.KindPropose:
		return r.turnStep(s)
	case blueprint.KindPoll:
		return r.pollStep(s)
	case blueprint.KindBounded:
		return r.boundedStep(s)
	case blueprint.KindConsequential:
		return r.consequentialStep(s)
	}
	return fmt.Errorf("internal: no driver for step kind %q", s.Kind)
}

// --- agent turns ------------------------------------------------------

func (r *workflowRun) turnStep(s blueprint.RenderedStep) error {
	r.app.notef("")
	r.app.notef("%s", r.app.presenter().Heading(fmt.Sprintf("== %s (%s)", s.Label, s.Kind)))
	reply, err := r.turn(s)
	if err != nil {
		return err
	}
	if s.Capture == "" {
		return nil
	}
	if strings.TrimSpace(reply) == "" {
		return fmt.Errorf("step %q captures %q and the agent's reply was empty", s.Label, s.Capture)
	}
	r.captures[s.Capture] = reply
	path := filepath.Join(r.dir, s.Capture+".txt")
	if err := os.WriteFile(path, []byte(reply), 0o600); err != nil {
		return err
	}
	r.files[s.Capture] = path
	r.app.notef("Captured %q (%d lines) — it is prose, and nothing binds it.",
		s.Capture, strings.Count(reply, "\n")+1)
	return nil
}

// turn runs one agent turn and returns its reply. A turn only counts if
// the task completed WITH a reply: an earlier version of the former release
// driver printed a note and returned success on a failed
// turn, which is the exact thing this repository's agent brief forbids,
// in the code that enforces it.
// retryPolicyFor picks the transport-retry class a step's turn may use.
//
// A read or a draft can be re-asked: nothing it does outlives the reply. A
// bounded or consequential step's turn makes the governed call, so an
// AMBIGUOUS drop — the request may have arrived and been acted on before the
// connection went — must NOT be retried. It fails the step instead, which is
// the recoverable direction: a stopped release can be resumed with --step,
// and a pipeline that ran twice cannot be un-run.
func retryPolicyFor(kind string) *regexp.Regexp {
	switch kind {
	case blueprint.KindBounded, blueprint.KindConsequential:
		return ChatRetryableSafe
	}
	return ChatRetryable
}

func (r *workflowRun) turn(s blueprint.RenderedStep) (string, error) {
	prompt, err := r.resolveCaptures(s.Prompt)
	if err != nil {
		return "", err
	}
	out, status, err := r.app.askAgent(r.bundle.Agent, prompt, "", false, retryPolicyFor(s.Kind))
	if err != nil {
		return "", err
	}
	if !renderChat(r.app.Out, out) {
		fmt.Fprintln(r.app.Out, out)
	}
	if status != 0 {
		return "", fmt.Errorf("step %q: kagent invoke exited %d", s.Label, status)
	}
	line := lastJSONLine(out)
	if line == "" {
		return "", fmt.Errorf("step %q: the agent's turn did not complete with a task (exit %d)", s.Label, status)
	}
	var task a2aTask
	if err := json.Unmarshal([]byte(line), &task); err != nil {
		return "", fmt.Errorf("step %q: the agent's turn did not complete with a task: %w", s.Label, err)
	}
	reply := firstText(task)
	if task.Status.State != "completed" || strings.TrimSpace(reply) == "" {
		return "", fmt.Errorf("step %q: the agent's turn ended %q with no reply. Nothing is being reported as "+
			"done that the plane did not record", s.Label, task.Status.State)
	}
	return reply, nil
}

func (r *workflowRun) pollStep(s blueprint.RenderedStep) error {
	r.app.notef("")
	r.app.notef("%s", r.app.presenter().Heading(fmt.Sprintf("== %s — watching, up to %ds, every %ds", s.Label, s.Poll.TimeoutSeconds, s.Poll.IntervalSeconds)))
	deadline := time.Now().Add(time.Duration(s.Poll.TimeoutSeconds) * time.Second)
	for time.Now().Before(deadline) {
		if err := r.refreshFor(s); err != nil {
			return err
		}
		reply, err := r.turn(s)
		if err != nil {
			// A turn that failed mid-poll is not a build result. Say so
			// and keep waiting rather than reporting either outcome.
			r.app.notef("the turn did not complete: %v", err)
		} else {
			switch {
			case hasMarker(reply, s.Poll.Done):
				r.app.notef("The agent reports every build finished successfully.")
				return nil
			case hasMarker(reply, s.Poll.Failed):
				if s.OnFailure != "" {
					r.app.notef("A build failed — the agent reads the log")
					failing := s
					failing.Prompt = s.OnFailure
					if _, err := r.turn(failing); err != nil {
						r.app.notef("could not read the failure: %v", err)
					}
				}
				return fmt.Errorf("step %q: a build failed — see above. Nothing further was done", s.Label)
			}
		}
		time.Sleep(time.Duration(s.Poll.IntervalSeconds) * time.Second)
	}
	return fmt.Errorf("step %q: still running after %ds. Not claiming a result the tools did not report; "+
		"resume with --step %s to keep waiting:\n  %s", s.Label, s.Poll.TimeoutSeconds, s.Name, r.resumeCommand(s.Name))
}

// hasMarker looks for the terminal marker on a line of its own, which is
// what the prompt asks for. A substring match anywhere would fire on the
// agent quoting the instruction back.
func hasMarker(reply, marker string) bool {
	for _, line := range strings.Split(reply, "\n") {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}

// --- governed calls ---------------------------------------------------

// boundedStep is a call with consequences that a STANDING CONSTRAINT
// admits: no human, inside declared bounds, audited. The driver's job is
// to prove that is actually what happened — a bounded call that was
// admitted for some other reason (an allowlist entry, a stray grant) is
// not the posture the blueprint declared.
func (r *workflowRun) boundedStep(s blueprint.RenderedStep) error {
	r.app.notef("")
	r.app.notef("%s", r.app.presenter().Heading(fmt.Sprintf("== %s — bounded: %s", s.Label, s.Summary())))
	if r.opt.DryRun {
		r.app.notef("--dry-run: stopping before the first call with consequences.")
		return errDryRunStop
	}
	before, err := r.newestFor(s.Tool)
	if err != nil {
		return err
	}
	if _, err := r.turn(s); err != nil {
		r.app.notef("the agent's turn did not complete cleanly: %v", err)
	}
	row, err := r.newestAudit(s.Tool, before)
	if err != nil {
		return err
	}
	if !strings.Contains(row.Decision, "allowed") {
		return fmt.Errorf("step %q: %s was not admitted: %s %s", s.Label, s.Tool, row.Decision, row.Detail)
	}
	if !strings.Contains(strings.ToLower(row.Detail), "standing constraint") {
		return fmt.Errorf("step %q: %s was admitted, but not by the standing bound this blueprint declares — "+
			"the plane says %q. A bounded step that is admitted for some other reason is not the posture that "+
			"was reviewed", s.Label, s.Tool, row.Detail)
	}
	r.app.notef("Admitted inside the standing bound, with no human, and audited.")
	return nil
}

// consequentialStep is the governed shape.
func (r *workflowRun) consequentialStep(s blueprint.RenderedStep) error {
	r.app.notef("")
	r.app.notef("%s", r.app.presenter().Warning(fmt.Sprintf("== Proposing: %s", s.Summary())))
	if r.opt.DryRun {
		r.app.notef("--dry-run: stopping before the first call with consequences. Nothing was created.")
		return errDryRunStop
	}

	// A live grant would let this run's call succeed without anyone
	// approving anything in it.
	live, err := r.liveGrant(s.Tool)
	if err != nil {
		return err
	}
	if live {
		return fmt.Errorf("%s already carries a LIVE grant on %q. This run would spend an approval somebody "+
			"gave earlier, for a call this run did not describe. Wait for it to expire or be exhausted before starting again; denying a pending request does not revoke a live grant",
			s.Tool, r.bundle.Credential)
	}

	// The ungoverned action's PREVIEW runs before the approval, so
	// whoever decides sees what lands rather than a count they take on
	// trust.
	if s.Exec != nil && len(s.Exec.Preview) > 0 {
		r.app.notef("What this would do:")
		if err := r.exec(s, s.Exec.Preview); err != nil {
			return fmt.Errorf("step %q: could not preview the action — nothing was created: %w", s.Label, err)
		}
	}

	args, err := s.ArgsJSON()
	if err != nil {
		return err
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return err
	}
	if _, err := r.client.Request(r.bundle.Credential, "tool", s.Tool, parsed); err != nil {
		return err
	}

	// Never selected by position: the request this run must wait for is
	// the one whose summary names every policy-relevant field of the
	// call the operator asked for. A selector less specific than that
	// could match a request a human then approves by mistake.
	id, digest, err := r.pendingRequest(s)
	if err != nil {
		return err
	}
	r.app.notef("%s", r.app.presenter().Warning(fmt.Sprintf("Filed as request %s. What a human is asked is the CALL:", id)))
	r.app.notef("  %s", s.Summary())
	// The run's one banner is minutes and several steps back by now. The
	// cluster is half of what a person is being asked to approve, so it
	// is repeated where the decision is actually made.
	r.app.notef("  on cluster:  %s", r.app.Cfg.KubeContext)
	if s.Ungoverned != "" {
		r.app.notef("")
		r.app.notef("%s this step's ACTION is not governed by the plane.", r.app.presenter().Warning("NOTE:"))
		for _, line := range strings.Split(strings.TrimRight(wrap(s.Ungoverned, 76), "\n"), "\n") {
			r.app.notef("  %s", line)
		}
	}
	r.app.notef("")
	if r.opt.Approver == "" {
		r.app.notef("%s  %s", r.app.presenter().Accent("Approve it with:"), r.app.operationCommand("approve", id, "--uses", "1", "--ttl", "10m"))
	} else {
		r.app.notef("Required approver: %s. CLI approvals record admin, not a verified person's identity.", r.opt.Approver)
	}

	permit, err := r.awaitApproval(id, s.Tool)
	if err != nil {
		return fmt.Errorf("%w\n  To retry this step (only when no earlier grant is live): %s", err, r.resumeCommand(s.Name))
	}
	if permit.digest == "" || permit.digest != digest {
		return fmt.Errorf("request %s and grant %s do not carry the same nonempty arg_digest; refusing to perform the call", id, permit.id)
	}

	before, err := r.newestFor(s.Tool)
	if err != nil {
		return err
	}
	r.app.notef("%s", r.app.presenter().Success("Approved — performing the call"))
	if s.Exec != nil {
		// The DECISION was governed; the TRANSFER is this, and the plane
		// will record nothing further — no metering, no tool-audit row.
		// So the grant this run's OWN request produced is named here,
		// because it is the only evidence there will be that the thing
		// about to move bytes was approved at all.
		r.app.notef("Acting under grant %s from request %s, welded to digest %s.", permit.id, id, permit.digest)
		r.app.notef("The plane records the DECISION and nothing about the transfer: it is outside the gateway,")
		r.app.notef("so there will be no tool-audit row, no metering, and no record of what actually moved.")
		return r.exec(s, s.Exec.Args)
	}
	if _, err := r.turn(s); err != nil {
		r.app.notef("the agent's turn did not complete cleanly: %v", err)
	}
	row, err := r.newestAudit(s.Tool, before)
	if err != nil {
		return err
	}
	if !strings.Contains(row.Decision, "allowed") || !strings.Contains(row.Detail, "granted") {
		return fmt.Errorf("%s was approved but the call was not admitted under the grant (%s %s). Nothing is "+
			"being reported as done that the plane did not record", s.Tool, row.Decision, row.Detail)
	}
	if row.Digest == "" || row.Digest != digest {
		return fmt.Errorf("%s has a granted audit row, but its arg_digest does not match the filed request and grant. The approved call is not verified", s.Tool)
	}
	r.app.notef("Admitted under a grant. The filed request, its grant and this audit row carry the same digest.")
	r.app.notef("The approved policy-relevant arguments match the admitted call; downstream completion is not proven by admission.")
	return nil
}

// errDryRunStop ends a run at the first step with consequences, without
// reporting a failure.
var errDryRunStop = dryRunStop{}

type dryRunStop struct{}

func (dryRunStop) Error() string { return "dry run: stopped before the first call with consequences" }

// --- the plane's own record -------------------------------------------

type auditRow struct {
	Created  string
	Tool     string
	Decision string
	Detail   string
	Summary  string
	Digest   string
}

// id is a row's identity, for telling "the call produced a new row" from
// "the page happened to shift". A COUNT cannot do that: the audit view is
// capped, so on a busy credential a successful call can evict an older
// row of the same tool and leave the count unchanged — and the driver
// would then report that a call which did happen produced no audit row
// at all, failing a release for a paging artefact.
func (a auditRow) id() string {
	return a.Created + "|" + a.Decision + "|" + a.Detail + "|" + a.Summary + "|" + a.Digest
}

func (r *workflowRun) auditRows() ([]auditRow, error) {
	doc, err := r.client.Get("tool-audit", "/admin/tool-audit?credential="+r.bundle.Credential+"&limit=200")
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(doc["entries"])
	var entries []map[string]any
	_ = json.Unmarshal(raw, &entries)
	out := make([]auditRow, 0, len(entries))
	for _, e := range entries {
		out = append(out, auditRow{
			Created:  text(e["created_at"]),
			Tool:     text(e["tool"]),
			Decision: text(e["decision"]),
			// `detail` is the plane's own word for WHY: "granted",
			// "within standing constraint", "tool call not permitted…".
			// It is the column `kmx audit tool` prints and CI greps.
			Detail:  text(e["detail"]),
			Summary: text(e["arg_summary"]),
			Digest:  text(e["arg_digest"]),
		})
	}
	return out, nil
}

// newestFor returns the identity of the newest audit row for a tool, or
// "" when there is none. Taken before a call, it is what the row after
// the call has to differ from.
func (r *workflowRun) newestFor(tool string) (string, error) {
	rows, err := r.auditRows()
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.Tool == tool {
			return row.id(), nil
		}
	}
	return "", nil
}

// newestAudit returns the audit row this call produced, and refuses to
// invent one: if the newest row for the tool is the one that was already
// there, the call did not reach the plane at all — which is a different
// fact from a denial, and must not be reported as either.
func (r *workflowRun) newestAudit(tool, before string) (auditRow, error) {
	rows, err := r.auditRows()
	if err != nil {
		return auditRow{}, err
	}
	for _, row := range rows {
		if row.Tool != tool {
			continue
		}
		if row.id() == before {
			break
		}
		return row, nil
	}
	return auditRow{}, fmt.Errorf("%s produced no tool-audit row at all. The agent did not make the call — "+
		"a tool it cannot see is a tool it cannot attempt, so there is no denial either", tool)
}

func (r *workflowRun) liveGrant(tool string) (bool, error) {
	grants, err := r.grants()
	if err != nil {
		return false, err
	}
	for _, g := range grants {
		if text(g["kind"]) == "tool" && text(g["subject"]) == tool && truthy(g["live"]) {
			return true, nil
		}
	}
	return false, nil
}

// pendingRequest finds the request for THIS call, by its summary.
func (r *workflowRun) pendingRequest(s blueprint.RenderedStep) (string, string, error) {
	doc, err := r.client.Get("approvals", "/admin/approvals")
	if err != nil {
		return "", "", err
	}
	raw, _ := json.Marshal(doc["pending"])
	var requests []map[string]any
	_ = json.Unmarshal(raw, &requests)
	id := selectRequest(requests, r.bundle.Credential, s.Tool, s.Summary())
	if id == "" {
		found := "  Nothing is pending for that tool."
		if others := candidateSummaries(requests, r.bundle.Credential, s.Tool); len(others) > 0 {
			found = "  What IS pending for that tool:\n    " + strings.Join(others, "\n    ")
		}
		return "", "", fmt.Errorf("the request for this call was not filed:\n    %s\n%s\n"+
			"  Nothing has been approved and nothing was done.", s.Summary(), found)
	}
	for _, req := range requests {
		if text(req["id"]) == id {
			if digest, ok := req["arg_digest"].(string); ok && strings.TrimSpace(digest) != "" {
				return id, digest, nil
			}
		}
	}
	return "", "", fmt.Errorf("request %s carries no arg_digest; refusing to proceed without an exact-call binding", id)
}

// selectRequest picks the pending request for one exact call.
//
// NEVER by position, never by tool name alone, and never by SUBSTRING.
// The summary names every policy-relevant field of the call in the order
// the plane declares them, and the match is equality — because a
// substring match is a wider selector than it looks. A call ending
// `pipelineId 4` is a substring of a pending `pipelineId 41`, and a
// branch `release/v1` of `release/v10`, so an unanchored match could
// select somebody else's request and hand a human a call this run never
// described. That is the shape of the accounts-payable demo's failure: on
// its first live run the agent filed a payment for a different invoice at
// the same amount, and the approval landed on the wrong one.
//
// The plane builds the same string from the same declared fields
// (plane/internal/gateway/digest.go, summarize), so equality is what
// normally holds. Where it cannot — a value long enough for the plane to
// clip — the plane's summary is SHORTER than this one, so a substring
// test would not have rescued it either. It fails closed instead, and
// names what it did find.
func selectRequest(requests []map[string]any, credential, tool, want string) string {
	for _, req := range requests {
		if text(req["credential"]) != credential || text(req["kind"]) != "tool" {
			continue
		}
		if text(req["subject"]) != tool {
			continue
		}
		if text(req["arg_summary"]) == want {
			return text(req["id"])
		}
	}
	return ""
}

// candidateSummaries is what a failed selection reports: the calls that
// ARE pending for this tool. An operator whose request was not found
// needs to see what was, or the message is "no" with no way forward.
func candidateSummaries(requests []map[string]any, credential, tool string) []string {
	var out []string
	for _, req := range requests {
		if text(req["credential"]) == credential && text(req["kind"]) == "tool" &&
			text(req["subject"]) == tool {
			out = append(out, text(req["arg_summary"]))
		}
	}
	return out
}

// awaitApproval blocks until a human decides, or the wait runs out.
func (r *workflowRun) awaitApproval(id, tool string) (grant, error) {
	deadline := time.Now().Add(time.Duration(r.opt.HumanSeconds) * time.Second)
	for time.Now().Before(deadline) {
		still, err := r.stillPending(id)
		if err != nil {
			return grant{}, err
		}
		if !still {
			// The request left the pending list: approved, or denied.
			// Which one is the GRANTS view's answer, not a guess — a
			// driver that assumed "gone means approved" would carry on
			// after a denial.
			return r.confirmDecision(id, tool)
		}
		time.Sleep(5 * time.Second)
	}
	return grant{}, fmt.Errorf("nobody decided request %s within %ds. This step's action was not performed",
		id, r.opt.HumanSeconds)
}

func (r *workflowRun) resumeCommand(step string) string {
	command := r.app.workflowCommand("run", r.bundle.Name, r.opt.WorkflowOptions) + " --step " + shellArg(step)
	if r.opt.Approver != "" {
		command += " --approver " + shellArg(r.opt.Approver)
	}
	return command
}

func (r *workflowRun) stillPending(id string) (bool, error) {
	doc, err := r.client.Get("approvals", "/admin/approvals")
	if err != nil {
		return false, err
	}
	raw, _ := json.Marshal(doc["pending"])
	var requests []map[string]any
	_ = json.Unmarshal(raw, &requests)
	for _, req := range requests {
		if text(req["id"]) == id {
			return true, nil
		}
	}
	return false, nil
}

// confirmDecision distinguishes an approval from a denial by the grant
// THIS REQUEST created, and honours --approver: the trail records who
// decided, and a workflow that wanted one person's approval must not accept
// somebody else's.
//
// Matched on `request_id`, never on the tool. A grant row carries the id
// of the request it came from, and matching on the tool alone would
// accept a colleague's approval of a different call on the same tool —
// so a run whose own request was DENIED could find somebody else's live
// grant and carry on. That is the "gone from the pending list means
// approved" assumption this function exists to refuse, arriving by
// another door.
func (r *workflowRun) confirmDecision(id, tool string) (grant, error) {
	grants, err := r.grants()
	if err != nil {
		return grant{}, err
	}
	permit, by, err := selectGrant(grants, id, tool)
	if err != nil {
		return grant{}, err
	}
	if r.opt.Approver != "" && by != r.opt.Approver {
		return grant{}, fmt.Errorf("request %s was approved by %q, and this run requires %q. Nothing was done",
			id, by, r.opt.Approver)
	}
	r.app.notef("Approved%s.", decidedBy(by))
	return permit, nil
}

// selectGrant finds the grant one request produced, by REQUEST ID.
//
// Never by tool: a grant row names the request it came from, and
// matching on the tool alone would accept a colleague's approval of a
// different call on the same tool — so a run whose own request was
// DENIED could find somebody else's live grant and carry on. That is the
// "gone from the pending list means approved" assumption arriving by
// another door.
func selectGrant(grants []map[string]any, id, tool string) (grant, string, error) {
	for _, g := range grants {
		if text(g["request_id"]) != id {
			continue
		}
		if !truthy(g["live"]) {
			return grant{}, "", fmt.Errorf("request %s produced a grant that is no longer live — it lapsed, or "+
				"its uses are spent, before this run could act on it. Nothing was done", id)
		}
		return grant{id: text(g["id"]), digest: text(g["arg_digest"])}, text(g["decided_by"]), nil
	}
	return grant{}, "", fmt.Errorf("request %s left the pending list and produced no grant of its own — it was "+
		"DENIED, or it was withdrawn. Nothing was done.\n"+
		"  (A live grant on %s from some other request is not this one, and is not accepted.)", id, tool)
}

// grant is the authority this run's own request produced.
type grant struct {
	id     string
	digest string
}

// grants reads the credential's grants, and FAILS CLOSED on a shape it
// does not recognise.
//
// This is the one read in this file whose empty answer means "permitted"
// — `liveGrant` reads it to decide whether a run may proceed. Every
// other read here turns an empty result into an error by what it does
// next; this one would turn a renamed key or a changed response shape
// into "no grants, go ahead", silently and forever.
func (r *workflowRun) grants() ([]map[string]any, error) {
	doc, err := r.client.Get("grants", "/admin/grants?credential="+r.bundle.Credential+"&limit=200")
	if err != nil {
		return nil, err
	}
	value, ok := doc["grants"]
	if !ok {
		return nil, fmt.Errorf("the plane's grants view carried no \"grants\" key. kmx will not read that as " +
			"\"no grants\": this is the answer that decides whether a run may proceed, and an unrecognised shape " +
			"is not a permission. Is the plane a different version? (kmx plane)")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var grants []map[string]any
	if err := json.Unmarshal(raw, &grants); err != nil {
		return nil, fmt.Errorf("the plane's grants view is not a list of grants: %w", err)
	}
	return grants, nil
}

func decidedBy(who string) string {
	if who == "" || who == "none" {
		return ""
	}
	return " by " + who
}

// --- actions on this machine ------------------------------------------

// exec runs a step's ungoverned action.
func (r *workflowRun) exec(s blueprint.RenderedStep, args []string) error {
	var bin string
	var argv []string
	switch {
	case s.Exec.Script != "":
		// Materialised from the bundle rather than read off disk, which
		// is what lets a blueprint carried by the binary run on a
		// machine with no checkout.
		body, err := r.bundle.Script(s.Exec.Script)
		if err != nil {
			return err
		}
		path := filepath.Join(r.dir, s.Exec.Script)
		if err := os.WriteFile(path, body, 0o700); err != nil {
			return err
		}
		bin, argv = "bash", append([]string{path}, args...)
	default:
		bin, argv = s.Exec.Command[0], append(append([]string{}, s.Exec.Command[1:]...), args...)
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("step %q needs %s on PATH and it is not there", s.Label, bin)
	}
	for _, need := range s.Exec.Requires {
		if _, err := exec.LookPath(need); err != nil {
			return fmt.Errorf("step %q needs %s on PATH and it is not there. kmx does not fetch it: it is your "+
				"own logged-in tool, and a fresh download would have nobody logged into it", s.Label, need)
		}
	}
	cmd := r.app.Run.Command(bin, argv...)
	cmd.Env = os.Environ()
	for _, k := range sortedKeysOf(s.Exec.Env) {
		v, err := r.resolveCaptures(s.Exec.Env[k])
		if err != nil {
			return err
		}
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout, cmd.Stderr = r.app.Out, r.app.Err
	fmt.Fprintf(r.app.Err, "%s %s\n", bin, strings.Join(argv, " "))
	return cmd.Run()
}

// resolveCaptures substitutes ${capture.x} and ${capture.x.file}, which
// the renderer deliberately left alone: they do not exist until a step
// has run.
func (r *workflowRun) resolveCaptures(s string) (string, error) {
	out := s
	for name, text := range r.captures {
		out = strings.ReplaceAll(out, "${capture."+name+"}", text)
		out = strings.ReplaceAll(out, "${capture."+name+".file}", r.files[name])
	}
	if i := strings.Index(out, "${capture."); i >= 0 {
		rest := out[i:]
		if end := strings.Index(rest, "}"); end > 0 {
			return "", fmt.Errorf("this step reads %s, and no step in THIS run captured it. Resuming with "+
				"--step skips the step that would have; run the whole workflow, or the step that captures it",
				rest[:end+1])
		}
	}
	return out, nil
}

// --- expiring credentials ---------------------------------------------

// refreshFor re-mints an expiring upstream credential before a step that
// touches its seam.
//
// The Entra token the release workflow rides lives about an hour and a
// release session is longer; it expired twice on the first real run, and
// both times the visible symptom was the agent saying "there is no
// pipelines_build tool in my toolset" — the seam had gone Accepted=False
// and kagent had
// dropped its tools. Cause and symptom nowhere near each other, and
// nothing in the message pointing at a credential.
func (r *workflowRun) refreshFor(s blueprint.RenderedStep) error {
	if r.opt.DryRun {
		// A dry run writes nothing to the cluster, and a refresh is a
		// write: it mints a token and `kubectl apply`s a Secret into
		// plane custody. Doing that under a flag whose whole promise is
		// "nothing was created" is a trap, and the promise is the more
		// valuable of the two — so the dry run rides the credential
		// already in custody and says so before it starts. A stale one
		// shows up as the agent reporting a tool missing from its
		// toolset, which is the symptom the note at the top of the run
		// names.
		return nil
	}
	seam := s.Upstream
	if seam == "" {
		// A turn can touch any seam this workflow has, so refresh them
		// all — a token minted a minute ago is not minted again.
		for _, name := range sortedAny(r.bundle.Seams) {
			if err := r.refreshSeam(name); err != nil {
				return err
			}
		}
		return nil
	}
	return r.refreshSeam(seam)
}

// refreshingSeams names the seams this workflow would re-mint a
// credential for, in a stable order. A dry run uses it to say which
// credentials it is deliberately leaving alone.
func refreshingSeams(b *blueprint.Bundle) []string {
	var names []string
	for _, name := range sortedAny(b.Seams) {
		if b.Seams[name].Refresh != nil {
			names = append(names, name)
		}
	}
	return names
}

// refreshInterval is how often one seam's credential is re-minted inside
// a run. Short enough that no step rides a token near its end; long
// enough that a ten-step workflow does not shell out ten times.
const refreshInterval = 10 * time.Minute

func (r *workflowRun) refreshSeam(name string) error {
	ref := r.bundle.Seams[name].Refresh
	if ref == nil {
		return nil
	}
	if r.refreshed == nil {
		r.refreshed = map[string]time.Time{}
	}
	if last, ok := r.refreshed[name]; ok && time.Since(last) < refreshInterval {
		return nil
	}
	if _, err := exec.LookPath(ref.Requires); err != nil {
		if r.strictRefresh {
			return fmt.Errorf("%s is not on PATH; cannot refresh the %s credential", ref.Requires, name)
		}
		r.app.notef("%s is not on PATH — leaving the %s credential as it is (%s)", ref.Requires, name, ref.Why)
		return nil
	}
	out, err := r.app.Run.Capture(ref.Command[0], ref.Command[1:]...)
	if err != nil {
		if r.strictRefresh {
			return fmt.Errorf("could not mint a %s credential: %w", name, err)
		}
		r.app.notef("could not mint a %s credential (%v); the stored one stays in place", name, err)
		return nil
	}
	if strings.TrimSpace(out) == "" {
		if r.strictRefresh {
			return fmt.Errorf("%s returned nothing for the %s credential", ref.Requires, name)
		}
		r.app.notef("%s returned nothing for the %s credential; the stored one stays in place", ref.Requires, name)
		return nil
	}
	// Through a 0600 file and `--from-file`, never argv: a token in a
	// command line is a token in the process table, which is why
	// `kmx credential capture` keeps its Secret off argv too.
	path := filepath.Join(r.dir, name+".cred")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(out)), 0o600); err != nil {
		return err
	}
	defer os.Remove(path)
	manifest, err := r.app.kubectlCapture("-n", "kaimahi", "create", "secret", "generic", ref.Secret,
		"--from-file="+ref.Key+"="+path, "--dry-run=client", "-o", "yaml")
	if err != nil {
		return err
	}
	// Taken BEFORE the write, because its whole job is to be the thing the
	// verdict afterwards has to have moved past (seamverdict.go).
	var baseline seamBaseline
	if ref.Seam != "" {
		baseline = r.app.seamVerdictBaseline("kagent", ref.Seam, r.app.timeNow())
	}
	if err := r.app.Run.RunStdin([]byte(manifest), "kubectl", r.app.kubectl("apply", "-f", "-")...); err != nil {
		return err
	}
	r.refreshed[name] = r.app.timeNow()
	r.app.notef("Refreshed the %s credential in plane custody: %s", name, ref.Why)
	return r.reconnectSeam(ref, baseline)
}

// reconnectSeam makes kagent look again.
//
// `Accepted` is a CACHED reconcile result: kagent records it when it last
// tried and does not retry, so after a refresh it still reads Unauthorized
// from minutes ago. The release workflow's first health check reported a
// healthy credential as broken for exactly this reason.
func (r *workflowRun) reconnectSeam(ref *blueprint.Refresh, baseline seamBaseline) error {
	if ref.Seam == "" {
		return nil
	}
	// The verdict has to be about the credential just written. A cached
	// `True` from before the refresh is not a pass — it is the answer to a
	// question about a credential that no longer exists, and treating it as
	// one is how a rotated seam reported healthy while it was not.
	//
	// This asks kagent to look and then WAITS for what it decides, through
	// the one implementation every other caller uses (seamverdict.go). What
	// stood here before nudged the seam, slept, and returned without reading
	// again — so it never learned anything, and a credential kagent went on
	// to refuse was refreshed and then not mentioned.
	verdict, err := r.app.waitForSeamVerdict("kagent", ref.Seam, baseline)
	if err != nil {
		// Not fatal — but not silence either. An RBAC denial or a typo in
		// `seam:` reads exactly like a healthy seam if the error is
		// dropped, and the run then continues against a seam whose
		// credential was just rotated.
		r.app.notef("could not read the %s seam's Accepted condition (%v); not reconnecting it", ref.Seam, err)
		return nil
	}
	if verdict.State == verdictAccepted {
		return nil
	}
	// A workflow run is not the place to fail on this: the credential was
	// refreshed because a step needs it, and a rejection here is worth saying
	// loudly rather than turning into a stop. The step that uses the seam
	// will fail on its own terms if the credential really is bad.
	r.app.notef("The %s seam's status is %s", ref.Seam, verdict.Line(r.app.timeNow()))
	return nil
}

// --- small helpers ----------------------------------------------------

func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "yes" || t == "true"
	default:
		return false
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sortedKeysOf(m map[string]string) []string { return sortedKeys(m) }
