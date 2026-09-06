// Package blueprint is one declarative file that says what a governed
// workflow REACHES, what its policy is, and what its steps are — so that
// intent the release workflow spread over four layers (docs/release-agent.md)
// has one place to live and one command to apply.
//
// What a blueprint is NOT, and the boundaries are the design:
//
//   - It is not a credential store. A blueprint NAMES Kubernetes Secrets
//     and never carries token material. The parser refuses a
//     document that looks like it is carrying one, because a declarative
//     format that wants to be self-contained is exactly where that rule
//     gets bent.
//   - It is not an upstream table. `plane/internal/config/overlay.go`
//     refuses an overlay entry that sets `credential_file`,
//     `credential_header`, `internet`, `ca_file` or `extra_headers`,
//     because together they are an exfiltration primitive. A blueprint
//     therefore NAMES upstreams that already exist in the merged table
//     and ASSERTS their policy_fields; it cannot create a hosted or keyed
//     seam, and must not gain the ability to.
//   - It is not a program. Steps are a fixed vocabulary, commands are
//     argv arrays (never a shell string), and there is no expression
//     language — the same restraint the standing constraints take.
//
// The one property everything else is arranged around: THE DRIVER FILES
// THE APPROVAL REQUEST, for the call the blueprint and the operator's
// parameters name, and a model cannot influence what a human is shown.
// The accounts-payable demo paid for that lesson. So `call.args` may
// reference PARAMETERS and literals only — never a value an agent turn
// produced — and the parser enforces it rather than documenting it.
package blueprint

import (
	"fmt"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Version is the only `blueprint:` value this kmx understands. A
// blueprint decides what an agent may call and what an approval binds;
// guessing at a document from a future version is not a thing to do
// quietly.
const Version = "v1"

// Blueprint is one governed workflow, as written.
type Blueprint struct {
	Version string `yaml:"blueprint"`
	Name    string `yaml:"name"`
	Summary string `yaml:"summary"`

	// Credential is the plane credential the workflow's calls are made
	// under: the allowlist is written for it, the standing constraints
	// are keyed by it, and the audit trail reads under it.
	Credential string `yaml:"credential"`
	// Agent is the kagent Agent the steps' turns are addressed to.
	Agent string `yaml:"agent"`

	// Parameters are the operator's configuration — a repository, an
	// organization, a version. They are declared here and SUPPLIED at
	// apply time, which is what keeps somebody's real repository and
	// somebody's Azure organization out of this repository's tree.
	Parameters map[string]Parameter `yaml:"parameters"`

	// Seams name upstreams that must already exist in the plane's merged
	// table, and say what this workflow needs from each.
	Seams map[string]Seam `yaml:"seams"`

	// Steps are the ordered work.
	Steps []Step `yaml:"steps"`
}

// Parameter is one operator-supplied value.
type Parameter struct {
	// Type is one of ParameterTypes.
	Type string `yaml:"type"`
	// Help is shown when the parameter is missing. Required: a parameter
	// whose absence produces "missing parameter ado_project" and nothing
	// else is a worse interface than the make variable it replaced.
	Help string `yaml:"help"`
	// Pattern is an anchored regexp the supplied value must match. A
	// parameter reaches argv, a policy field and a request summary, so a
	// shape is declared rather than hoped for.
	Pattern string `yaml:"pattern"`
	// Required parameters must be supplied for any step to run.
	Required bool `yaml:"required"`
	// RequiredFor names the steps that cannot run without this
	// parameter. It is how a RUN-TIME value — the build ids the publish
	// step attaches, produced by a step that ran earlier — is expressed
	// without letting an agent supply it: the operator passes it on the
	// resumed run, and the driver files the approval from that.
	RequiredFor []string `yaml:"required_for"`
	// Needs names other parameters that must be supplied whenever this
	// one is.
	Needs []string `yaml:"needs"`
	// Default is used when the operator supplies nothing. It may
	// reference other parameters (`release/${version}`).
	Default string `yaml:"default"`
}

// Seam is what the workflow needs from one upstream in the plane's table.
type Seam struct {
	// Requires is the policy_fields declaration this blueprint DEPENDS
	// on, per tool, in the order the audit summary reads them. kmx
	// asserts it against the running table and refuses on disagreement;
	// it does not write it, because for a hosted or keyed upstream only
	// the committed table may declare it.
	Requires map[string][]string `yaml:"requires"`
	// Allow is this workflow's contribution to the credential's tool
	// allowlist: the calls that need no human and no bounds. The
	// allowlist is per-credential and not per-upstream, so the seams'
	// lists union — and two seams naming one tool is refused.
	Allow []string `yaml:"allow"`
	// Bound is the standing constraints: declarative bounds under
	// which a call proceeds with no approval.
	Bound map[string][]Constraint `yaml:"bound"`
	// Refresh is how a credential that expires mid-run is re-minted.
	Refresh *Refresh `yaml:"refresh"`
}

// Constraint is one bound on one declared field, in the plane's own
// vocabulary (plane/internal/config/policy.go). `when` is the only
// conditional: a block that exists only when the operator supplied the
// parameter it is written from.
type Constraint struct {
	Field  string `yaml:"field"`
	Op     string `yaml:"op"`
	Value  string `yaml:"value"`
	Values string `yaml:"values"`
	When   string `yaml:"when"`
	// Literal forces the JSON type of the constraint's literal, for the
	// case a field is an integer and the operator is `eq`. Default:
	// integers for the numeric operators and for a list drawn from an
	// int_list parameter, strings otherwise.
	Literal string `yaml:"literal"`

	valuesSet bool
}

// Refresh re-mints an expiring upstream credential into plane custody.
//
// It runs a command on the OPERATOR's machine, and that is the whole
// reason it works: `az` is already logged in there. kmx deliberately does
// NOT provision these binaries the way internal/kmx/toolchain provisions
// kubectl, kind and helm — those are pinned, checksum-verified release
// artifacts whose identity IS the checksum, whereas a freshly downloaded
// `az` is a binary with nobody logged into it. It buys nothing and adds a
// supply chain. So a refresh command is REQUIRED on PATH, named when it
// is missing, and the run continues with the credential already in
// custody rather than failing — which is what the shell release driver does.
type Refresh struct {
	// Command is argv. Never a shell string: no word splitting, no
	// globbing, no interpolation the operator did not write.
	Command []string `yaml:"command"`
	// Secret and Key are where the minted value is stored — a NAME and a
	// key, never a value.
	Secret string `yaml:"secret"`
	Key    string `yaml:"key"`
	// Requires is the binary the command needs on PATH.
	Requires string `yaml:"requires"`
	// Why is printed when the refresh happens.
	Why string `yaml:"why"`
	// Seam names the kagent RemoteMCPServer whose Accepted condition is
	// re-checked after a refresh. kagent caches a reconcile result, so a
	// seam that failed on the OLD credential still reads Unauthorized
	// minutes later — which is how the release workflow's first health
	// check reported a healthy credential as broken.
	Seam string `yaml:"seam"`
}

// Step kinds. The vocabulary is small on purpose: every kind is a
// different answer to "who is interrupted, and what is bound".
const (
	// KindRead is an agent turn that only reads. No approval: the reads
	// are admitted by the allowlist.
	KindRead = "read"
	// KindPropose is an agent turn whose REPLY is an artifact a human
	// reads before approving something. Nothing is created.
	KindPropose = "propose"
	// KindConsequential is the governed shape: the driver files a
	// request for the exact call, a human approves it, and only then can
	// the agent make it — or, for a step with an `exec` action, only
	// then does the driver run it.
	KindConsequential = "consequential"
	// KindBounded is a call with consequences that a STANDING CONSTRAINT
	// admits: it proceeds with no human, inside declared bounds, and is
	// audited. It exists as its own kind because "bounded" and
	// "consequential" are two different answers to the same question and
	// the release workflow gives BOTH for one tool — `scripts/release-bind.sh`
	// bounds
	// `pipelines_write` ("builds are bounded, not approved",
	// docs/release-agent.md) while `scripts/release-run.sh` files an
	// approval request for it. Naming the posture turns that into a
	// validation error instead of a contradiction spread across a shell
	// script and a document.
	KindBounded = "bounded"
	// KindPoll is a bounded wait: an agent turn asking for a status,
	// repeated on an interval until it reports a terminal state.
	KindPoll = "poll"
)

// StepKinds is the vocabulary, for messages and completion.
var StepKinds = []string{KindRead, KindPropose, KindConsequential, KindBounded, KindPoll}

// Step is one unit of the workflow.
type Step struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
	// Prompt is the task text for the agent turn. Substituted with
	// parameters; may also reference a previous step's captured reply,
	// because prose is not what an approval binds.
	Prompt string `yaml:"prompt"`
	// Capture names this step's reply so later steps can use it.
	Capture string `yaml:"capture"`
	// Call is the exact call a consequential or bounded step is about.
	// The driver files the request from THIS, never from anything the
	// agent said.
	Call *Call `yaml:"call"`
	// ForEach names a list parameter the step is repeated over, with
	// ${item} bound to each element.
	ForEach string `yaml:"for_each"`
	// Poll bounds a `poll` step.
	Poll *PollSpec `yaml:"poll"`
	// Exec makes this step's ACTION a command on the operator's machine
	// rather than a tool call. Only on a consequential step, and only
	// with `call.ungoverned` set.
	Exec *Exec `yaml:"exec"`
	// When makes the step conditional on a parameter having been
	// supplied.
	When string `yaml:"when"`
}

// Call is one governed call: which upstream's tool, and the exact
// arguments. Arguments may reference parameters and literals ONLY.
type Call struct {
	Upstream string            `yaml:"upstream"`
	Tool     string            `yaml:"tool"`
	Args     map[string]string `yaml:"args"`
	// Types gives an argument a JSON type other than string. The gateway
	// canonicalises what it receives and the digest is over that, so an
	// integer sent as "41" and one sent as 41 are DIFFERENT calls — the
	// approval a human gave for one would not admit the other. YAML
	// cannot carry the type through `${…}` substitution, so it is
	// declared: `types: {pipelineId: int}`.
	Types map[string]string `yaml:"types"`
	// Ungoverned marks the step whose DECISION is governed and whose
	// ACTION is not: the driver's own publish, which moves bytes with the
	// operator's own tools, outside the gateway. It is REQUIRED on any
	// step with `exec`, and it must carry a reason, because a format that
	// rendered every step in one vocabulary would launder the distinction
	// docs/release-agent.md draws rather than glosses (1.28 GB across five
	// assets, moved by `az` and `gh`).
	Ungoverned string `yaml:"ungoverned"`
}

// Exec is an action the DRIVER performs, after a human approved the
// call the step declares.
type Exec struct {
	// Command is argv for a binary on PATH.
	Command []string `yaml:"command"`
	// Script names a script the blueprint's bundle carries. kmx
	// materialises it into a 0700 file and runs it — which is what keeps
	// a blueprint runnable from a released binary with no checkout.
	Script string `yaml:"script"`
	// Args are appended to Command, or passed to Script.
	Args []string `yaml:"args"`
	// Env is the environment the action runs with. Values may reference
	// captures, including `${capture.<name>.file}` for a path to the
	// captured text.
	Env map[string]string `yaml:"env"`
	// Requires are binaries that must be on PATH. Named, never fetched.
	Requires []string `yaml:"requires"`
	// Preview is an argv suffix that makes the action LIST what it would
	// do without doing it. The driver runs it before the approval, so
	// whoever decides sees what lands rather than a count they take on
	// trust.
	Preview []string `yaml:"preview"`
}

// PollSpec bounds a poll step.
type PollSpec struct {
	IntervalSeconds int    `yaml:"interval_seconds"`
	TimeoutSeconds  int    `yaml:"timeout_seconds"`
	Done            string `yaml:"done"`
	Failed          string `yaml:"failed"`
	Running         string `yaml:"running"`
	// OnFailure is an extra turn run when Failed matches, to read the
	// log before giving up.
	OnFailure string `yaml:"on_failure"`
}

// UnmarshalYAML records whether `values` was present, which "" cannot.
func (c *Constraint) UnmarshalYAML(node *yaml.Node) error {
	type plain struct {
		Field   string `yaml:"field"`
		Op      string `yaml:"op"`
		Value   string `yaml:"value"`
		Values  string `yaml:"values"`
		When    string `yaml:"when"`
		Literal string `yaml:"literal"`
	}
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	c.Field, c.Op, c.Value, c.Values = p.Field, p.Op, p.Value, p.Values
	c.When, c.Literal = p.When, p.Literal
	// Parse enables KnownFields on ITS decoder, and this one is a
	// different decoder — so an unknown key inside a constraint would be
	// silently dropped, which is the one thing this parser refuses to do
	// anywhere else. A misspelled `feild:` should be a message, not a
	// constraint that quietly binds something else.
	known := map[string]bool{"field": true, "op": true, "value": true, "values": true,
		"when": true, "literal": true}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !known[key] {
			return fmt.Errorf("line %d: a constraint has no %q field (it takes field, op, value, values, when, literal)",
				node.Content[i].Line, key)
		}
		if key == "values" {
			c.valuesSet = true
		}
	}
	return nil
}

// Parse reads a blueprint document and validates everything that can be
// known without a cluster. Anything a cluster is needed for — that the
// upstreams exist, that their declarations match — is Rendered.Check.
func Parse(raw []byte) (*Blueprint, error) {
	if err := refuseCredentialMaterial(raw); err != nil {
		return nil, err
	}
	var b Blueprint
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("blueprint: %w", err)
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	return &b, nil
}

func (b *Blueprint) validate() error {
	if b.Version != Version {
		return fmt.Errorf("blueprint: `blueprint: %s` — this kmx understands %q only. A blueprint decides "+
			"what an agent may call; a document from another version is not something to interpret optimistically",
			quoteOrAbsent(b.Version), Version)
	}
	for _, f := range []struct{ value, field string }{
		{b.Name, "name"}, {b.Credential, "credential"}, {b.Agent, "agent"},
	} {
		if !dnsLabelRE.MatchString(f.value) {
			return fmt.Errorf("blueprint: `%s: %s` is not a lowercase DNS label (it becomes a ConfigMap key, "+
				"a credential name or a Kubernetes object name)", f.field, quoteOrAbsent(f.value))
		}
	}
	if strings.TrimSpace(b.Summary) == "" {
		return fmt.Errorf("blueprint %q: `summary` is required — it is what `kmx workflow list` shows and what "+
			"an operator reads before running somebody else's workflow", b.Name)
	}
	if len(b.Seams) == 0 {
		return fmt.Errorf("blueprint %q: no seams. A workflow that reaches nothing has nothing to govern", b.Name)
	}
	if len(b.Steps) == 0 {
		return fmt.Errorf("blueprint %q: no steps", b.Name)
	}
	if err := b.validateParameters(); err != nil {
		return err
	}
	if err := b.validateSeams(); err != nil {
		return err
	}
	return b.validateSteps()
}

func (b *Blueprint) validateParameters() error {
	for _, name := range b.ParameterNames() {
		p := b.Parameters[name]
		if !paramNameRE.MatchString(name) {
			return fmt.Errorf("blueprint %q: %q is not a parameter name (lowercase letters, digits, underscores)", b.Name, name)
		}
		if !contains(ParameterTypes, p.Type) {
			return fmt.Errorf("blueprint %q: parameter %q has type %s (want one of %s)",
				b.Name, name, quoteOrAbsent(p.Type), strings.Join(ParameterTypes, ", "))
		}
		if strings.TrimSpace(p.Help) == "" {
			return fmt.Errorf("blueprint %q: parameter %q has no `help`. It is the whole message an operator "+
				"gets when they leave it out", b.Name, name)
		}
		if p.Pattern != "" {
			if _, err := compilePattern(p.Pattern); err != nil {
				return fmt.Errorf("blueprint %q: parameter %q: %w", b.Name, name, err)
			}
		}
		if p.Required && p.Default != "" {
			return fmt.Errorf("blueprint %q: parameter %q is required AND defaulted; a default means it is not required", b.Name, name)
		}
		for _, ref := range references(p.Default) {
			base, _, _ := strings.Cut(ref, ".")
			if base == name {
				return fmt.Errorf("blueprint %q: parameter %q's default refers to itself", b.Name, name)
			}
			if err := b.knownReference(ref, fmt.Sprintf("blueprint %q: parameter %q default", b.Name, name)); err != nil {
				return err
			}
			if b.Parameters[base].Default != "" && references(b.Parameters[base].Default) != nil {
				return fmt.Errorf("blueprint %q: parameter %q's default refers to %q, which is itself a "+
					"computed default. Defaults resolve in one pass, so chains are refused rather than "+
					"depending on ordering", b.Name, name, base)
			}
		}
		for _, need := range p.Needs {
			if _, ok := b.Parameters[need]; !ok {
				return fmt.Errorf("blueprint %q: parameter %q needs %q, which is not declared", b.Name, name, need)
			}
		}
		for _, step := range p.RequiredFor {
			if !b.hasStep(step) {
				return fmt.Errorf("blueprint %q: parameter %q is required_for step %q, which does not exist", b.Name, name, step)
			}
		}
	}
	return nil
}

func (b *Blueprint) validateSeams() error {
	// The allowlist is per-CREDENTIAL, not per-(credential, upstream) —
	// a documented property of the gateway, and the one way onboarding
	// can widen something without an allowlist edit. Two seams naming
	// one tool would produce a single allowlist entry admitting it on
	// BOTH servers. Refused here, where the message can name both.
	allowedBy := map[string]string{}
	boundBy := map[string]string{}
	for _, name := range sortedSeams(b.Seams) {
		seam := b.Seams[name]
		if !upstreamNameRE.MatchString(name) {
			return fmt.Errorf("blueprint %q: %q is not an upstream name", b.Name, name)
		}
		if len(seam.Requires) == 0 {
			return fmt.Errorf("blueprint %q: seam %q declares no `requires`. A blueprint states the "+
				"policy_fields it depends on so kmx can refuse when the running table disagrees; without it "+
				"the workflow binds whatever happened to be configured", b.Name, name)
		}
		for _, tool := range sortedKeys(seam.Requires) {
			if !toolNameRE.MatchString(tool) {
				return fmt.Errorf("blueprint %q: seam %q: %q is not a tool name", b.Name, name, tool)
			}
			for _, f := range seam.Requires[tool] {
				if !policyFieldRE.MatchString(f) {
					return fmt.Errorf("blueprint %q: seam %q: %s: %q is not a top-level argument field name. "+
						"Kaimahi's policy vocabulary is top-level names only — a value nested inside an object "+
						"is not addressable (plane/internal/config/policy.go)", b.Name, name, tool, f)
				}
			}
		}
		for _, tool := range seam.Allow {
			if _, ok := seam.Requires[tool]; !ok {
				return fmt.Errorf("blueprint %q: seam %q allows %s but does not declare its policy fields under `requires`",
					b.Name, name, tool)
			}
			if from, ok := allowedBy[tool]; ok {
				return fmt.Errorf("blueprint %q: %s is allowed by seams %q and %q. The gateway's allowlist is "+
					"per-credential, not per-upstream, so one entry would admit it on BOTH servers", b.Name, tool, from, name)
			}
			allowedBy[tool] = name
		}
		for _, tool := range sortedKeys(seam.Bound) {
			cs := seam.Bound[tool]
			if _, ok := seam.Requires[tool]; !ok {
				return fmt.Errorf("blueprint %q: seam %q bounds %s but does not declare its policy fields under `requires`",
					b.Name, name, tool)
			}
			if from, ok := boundBy[tool]; ok {
				return fmt.Errorf("blueprint %q: %s is bounded by seams %q and %q; standing constraints are keyed "+
					"by credential and tool, so one would silently replace the other", b.Name, tool, from, name)
			}
			boundBy[tool] = name
			if len(cs) == 0 {
				return fmt.Errorf("blueprint %q: seam %q: `bound: %s` lists no constraints. An empty bound is not "+
					"a bound — remove it, or state the constraint", b.Name, name, tool)
			}
			for i, c := range cs {
				if err := b.validateConstraint(name, tool, i, c, seam.Requires[tool]); err != nil {
					return err
				}
			}
		}
		if seam.Refresh != nil {
			if err := b.validateRefresh(name, seam.Refresh); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Blueprint) validateConstraint(seam, tool string, i int, c Constraint, declared []string) error {
	where := fmt.Sprintf("blueprint %q: seam %q: bound %s[%d]", b.Name, seam, tool, i)
	if !policyFieldRE.MatchString(c.Field) {
		return fmt.Errorf("%s: %q is not a field name", where, c.Field)
	}
	// A constraint on a field the tool does not declare is refused by the
	// plane at LOAD, which takes the rollout down. Caught here, where the
	// failure is one message instead of a failed deploy.
	if !contains(declared, c.Field) {
		return fmt.Errorf("%s: %s does not declare %q as policy-relevant, so a constraint on it could not be "+
			"enforced — the plane refuses the whole table at boot. Declared: %s",
			where, tool, c.Field, strings.Join(declared, ", "))
	}
	if !validOp(c.Op) {
		return fmt.Errorf("%s: %q is not an operator (want %s)", where, c.Op, strings.Join(ConstraintOps, ", "))
	}
	setOp := c.Op == OpIn || c.Op == OpNotIn
	switch {
	case setOp && !c.valuesSet:
		return fmt.Errorf("%s: op %q needs `values`", where, c.Op)
	case !setOp && c.valuesSet:
		return fmt.Errorf("%s: op %q takes `value`, not `values`", where, c.Op)
	case !setOp && c.Value == "":
		return fmt.Errorf("%s: op %q needs a `value`", where, c.Op)
	}
	if c.Literal != "" && c.Literal != LiteralString && c.Literal != LiteralInt {
		return fmt.Errorf("%s: `literal: %s` (want %s or %s)", where, c.Literal, LiteralString, LiteralInt)
	}
	if c.When != "" {
		p, ok := b.Parameters[c.When]
		if !ok {
			return fmt.Errorf("%s: `when: %s` names no declared parameter", where, c.When)
		}
		if err := guardable(where, c.When, p); err != nil {
			return err
		}
	}
	for _, ref := range references(c.Value) {
		if err := b.knownReference(ref, where); err != nil {
			return err
		}
	}
	// `values` is the one place a LIST reference belongs: an `in`
	// constraint over the pipeline ids an operator named.
	for _, ref := range references(c.Values) {
		name, field, _ := strings.Cut(ref, ".")
		if p, ok := b.Parameters[name]; ok && field == "" &&
			(p.Type == TypeIntList || p.Type == TypeStringList) {
			continue
		}
		if err := b.knownReference(ref, where); err != nil {
			return err
		}
	}
	return nil
}

func (b *Blueprint) validateRefresh(seam string, r *Refresh) error {
	where := fmt.Sprintf("blueprint %q: seam %q: refresh", b.Name, seam)
	if len(r.Command) == 0 {
		return fmt.Errorf("%s: no command", where)
	}
	if r.Requires == "" {
		r.Requires = r.Command[0]
	}
	if r.Secret == "" || r.Key == "" {
		return fmt.Errorf("%s: `secret` and `key` name where the minted value is stored; both are required. "+
			"kmx never writes a credential it was GIVEN — only one a command it was told to run minted, into "+
			"a Secret this file names", where)
	}
	if strings.TrimSpace(r.Why) == "" {
		return fmt.Errorf("%s: `why` is required. An operator watching a release is told when a credential is "+
			"replaced under them, and by what", where)
	}
	for _, arg := range r.Command {
		for _, ref := range references(arg) {
			if err := b.knownReference(ref, where); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Blueprint) validateSteps() error {
	seen := map[string]bool{}
	captured := map[string]bool{}
	for i := range b.Steps {
		s := &b.Steps[i]
		where := fmt.Sprintf("blueprint %q: step %d", b.Name, i+1)
		if s.Name != "" {
			where = fmt.Sprintf("blueprint %q: step %q", b.Name, s.Name)
		}
		if !dnsLabelRE.MatchString(s.Name) {
			return fmt.Errorf("%s: %s is not a step name (lowercase letters, digits and dashes)", where, quoteOrAbsent(s.Name))
		}
		if seen[s.Name] {
			return fmt.Errorf("%s: named twice; --step names one step, so they have to be distinct", where)
		}
		seen[s.Name] = true
		if !contains(StepKinds, s.Kind) {
			return fmt.Errorf("%s: kind %s (want one of %s)", where, quoteOrAbsent(s.Kind), strings.Join(StepKinds, ", "))
		}
		if err := b.validateStepShape(where, s); err != nil {
			return err
		}
		if err := b.validateStepReferences(where, s, captured); err != nil {
			return err
		}
		if s.Capture != "" {
			captured[s.Capture] = true
		}
	}
	return b.validatePostures()
}

func (b *Blueprint) validateStepShape(where string, s *Step) error {
	switch s.Kind {
	case KindConsequential:
		if s.Call == nil {
			return fmt.Errorf("%s: a consequential step must name the `call` it is about. The driver files the "+
				"approval request from it, so it cannot be left to the agent", where)
		}
	case KindBounded:
		if s.Call == nil {
			return fmt.Errorf("%s: a bounded step must name the `call` it is about, so kmx can check that a "+
				"standing constraint actually admits it", where)
		}
		if s.Exec != nil {
			return fmt.Errorf("%s: a bounded step is a governed tool call; a command on the operator's machine "+
				"is not something a standing constraint can admit", where)
		}
	default:
		if s.Call != nil {
			return fmt.Errorf("%s: only a consequential or bounded step carries a `call`; a %s step is a turn, "+
				"and its tool calls are admitted by the allowlist", where, s.Kind)
		}
		if s.Exec != nil {
			return fmt.Errorf("%s: only a consequential step may `exec` — an action on the operator's machine "+
				"is approved like every other one", where)
		}
	}
	if s.Kind != KindPropose && s.Capture != "" {
		return fmt.Errorf("%s: only a `propose` step may `capture` its reply", where)
	}
	if s.Kind == KindPoll {
		if s.Poll == nil {
			return fmt.Errorf("%s: a poll step needs a `poll` block", where)
		}
		if s.Poll.TimeoutSeconds <= 0 || s.Poll.IntervalSeconds <= 0 {
			return fmt.Errorf("%s: poll needs a positive interval_seconds and timeout_seconds. An unbounded wait "+
				"inside a release is how an afternoon disappears", where)
		}
		if s.Poll.Done == "" || s.Poll.Failed == "" {
			return fmt.Errorf("%s: poll needs `done` and `failed` markers — a driver that cannot tell success "+
				"from failure would report the timeout as either", where)
		}
	} else if s.Poll != nil {
		return fmt.Errorf("%s: only a poll step carries a `poll` block", where)
	}
	if strings.TrimSpace(s.Prompt) == "" && s.Exec == nil {
		return fmt.Errorf("%s: no `prompt` and no `exec` — the step does nothing", where)
	}
	if s.When != "" {
		p, ok := b.Parameters[s.When]
		if !ok {
			return fmt.Errorf("%s: `when: %s` names no declared parameter", where, s.When)
		}
		if err := guardable(where, s.When, p); err != nil {
			return err
		}
	}
	if s.ForEach != "" {
		p, ok := b.Parameters[s.ForEach]
		if !ok {
			return fmt.Errorf("%s: `for_each: %s` names no declared parameter", where, s.ForEach)
		}
		if p.Type != TypeIntList && p.Type != TypeStringList {
			return fmt.Errorf("%s: `for_each: %s` is a %s; a step is repeated over a list", where, s.ForEach, p.Type)
		}
	}
	if s.Exec != nil {
		if err := validateExec(where, s.Exec); err != nil {
			return err
		}
	}
	if s.Call == nil {
		return nil
	}
	seam, ok := b.Seams[s.Call.Upstream]
	if !ok {
		return fmt.Errorf("%s: call names upstream %s, which the blueprint does not declare as a seam",
			where, quoteOrAbsent(s.Call.Upstream))
	}
	fields, ok := seam.Requires[s.Call.Tool]
	if !ok {
		return fmt.Errorf("%s: call names %s, whose policy fields seam %q does not declare",
			where, quoteOrAbsent(s.Call.Tool), s.Call.Upstream)
	}
	// A declared field the call does not supply is NOT an error: the
	// digest is over the declared fields as they appear, so a field
	// absent from the request is absent from the admitted call too, and
	// an agent that added it would produce a different digest and be
	// denied. The release workflow relies on that — its `pipelines_write`
	// request names no `previewRun`. What IS refused is an argument that is
	// not a field name at all.
	_ = fields
	for arg := range s.Call.Args {
		if !policyFieldRE.MatchString(arg) {
			return fmt.Errorf("%s: %q is not an argument name", where, arg)
		}
	}
	for arg, kind := range s.Call.Types {
		if _, ok := s.Call.Args[arg]; !ok {
			return fmt.Errorf("%s: `types` gives %q a type, and the call does not pass it", where, arg)
		}
		if !contains(ArgTypes, kind) {
			return fmt.Errorf("%s: argument %q has type %q (want one of %s)", where, arg, kind, strings.Join(ArgTypes, ", "))
		}
	}
	if s.Exec != nil && s.Call.Ungoverned == "" {
		return fmt.Errorf("%s: a step that runs a command must say `ungoverned:` and why. The DECISION is "+
			"governed and the TRANSFER is not; a blueprint that rendered both in one vocabulary would launder "+
			"the distinction docs/release-agent.md writes down", where)
	}
	if s.Call.Ungoverned != "" && s.Exec == nil {
		return fmt.Errorf("%s: `ungoverned` is set but the step makes an ordinary governed tool call", where)
	}
	return nil
}

func validateExec(where string, e *Exec) error {
	switch {
	case len(e.Command) == 0 && e.Script == "":
		return fmt.Errorf("%s: exec names neither a `command` nor a `script`", where)
	case len(e.Command) > 0 && e.Script != "":
		return fmt.Errorf("%s: exec names both a `command` and a `script`", where)
	}
	if e.Script != "" && !scriptNameRE.MatchString(e.Script) {
		return fmt.Errorf("%s: `script: %q` — a bundled script is named by its file name, with no directory "+
			"part, so a blueprint cannot reach out of its own bundle", where, e.Script)
	}
	for k := range e.Env {
		if !envNameRE.MatchString(k) {
			return fmt.Errorf("%s: %q is not an environment variable name", where, k)
		}
	}
	return nil
}

// validateStepReferences enforces the property the accounts-payable demo
// paid for.
//
// A `call`'s arguments are what the driver files a request for and what a
// human is shown. They may reference PARAMETERS and literals only. A
// reference to a previous step's captured reply inside `call.args` would
// let the model choose the value being approved — precisely the failure
// that demo paid for, where the agent filed a payment for a different
// invoice at the same amount and the approval landed on the wrong one.
//
// Prompts and exec environments are different: prose is not what an
// approval binds, and docs/release-agent.md states plainly that the
// release BODY is unbound for exactly that reason.
func (b *Blueprint) validateStepReferences(where string, s *Step, captured map[string]bool) error {
	hasItem := s.ForEach != ""
	if s.Call != nil {
		for _, arg := range sortedKeys(s.Call.Args) {
			for _, ref := range references(s.Call.Args[arg]) {
				if base, _, _ := strings.Cut(ref, "."); ref != itemRef && !strings.HasPrefix(ref, capturePrefix) {
					if p, ok := b.Parameters[base]; ok && (p.Type == TypeIntList || p.Type == TypeStringList) {
						return fmt.Errorf("%s: argument %q reads ${%s}, which is a list. One call takes one "+
							"value; repeat the step with `for_each` instead", where, arg, ref)
					}
				}
				if strings.HasPrefix(ref, capturePrefix) {
					return fmt.Errorf("%s: argument %q reads ${%s}. A policy-bound argument may reference "+
						"parameters and literals only — never something an agent turn produced. Otherwise the "+
						"model chooses the value a human is asked to approve, which is the failure this rule exists to prevent. "+
						"Supply it as a parameter instead (`required_for: [%s]`)", where, arg, ref, s.Name)
				}
				if err := b.knownReferenceWithItem(ref, where, hasItem); err != nil {
					return err
				}
			}
		}
	}
	prose := []string{s.Prompt}
	if s.Poll != nil {
		prose = append(prose, s.Poll.OnFailure)
	}
	if s.Exec != nil {
		prose = append(prose, s.Exec.Command...)
		prose = append(prose, s.Exec.Args...)
		prose = append(prose, s.Exec.Preview...)
		for _, k := range sortedKeys(s.Exec.Env) {
			prose = append(prose, s.Exec.Env[k])
		}
	}
	for _, text := range prose {
		for _, ref := range references(text) {
			if strings.HasPrefix(ref, capturePrefix) {
				name := strings.TrimSuffix(strings.TrimPrefix(ref, capturePrefix), fileSuffix)
				if !captured[name] {
					return fmt.Errorf("%s: reads ${%s}, which no earlier step captured", where, ref)
				}
				continue
			}
			if err := b.knownReferenceWithItem(ref, where, hasItem); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePostures is the guardrail a blueprint buys that four
// hand-written layers did not.
//
// A standing constraint ADMITS: a call inside its bounds proceeds with no
// human. An allowlist entry admits too, with no bounds at all. So:
//
//   - a CONSEQUENTIAL step's tool must be in neither, or the approval it
//     performs is theatre — the agent could have made the call without
//     it;
//   - a BOUNDED step's tool must be bounded and not allowlisted, or the
//     constraint is not the thing deciding.
//
// The release workflow gets the first right by hand and says why
// (`release-bind.sh` binds the read tools only). It gets the second
// WRONG, and this is what found it: `release-run.sh`'s `do_build` files
// an approval request for `pipelines_write`, which `release-bind.sh`
// optionally gives a standing
// bound — and docs/release-agent.md says those builds "run with no human
// at all". Both cannot be true.
func (b *Blueprint) validatePostures() error {
	for _, s := range b.Steps {
		if s.Call == nil {
			continue
		}
		bounded := false
		for _, name := range sortedSeams(b.Seams) {
			seam := b.Seams[name]
			_, isBound := seam.Bound[s.Call.Tool]
			bounded = bounded || isBound
			allowed := contains(seam.Allow, s.Call.Tool)
			switch s.Kind {
			case KindConsequential:
				if allowed {
					return fmt.Errorf("blueprint %q: step %q is consequential on %s, which seam %q ALLOWLISTS. "+
						"An allowlisted call needs no human, so the approval this step performs would be theatre. "+
						"Take it off `allow`", b.Name, s.Name, s.Call.Tool, name)
				}
				if isBound {
					return fmt.Errorf("blueprint %q: step %q is consequential on %s, which seam %q gives a "+
						"standing bound. A standing constraint ADMITS — a call inside it proceeds with no human — "+
						"so this would approve a call that was already permitted. Either drop the bound, or make "+
						"the step `kind: bounded`", b.Name, s.Name, s.Call.Tool, name)
				}
			case KindBounded:
				if allowed {
					return fmt.Errorf("blueprint %q: step %q is bounded on %s, which seam %q also ALLOWLISTS. "+
						"An allowlist entry admits the call with no bounds at all, so the constraint would never "+
						"be the thing deciding", b.Name, s.Name, s.Call.Tool, name)
				}
			}
		}
		if s.Kind == KindBounded && !bounded {
			return fmt.Errorf("blueprint %q: step %q is bounded on %s, and no seam gives %s a standing bound. "+
				"A bounded step with no constraint is a call that is simply denied", b.Name, s.Name, s.Call.Tool, s.Call.Tool)
		}
	}
	return nil
}

// guardable refuses a `when:` on a parameter that carries a default.
//
// `when:` asks whether a value was SUPPLIED, and Bind treats a default as
// supplying one — deliberately, because a defaulted parameter has a value
// everywhere else in the format and a `${release_branch}` that resolved
// in one place and not another would be worse than either rule. The
// consequence is that a guard on a defaulted parameter is never false, so
// the step or the bound is not conditional at all while LOOKING
// conditional. That is refused here rather than discovered by an operator
// wondering why --set turns nothing off.
func guardable(where, name string, p Parameter) error {
	if p.Default == "" {
		return nil
	}
	return fmt.Errorf("%s: `when: %s` guards on a parameter that carries a default (%q). `when:` asks whether a "+
		"value was SUPPLIED and a default supplies one, so this guard is never false and nothing is actually "+
		"conditional. Drop the default, or drop the guard", where, name, p.Default)
}

func (b *Blueprint) hasStep(name string) bool {
	for _, s := range b.Steps {
		if s.Name == name {
			return true
		}
	}
	return false
}

// StepNames is the ordered step list, for --step and messages. It is
// EVERY step, conditional ones included — what a particular run contains
// is ActiveSteps, and conflating the two is what `kmx workflow run` once
// got wrong.
func (b *Blueprint) StepNames() []string {
	out := make([]string, 0, len(b.Steps))
	for _, s := range b.Steps {
		out = append(out, s.Name)
	}
	return out
}

// inRun answers `when:`: a step with no guard is always in the run, and a
// guarded one is in it when its parameter was supplied.
//
// This is the ONE definition of what a run contains, and it is one
// function because it was two. `kmx workflow show` filtered on this
// predicate while `kmx workflow run` bound every step unconditionally, so
// the two commands described different workflows — and the one that
// described it correctly was the one that does nothing.
func inRun(s Step, v Values) bool { return s.When == "" || v.Supplied(s.When) }

// ActiveSteps narrows `steps` to the ones these parameters turn on,
// keeping the blueprint's order. It is what `--step` is filtered through
// too: naming a step whose guard is unmet asks for a run with nothing in
// it, and that is said rather than half-run.
func (b *Blueprint) ActiveSteps(v Values, steps []string) []string {
	asked := map[string]bool{}
	for _, s := range steps {
		asked[s] = true
	}
	out := make([]string, 0, len(steps))
	for _, s := range b.Steps {
		if asked[s.Name] && inRun(s, v) {
			out = append(out, s.Name)
		}
	}
	return out
}

// StepGuard is the parameter a step's `when:` names, or "" for a step
// that is always in the run. It exists so a driver that ran nothing can
// say WHICH --set would have turned each step on.
func (b *Blueprint) StepGuard(name string) string {
	for _, s := range b.Steps {
		if s.Name == name {
			return s.When
		}
	}
	return ""
}

// Scripts names every bundled script the blueprint runs.
func (b *Blueprint) Scripts() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range b.Steps {
		if s.Exec == nil || s.Exec.Script == "" || seen[s.Exec.Script] {
			continue
		}
		seen[s.Exec.Script] = true
		out = append(out, s.Exec.Script)
	}
	sort.Strings(out)
	return out
}

func (b *Blueprint) knownReference(ref, where string) error {
	return b.knownReferenceWithItem(ref, where, false)
}

func (b *Blueprint) knownReferenceWithItem(ref, where string, hasItem bool) error {
	if ref == itemRef {
		if !hasItem {
			return fmt.Errorf("%s: reads ${%s}, but the step has no `for_each`", where, itemRef)
		}
		return nil
	}
	name, field, _ := strings.Cut(ref, ".")
	p, ok := b.Parameters[name]
	if !ok {
		return fmt.Errorf("%s: reads ${%s}, which is not a declared parameter", where, ref)
	}
	if field == "" {
		if p.Type == TypeGitHubRepo {
			return fmt.Errorf("%s: reads ${%s}; a github_repo parameter is referenced as ${%s.owner} or "+
				"${%s.name}, because owner and repo are separate policy fields", where, ref, name, name)
		}
		// A list read in prose or in an environment variable renders
		// comma-joined, which is how the release driver passes ADO_BUILDS and
		// ADO_ARTIFACTS as. A list read in a CALL ARGUMENT is refused
		// where that check belongs — one call takes one value.
		return nil
	}
	if p.Type != TypeGitHubRepo || (field != "owner" && field != "name") {
		return fmt.Errorf("%s: reads ${%s}; %q has no field %q", where, ref, name, field)
	}
	return nil
}

func sortedSeams(m map[string]Seam) []string { return sortedKeys(m) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func quoteOrAbsent(v string) string {
	if v == "" {
		return "(absent)"
	}
	return fmt.Sprintf("%q", v)
}
