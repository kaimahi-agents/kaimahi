package app

// The governance of a workflow, declared once and applied by one command.
//
// What this replaces is not a missing feature — it is an intent that had
// no single place to live. The release workflow (docs/release-agent.md)
// works, and it is
// unrepeatable: which tools its credential may call is a Make variable,
// which repository it may call them on is a 191-line shell script, and
// what its arguments MEAN is a committed JSON table. `kmx agent create`
// reaches none of it.
//
// The division of labour is the same one `kmx tools add` drew, one level
// up: kmx owns what is MECHANICAL — reading the overlay whole
// so another operator's fragment is not pruned, carrying the
// resourceVersion so a stale apply is refused, asking the RUNNING plane
// whether the result would load — and the operator owns POLICY, which is
// what the blueprint file says.
//
// The one thing kmx will NOT do here is write a seam. A hosted or keyed
// upstream cannot live in the overlay (plane/internal/config/overlay.go),
// so a blueprint NAMES its seams and this checks them: every upstream it
// uses must already be in the merged table, and every policy_fields
// declaration it depends on must be the one the table actually carries.
// A blueprint whose assumptions have drifted is refused before anything
// is applied, rather than binding whatever happened to be configured.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/blueprint"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// WorkflowOptions are the knobs `kmx workflow` shares.
type WorkflowOptions struct {
	// File is a blueprint an operator wrote. Empty means one kmx
	// carries, named by the positional argument — which is what keeps a
	// blueprint usable from a released binary with no checkout.
	File string
	// Set is the operator's parameters. Never a credential: the
	// blueprint's parser refuses a document that carries one, and there
	// is no flag here that could.
	Set map[string]string
	// Replace allows this command to overwrite governance that is
	// already on the cluster and does NOT match what the blueprint and
	// these parameters produce.
	Replace bool
}

// loadBlueprint resolves the blueprint an operator named.
func loadBlueprint(name string, opt WorkflowOptions) (*blueprint.Bundle, error) {
	if opt.File != "" {
		if name != "" {
			return nil, fmt.Errorf("name a blueprint OR pass --file, not both")
		}
		return blueprint.LoadFile(opt.File)
	}
	if name == "" {
		return nil, fmt.Errorf("name a blueprint kmx carries (kmx workflow list), or pass --file <path>")
	}
	return blueprint.Load(kaimahi.Blueprints, name)
}

// ListWorkflows prints the blueprints this binary carries. A read.
func (a *App) ListWorkflows() error {
	bundles, err := blueprint.Carried(kaimahi.Blueprints)
	if err != nil {
		return err
	}
	ui := cliui.New(a.Out)
	if ui.Rich() {
		rows := make([][]string, 0, len(bundles))
		for _, b := range bundles {
			rows = append(rows, []string{b.Name, b.Summary})
		}
		fmt.Fprintln(a.Out, ui.Heading("Workflows"))
		fmt.Fprintln(a.Out, ui.Table([]string{"NAME", "SUMMARY"}, rows))
		fmt.Fprintf(a.Out, "\n%s\n", ui.Actions("Your workflow", []cliui.Action{{Label: "Review a blueprint file", Command: "kmx workflow show --file ./my-workflow.yaml"}}))
		return nil
	}
	for _, b := range bundles {
		fmt.Fprintf(a.Out, "%-12s %s\n", b.Name, b.Summary)
	}
	fmt.Fprintf(a.Out, "\nA blueprint you wrote is a file: kmx workflow show --file ./my-workflow.yaml\n")
	return nil
}

// ShowWorkflow renders a blueprint against parameters and prints exactly
// what applying and running it would do. It touches no cluster, which is
// the point: what an operator reviews here is the same object the other
// two commands act on.
func (a *App) ShowWorkflow(name string, opt WorkflowOptions) error {
	b, err := loadBlueprint(name, opt)
	if err != nil {
		return err
	}
	// No step list: `show` runs nothing, so a parameter a STEP would need
	// is not demanded here. That matters for the release workflow, whose
	// build ids do not exist until its build step has run — being unable
	// to look at the shape of a workflow until you have values you can
	// only get by running it would be a poor way in.
	values, err := b.Bind(opt.Set, nil)
	if err != nil {
		var binding *blueprint.BindingError
		if !errors.As(err, &binding) || binding.Invalid {
			return err
		}
		// Missing parameters alone are an exploratory review, even when
		// some valid values were supplied. Invalid input is never success.
		fmt.Fprintf(a.Out, "%s\n\n%s\n\n%v\n\n", b.Name, b.Summary, err)
		return a.describeParameters(b)
	}
	r, err := b.RenderForReview(values, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "%s — %s\n", b.Name, b.Summary)
	fmt.Fprintf(a.Out, "source:     %s\n", b.Source)
	fmt.Fprintf(a.Out, "credential: %s\nagent:      %s\n\n", b.Credential, b.Agent)

	fmt.Fprintf(a.Out, "SEAMS (they must already be in the plane's table; a blueprint names them, never defines them)\n")
	for _, seam := range sortedSeamNames(b) {
		fmt.Fprintf(a.Out, "  %s\n", seam)
	}

	fmt.Fprintf(a.Out, "\nALLOWLIST for %q — these need no human and no bounds\n", b.Credential)
	for _, t := range r.Allowlist {
		fmt.Fprintf(a.Out, "  %s\n", t)
	}

	fmt.Fprintf(a.Out, "\nSTANDING BOUNDS for %q — inside these, a call proceeds with no human\n", b.Credential)
	fragment, err := r.Fragment()
	if err != nil {
		return err
	}
	if fragment == "" {
		fmt.Fprintf(a.Out, "  (none)\n")
	} else {
		fmt.Fprintf(a.Out, "%s", indent(fragment, "  "))
	}

	fmt.Fprintf(a.Out, "\nSTEPS\n")
	for _, s := range r.Steps {
		fmt.Fprintf(a.Out, "  %-28s %-14s", s.Label, s.Kind)
		switch {
		case s.Blocked != "":
			fmt.Fprintf(a.Out, "(needs more: %s)\n", s.Blocked)
		case s.Tool != "":
			fmt.Fprintf(a.Out, "%s\n", s.Summary())
		default:
			fmt.Fprintf(a.Out, "an agent turn\n")
		}
	}
	fmt.Fprintf(a.Out, "\n")
	if err := a.describeParameters(b); err != nil {
		return err
	}

	// The ungoverned steps get their own section rather than a line in
	// the table above. A blueprint that rendered every step in one
	// vocabulary would launder the distinction that matters: the
	// DECISION is governed, the TRANSFER is not.
	var ungoverned []blueprint.RenderedStep
	for _, s := range r.Steps {
		if s.Ungoverned != "" {
			ungoverned = append(ungoverned, s)
		}
	}
	if len(ungoverned) > 0 {
		fmt.Fprintf(a.Out, "\nNOT GOVERNED BY THE PLANE — these steps' ACTIONS run on this machine, outside the gateway.\n")
		fmt.Fprintf(a.Out, "The plane records that a human approved the DECISION. It meters nothing, sees no bytes,\n")
		fmt.Fprintf(a.Out, "and writes no tool-audit row for what actually moved.\n")
		for _, s := range ungoverned {
			fmt.Fprintf(a.Out, "\n  %s (%s)\n", s.Label, s.Summary())
			fmt.Fprintf(a.Out, "%s", indent(wrap(s.Ungoverned, 76), "    "))
			fmt.Fprintf(a.Out, "    runs: %s\n", describeExec(s.Exec))
			if len(s.Exec.Requires) > 0 {
				fmt.Fprintf(a.Out, "    needs on PATH: %s (kmx does not fetch these — see the blueprint)\n",
					strings.Join(s.Exec.Requires, ", "))
			}
		}
	}
	return nil
}

func (a *App) describeParameters(b *blueprint.Bundle) error {
	fmt.Fprintf(a.Out, "PARAMETERS\n")
	for _, name := range b.ParameterNames() {
		p := b.Parameters[name]
		flags := []string{p.Type}
		if p.Required {
			flags = append(flags, "required")
		}
		if len(p.RequiredFor) > 0 {
			flags = append(flags, "required for "+strings.Join(p.RequiredFor, ", "))
		}
		if p.Default != "" {
			flags = append(flags, "default "+p.Default)
		}
		fmt.Fprintf(a.Out, "  --set %s=…  (%s)\n%s", name, strings.Join(flags, ", "),
			indent(wrap(p.Help, 72), "      "))
	}
	return nil
}

// GovernWorkflow applies a blueprint's governance.
//
// Two artifacts, and nothing else: the credential's tool allowlist, and
// its standing constraints as an overlay fragment. It writes no
// upstream, no Secret, no credential and no agent — a workflow is
// governance over things that already exist, and the commands that create
// those (`kmx tools add`, `kmx tools govern`, the secret-capture scripts)
// are deliberately not folded in here.
func (a *App) GovernWorkflow(name string, opt WorkflowOptions) error {
	b, err := loadBlueprint(name, opt)
	if err != nil {
		return err
	}
	// The GOVERNANCE is the seams' allowlist and bounds; it uses no
	// parameter that only a step needs. Demanding those here would make
	// an operator supply a build id to set up a workflow that has not
	// run yet.
	values, err := b.Bind(opt.Set, nil)
	if err != nil {
		return err
	}

	fragments, version, err := a.readOverlay()
	if err != nil {
		return err
	}

	// A blueprint's constraints are keyed by CREDENTIAL, and the plane
	// refuses two overlay fragments defining the same one. A cluster
	// where somebody already ran `make release-bind` has exactly that
	// collision waiting, and the merge refusal would arrive as a proxy
	// that will not roll. Said here instead, with the fix.
	key := b.FragmentKey()
	for _, other := range sortedKeys(fragments) {
		if other == key {
			continue
		}
		constrains, err := fragmentConstrains(fragments[other], b.Credential)
		if err != nil {
			return fmt.Errorf("reading the overlay fragment %q: %w", other, err)
		}
		if constrains {
			return fmt.Errorf("the overlay already carries standing constraints for credential %q, in %q.\n"+
				"  The plane refuses two fragments defining one credential's constraints — the merge is per name "+
				"and refuses collisions rather than resolving by precedence, so applying this would take the next\n"+
				"  proxy rollout down. Remove the obsolete fragment first, then govern the workflow again.",
				b.Credential, other)
		}
	}

	var rendered *blueprint.Rendered
	var declared map[string][]string
	if err := a.session(func(c *admin.Client) error {
		// Render once with the blueprint's own declarations so there is
		// something to validate, then check it against the running table
		// and render again with the table's declared ORDER — which is
		// what the audit summary reads and therefore what the driver
		// will have to match a pending request on.
		r, err := b.Render(values, nil, nil)
		if err != nil {
			return err
		}
		fragment, err := r.Fragment()
		if err != nil {
			return err
		}
		candidate := map[string]string{}
		for k, v := range fragments {
			candidate[k] = v
		}
		if fragment != "" {
			candidate[key] = fragment
		}
		upstreams, decl, err := a.validateWorkflowOverlay(c, candidate)
		if err != nil {
			return err
		}
		declared = decl
		if err := checkSeams(b, upstreams, declared); err != nil {
			return err
		}
		// PRECHECKED, not discovered. This command writes the overlay
		// fragment and rolls the proxy, and only then sets the
		// allowlist — which is the one of the two the plane refuses for
		// a credential it does not have. Unchecked, that arrives as
		// `tool-allow failed (HTTP 404): no such credential` AFTER the
		// bounds are on the cluster and the proxy has restarted, and a
		// re-run repeats the mutation. It also left constraints that
		// would silently attach to a credential of that name created
		// later. Asked here, where "nothing has been applied" is still
		// true.
		if err := checkCredential(c, b.Credential, b.Agent); err != nil {
			return err
		}
		rendered, err = b.Render(values, nil, declared)
		return err
	}); err != nil {
		return err
	}

	fragment, err := rendered.Fragment()
	if err != nil {
		return err
	}

	// Imperative, with a precondition — not reconciled. There is no
	// controller here, and a command that silently reverted a constraint
	// somebody tightened by hand would take away the operator's own
	// escape hatch. So: identical is a no-op, different is refused with
	// the diff, and --replace is a deliberate act.
	unchanged := false
	remove := false
	switch existing, present := fragments[key]; {
	case present && fragment == "":
		// The blueprint and these parameters produce NO bounds, and the
		// cluster is holding some. Skipping the write would leave the
		// old constraints admitting calls while the operator believes
		// they applied this blueprint — the fail-open direction. The key
		// is deleted instead.
		if !opt.Replace {
			return fmt.Errorf("this blueprint and these parameters declare no standing bounds, and the cluster "+
				"holds some in %q.\n\n%s\n"+
				"  Applying would REMOVE them. Re-run with --replace if that is what you mean",
				key, indent(existing, "  "))
		}
		remove = true
		a.notef("These parameters declare no standing bounds; the ones on the cluster will be REMOVED.")
	case present && sameJSON(existing, fragment) && !opt.Replace:
		// Writing the identical bytes back would still roll the proxy,
		// which is a real outage window spent on a no-op. With --replace
		// it is NOT skipped: that is the recovery the warning below
		// advertises for a run that wrote the key and then failed while
		// rolling, and an advertised recovery that quietly does nothing
		// is worse than none.
		unchanged = true
		a.notef("The standing bounds on the cluster are already exactly these; nothing to write.")
	case present && !opt.Replace:
		diff, derr := jsonDiff(existing, fragment)
		if derr != nil {
			return derr
		}
		return fmt.Errorf("the cluster's %q does not match this blueprint and these parameters:\n\n%s\n"+
			"  kmx applies a blueprint; it does not reconcile one. Something changed the bounds — a hand edit, "+
			"or a different --set. Re-run with --replace to make the cluster match this, or change the "+
			"parameters to match the cluster", key, diff)
	}

	if err := a.Guard(fmt.Sprintf("apply workflow %q's governance to credential %q (allowlist of %d tools, "+
		"standing bounds on %d)", b.Name, b.Credential, len(rendered.Allowlist), countBounds(rendered)),
		"kmx workflow govern "+b.Name); err != nil {
		return err
	}

	switch {
	case remove:
		if err := a.removeOverlayFragment(key, version); err != nil {
			return err
		}
		if err := a.rollProxy(); err != nil {
			return err
		}
		a.notef("Removed %s/%s. Every call those bounds used to admit is denied and filed again.",
			scaffold.OverlayConfigMap, key)
	case fragment != "" && !unchanged:
		if err := a.writeOverlayFragment(key, fragment, version); err != nil {
			return err
		}
		if err := a.rollProxy(); err != nil {
			return err
		}
		a.notef("Standing bounds written to %s/%s. They survive `kmx plane`, which reapplies the base table only.",
			scaffold.OverlayConfigMap, key)
	case unchanged:
		// The fragment matching is not proof that the PLANE is serving
		// it: a previous run could have written the key and then failed
		// while rolling. Checking the rollout is cheap and does not
		// restart anything, and an incomplete one is said out loud
		// rather than skipped past.
		if err := a.kubectlRun("-n", scaffold.PlaneNamespace, "rollout", "status",
			"deploy/kaimahi-proxy", "--timeout=60s"); err != nil {
			a.notef("WARNING: the bounds on the cluster match this blueprint, but the proxy's rollout is not")
			a.notef("  complete (%v). If an earlier run failed while rolling, the plane may still be enforcing", err)
			a.notef("  the PREVIOUS table. Force one with: %s --replace", a.workflowCommand("govern", b.Name, opt))
		}
	}

	if err := a.session(func(c *admin.Client) error {
		return a.setToolAllowlist(c, b.Credential, rendered.Allowlist)
	}); err != nil {
		return err
	}

	a.notef("")
	seams := sortedSeamNames(b)
	verb := "is"
	if len(seams) != 1 {
		verb = "are"
	}
	a.notef("Workflow %q is governed. What is NOT governed, and this command did not do:", b.Name)
	pronoun := "it"
	if len(seams) != 1 {
		pronoun = "them"
	}
	a.notef("  - the seams themselves. %s %s already in the plane's table; a blueprint names %s.",
		strings.Join(seams, " and "), verb, pronoun)
	a.notef("  - the credential %q and its Secret. That is `kmx tools govern`.", b.Credential)
	a.notef("  - any credential material at all.")
	a.notef("Run it with: %s --dry-run", a.workflowCommand("run", b.Name, opt))
	a.notef("Supply any additional step parameters reported as missing before running live.")
	return nil
}

func (a *App) workflowCommand(verb, name string, opt WorkflowOptions) string {
	args := []string{"workflow", verb}
	if opt.File != "" {
		args = append(args, "--file", opt.File)
	} else {
		args = append(args, name)
	}
	for _, key := range sortedKeys(opt.Set) {
		args = append(args, "--set", key+"="+opt.Set[key])
	}
	return a.operationCommand(args...)
}

// checkCredential is the assertion a blueprint makes about the plane's
// custody: the credential its governance is written FOR has to exist
// before anything is written. It names how to create one rather than
// returning an HTTP status, which would tell an operator nothing they can
// act on.
func checkCredential(c *admin.Client, credential, agent string) error {
	names, err := c.CredentialNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == credential {
			return nil
		}
	}
	have := "none at all"
	if len(names) > 0 {
		have = strings.Join(names, ", ")
	}
	return fmt.Errorf("the plane has no credential %q, and this blueprint's governance is written for it — "+
		"the allowlist is set on it and the standing bounds are keyed by it.\n"+
		"  Nothing has been applied. The plane holds: %s\n"+
		"  Issue it, and bind it to the agent's Secret, with:\n"+
		"    kmx tools govern --credential %s --agent %s --secret <secret-name>\n"+
		"  A blueprint governs a credential that already exists; it never creates one, and kmx never handles "+
		"the token", credential, have, credential, agent)
}

// checkSeams is the assertion a blueprint makes about the world.
func checkSeams(b *blueprint.Bundle, upstreams []string, declared map[string][]string) error {
	have := map[string]bool{}
	for _, u := range upstreams {
		have[u] = true
	}
	for _, seam := range sortedSeamNames(b) {
		if !have[seam] {
			return fmt.Errorf("blueprint %q uses the upstream %q, and the plane's table does not have it "+
				"(it has: %s).\n"+
				"  A blueprint NAMES seams; it cannot create them. An in-cluster server is onboarded with "+
				"`kmx tools add %s --url …`; a hosted or keyed one belongs in the committed table, because "+
				"plane/internal/config/overlay.go refuses an overlay entry that names a credential or reaches "+
				"outside the cluster",
				b.Name, seam, strings.Join(upstreams, ", "), seam)
		}
		for _, tool := range sortedAny(b.Seams[seam].Requires) {
			want := b.Seams[seam].Requires[tool]
			got, ok := declared[tool]
			if !ok {
				return fmt.Errorf("blueprint %q requires %s to declare the policy fields [%s], and the plane's "+
					"table declares none for it.\n"+
					"  An undeclared tool binds its WHOLE argument object, which is exact and brittle — an "+
					"approval would not admit a semantically identical retry. Declare it in the table",
					b.Name, tool, strings.Join(want, ", "))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				return fmt.Errorf("blueprint %q requires %s to bind [%s], and the plane's table binds [%s].\n"+
					"  Nothing has been applied. An approval binds the DECLARED fields in the DECLARED order, so "+
					"these two disagree about what a human would be approving. Fix whichever is wrong",
					b.Name, tool, strings.Join(want, ", "), strings.Join(got, ", "))
			}
		}
	}
	return nil
}

// validateWorkflowOverlay asks the RUNNING plane whether the candidate
// overlay would load, and returns what it says the table declares. It is
// the same endpoint `kmx tools add` uses, for the same reason: the answer
// comes from the parser the proxy boots with, not from a second copy of
// it.
func (a *App) validateWorkflowOverlay(c *admin.Client, fragments map[string]string) ([]string, map[string][]string, error) {
	// Ask whether this plane can answer the question at all, before asking
	// it. The check that used to live at the bottom of this function
	// inferred the plane's age from a missing response field, which meant
	// the request was sent first and the diagnosis rested on one field.
	if err := c.Require(admin.ContractTableDeclared,
		"check a workflow's `requires` against its upstream table"); err != nil {
		return nil, nil, err
	}
	body := map[string]any{"fragments": map[string]json.RawMessage{}}
	frags := body["fragments"].(map[string]json.RawMessage)
	for name, raw := range fragments {
		frags[name] = json.RawMessage(raw)
	}
	status, out, err := c.Do("POST", "/admin/config/validate", body)
	if err != nil {
		return nil, nil, err
	}
	var resp struct {
		OK            bool                `json:"ok"`
		Error         string              `json:"error"`
		ToolUpstreams []string            `json:"tool_upstreams"`
		Declared      map[string][]string `json:"declared"`
		TableDeclared map[string][]string `json:"table_declared"`
	}
	_ = json.Unmarshal(out, &resp)
	if status != 200 || !resp.OK {
		msg := resp.Error
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		return nil, nil, fmt.Errorf("the plane refused this governance — nothing has been applied:\n  %s", msg)
	}
	// `declared` answers only about the submitted overlay — the endpoint
	// was built for `kmx tools add`, which is onboarding the tools it is
	// asking about. A blueprint NAMES seams it did not define, so the
	// question it needs answered is about the whole merged table.
	//
	// Reaching here with no table means a plane that DECLARED it serves this
	// and then did not, which is a different fault from an old plane and gets
	// a different sentence: the version handshake above already turned the
	// age case into a message naming both versions and the fix. This stays
	// because applying a blueprint unchecked would bind whatever happened to
	// be configured, and that must fail closed however the field went
	// missing.
	if resp.TableDeclared == nil {
		return nil, nil, fmt.Errorf("this plane reports admin contract %d, which promises the policy fields of "+
			"its merged upstream table, and then did not return them — so a workflow's `requires` cannot be "+
			"checked and applying it unchecked would bind whatever happened to be configured.\n"+
			"  Nothing has been applied. This is a fault in the plane, not a version gap: %s.",
			c.Plane().Contract, c.Plane().Describe())
	}
	return resp.ToolUpstreams, resp.TableDeclared, nil
}

// writeOverlayFragment sets ONE key with a merge patch, never a
// create-or-replace: other operators' fragments live in the same
// ConfigMap, and `kmx tools add`'s whole-map apply is the other half of
// the same rule (it emits every fragment it read).
func (a *App) writeOverlayFragment(key, fragment, version string) error {
	if _, err := a.kubectlCapture("-n", scaffold.PlaneNamespace, "get", "configmap",
		scaffold.OverlayConfigMap, "-o", "name"); err != nil {
		if !isNotFound(err) {
			return err
		}
		if err := a.kubectlRun("-n", scaffold.PlaneNamespace, "create", "configmap",
			scaffold.OverlayConfigMap); err != nil {
			return err
		}
		// A ConfigMap this command just created has a version nobody
		// read, and nobody else can have raced a fragment into it. Read
		// it so the write below still carries a precondition.
		if _, now, err := a.readOverlay(); err != nil {
			return err
		} else {
			version = now
		}
	} else if _, now, err := a.readOverlay(); err != nil {
		return err
	} else if now != version {
		return fmt.Errorf("the overlay changed while this was being prepared (read at %s, now %s) — nothing "+
			"has been applied. Somebody else onboarded an upstream or edited a fragment; run the same command "+
			"again to build on their change", quoteVersion(version), quoteVersion(now))
	}

	patch, err := overlayPatch(version, map[string]any{key: fragment})
	if err != nil {
		return err
	}
	// Through a 0600 file rather than argv: the patch is not secret, but
	// every other cluster write in this repo goes through a file and a
	// habit that has an exception is not a habit.
	dir, err := os.MkdirTemp("", "kmx-workflow")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "patch.json")
	if err := os.WriteFile(path, patch, 0o600); err != nil {
		return err
	}
	return a.kubectlRun("-n", scaffold.PlaneNamespace, "patch", "configmap", scaffold.OverlayConfigMap,
		"--type", "merge", "--patch-file", path)
}

// removeOverlayFragment deletes ONE key. A null value in a merge patch
// deletes the key and leaves every other operator's fragment alone —
// which is why this is a patch and never a create-or-replace.
func (a *App) removeOverlayFragment(key, version string) error {
	if _, now, err := a.readOverlay(); err != nil {
		return err
	} else if now != version {
		return fmt.Errorf("the overlay changed while this was being prepared (read at %s, now %s) — nothing "+
			"has been removed", quoteVersion(version), quoteVersion(now))
	}
	patch, err := overlayPatch(version, map[string]any{key: nil})
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kmx-workflow")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "patch.json")
	if err := os.WriteFile(path, patch, 0o600); err != nil {
		return err
	}
	return a.kubectlRun("-n", scaffold.PlaneNamespace, "patch", "configmap", scaffold.OverlayConfigMap,
		"--type", "merge", "--patch-file", path)
}

// overlayPatch builds a merge patch that carries the resourceVersion as
// a REAL precondition rather than only as something re-read a moment
// earlier. A merge patch naming a stale version is refused with a
// Conflict, which closes the window between the check and the write.
func overlayPatch(version string, data map[string]any) ([]byte, error) {
	patch := map[string]any{"data": data}
	if version != "" {
		patch["metadata"] = map[string]string{"resourceVersion": version}
	}
	return json.Marshal(patch)
}

// fragmentConstrains reports whether an overlay fragment already carries
// standing constraints for a credential.
//
// A fragment that does not parse answers UNKNOWN, not "no collision".
// This guard exists so a merge refusal does not arrive as a proxy that
// will not roll; a hand edit that broke the JSON is exactly the case
// where that would happen, and reading it as "nothing there" would
// deliver the outage the guard was written to prevent, under a success
// message.
func fragmentConstrains(raw, credential string) (bool, error) {
	var doc struct {
		StandingConstraints map[string]json.RawMessage `json:"standing_constraints"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return false, fmt.Errorf("an overlay fragment does not parse as JSON: %w.\n"+
			"  kmx will not read that as \"it constrains nothing\" — the plane parses every fragment at boot, "+
			"so this one would take the next rollout down whatever is applied on top of it. Fix or remove it "+
			"first", err)
	}
	_, ok := doc.StandingConstraints[credential]
	return ok, nil
}

func countBounds(r *blueprint.Rendered) int {
	n := 0
	for _, tools := range r.Constraints {
		n += len(tools)
	}
	return n
}

func sortedSeamNames(b *blueprint.Bundle) []string { return sortedAny(b.Seams) }

// sortedAny is sortedKeys for a map of anything; plane.go's predates
// generics being needed here and keeps its own narrower signature.
func sortedAny[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sameJSON(a, b string) bool {
	na, ea := blueprint.Normalize([]byte(a))
	nb, eb := blueprint.Normalize([]byte(b))
	return ea == nil && eb == nil && string(na) == string(nb)
}

// jsonDiff renders both sides normalised, which is what makes a
// difference readable rather than a wall of reordered keys.
func jsonDiff(have, want string) (string, error) {
	nh, err := blueprint.Normalize([]byte(have))
	if err != nil {
		return "", err
	}
	nw, err := blueprint.Normalize([]byte(want))
	if err != nil {
		return "", err
	}
	return "  --- on the cluster\n" + indent(string(nh), "  ") +
		"  --- from this blueprint\n" + indent(string(nw), "  "), nil
}

func indent(s, with string) string {
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out.WriteString(with)
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func wrap(s string, width int) string {
	words := strings.Fields(s)
	var out strings.Builder
	col := 0
	for _, w := range words {
		if col > 0 && col+1+len(w) > width {
			out.WriteByte('\n')
			col = 0
		} else if col > 0 {
			out.WriteByte(' ')
			col++
		}
		out.WriteString(w)
		col += len(w)
	}
	if col > 0 {
		out.WriteByte('\n')
	}
	return out.String()
}

func describeExec(e *blueprint.RenderedExec) string {
	if e == nil {
		return ""
	}
	if e.Script != "" {
		return "the bundled script " + e.Script + " " + strings.Join(e.Args, " ")
	}
	return strings.Join(append(append([]string{}, e.Command...), e.Args...), " ")
}
