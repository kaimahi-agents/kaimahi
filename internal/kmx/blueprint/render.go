package blueprint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Rendered is a blueprint plus one operator's parameters: the governance
// artifacts, and the steps with every parameter resolved.
//
// Nothing here has touched a cluster. That is deliberate — `kmx workflow
// show` renders exactly this and prints it, so what an operator reviews
// is the object that gets applied.
type Rendered struct {
	Blueprint *Blueprint
	Values    Values

	// Allowlist is the credential's tool allowlist: the union of every
	// seam's `allow`, sorted. Sorted because the plane reads it back
	// sorted (P12), and an operator diffing two runs should not have to
	// wonder whether the list changed or only its order.
	Allowlist []string
	// Constraints is credential -> tool -> constraints: exactly the
	// shape `standing_constraints` takes in the plane's table.
	Constraints map[string]map[string][]RenderedConstraint
	// Steps are the resolved steps, expanded over `for_each`.
	Steps []RenderedStep
}

// RenderedConstraint is one bound with its literal resolved and typed.
type RenderedConstraint struct {
	Field  string
	Op     string
	Value  any // string or int64
	Values []any
}

// RenderedStep is one unit of work with every parameter substituted.
// Captures are left as references: they do not exist until a step has
// run, and the driver resolves them.
type RenderedStep struct {
	Name string
	Kind string
	// Label distinguishes the iterations of a `for_each` step.
	Label   string
	Prompt  string
	Capture string
	// Upstream, Tool and Args are the exact call. The driver files the
	// approval request from Args and from nothing else.
	Upstream string
	Tool     string
	Args     map[string]string
	// ArgTypes gives an argument a JSON type other than string, because
	// the gateway's digest is over the canonicalised arguments and 41 is
	// not "41".
	ArgTypes map[string]string
	// PolicyFields is the declared order for Tool — the order the
	// plane's audit summary reads, and therefore the order the driver
	// must match a pending request on.
	PolicyFields []string
	// Ungoverned, when set, is why this step's ACTION is outside the
	// gateway even though its DECISION is not.
	Ungoverned string
	Exec       *RenderedExec
	Poll       *PollSpec
	OnFailure  string
	// Blocked says why this step could not be resolved, and is set only
	// by RenderForReview. A run never sees one: `kmx workflow run`
	// refuses at Bind rather than starting a workflow it cannot finish.
	Blocked string
}

// RenderedExec is an action on the operator's machine, resolved.
type RenderedExec struct {
	Command  []string
	Script   string
	Args     []string
	Env      map[string]string
	Requires []string
	Preview  []string
}

// Render resolves a blueprint against parameters and the steps that will
// run. `declared` is the running plane's policy_fields (from
// /admin/config/validate); pass nil to render without a cluster, in which
// case the blueprint's own `requires` is used and Check must be run
// before anything is applied.
// RenderForReview is Render for `kmx workflow show`, which is asked
// BEFORE an operator knows every value — the release workflow's build
// ids do not exist until its build step has run. A step that cannot be
// resolved is marked `Blocked` and described, instead of taking the
// whole render down and leaving the operator with no way to see the
// shape of the thing they are being asked to supply values for.
func (b *Blueprint) RenderForReview(v Values, declared map[string][]string) (*Rendered, error) {
	r, err := b.render(v, b.StepNames(), declared, true)
	return r, err
}

// Render resolves a blueprint against parameters and the steps that will
// run. An unresolved reference is an error: nothing runs half-resolved.
func (b *Blueprint) Render(v Values, steps []string, declared map[string][]string) (*Rendered, error) {
	return b.render(v, steps, declared, false)
}

func (b *Blueprint) render(v Values, steps []string, declared map[string][]string, review bool) (*Rendered, error) {
	r := &Rendered{Blueprint: b, Values: v, Constraints: map[string]map[string][]RenderedConstraint{}}

	allow := map[string]bool{}
	tools := map[string][]RenderedConstraint{}
	for _, seamName := range sortedSeams(b.Seams) {
		seam := b.Seams[seamName]
		for _, t := range seam.Allow {
			allow[t] = true
		}
		for _, tool := range sortedKeys(seam.Bound) {
			rendered, err := r.renderConstraints(seamName, tool, seam.Bound[tool])
			if err != nil {
				return nil, err
			}
			// A block whose constraints were all conditional on a
			// parameter nobody supplied is not an empty bound — it is NO
			// bound, and the tool is then denied and filed like any
			// other unlisted one. That is exactly what `make
			// release-bind` without ADO_PIPELINES does.
			if len(rendered) > 0 {
				tools[tool] = rendered
			}
		}
	}
	r.Allowlist = make([]string, 0, len(allow))
	for t := range allow {
		r.Allowlist = append(r.Allowlist, t)
	}
	sort.Strings(r.Allowlist)
	if len(tools) > 0 {
		r.Constraints[b.Credential] = tools
	}

	run := map[string]bool{}
	for _, s := range steps {
		run[s] = true
	}
	for _, s := range b.Steps {
		if !run[s.Name] {
			continue
		}
		if !inRun(s, v) {
			// In a run, a step whose condition is unmet simply does not
			// happen. In a review it still has to be VISIBLE: an
			// operator deciding whether to trust a workflow needs to see
			// the steps they have not enabled, not a shorter list.
			if review {
				r.Steps = append(r.Steps, RenderedStep{
					Name: s.Name, Kind: s.Kind, Label: s.Name,
					Blocked: "not in this run — needs --set " + s.When,
				})
			}
			continue
		}
		expanded, err := r.renderStep(s, declared)
		if err != nil {
			if !review {
				return nil, err
			}
			r.Steps = append(r.Steps, RenderedStep{
				Name: s.Name, Kind: s.Kind, Label: s.Name, Blocked: err.Error(),
			})
			continue
		}
		r.Steps = append(r.Steps, expanded...)
	}
	return r, nil
}

func (r *Rendered) renderConstraints(seam, tool string, cs []Constraint) ([]RenderedConstraint, error) {
	var out []RenderedConstraint
	for i, c := range cs {
		if c.When != "" && !r.Values.Supplied(c.When) {
			continue
		}
		where := fmt.Sprintf("seam %q: bound %s[%d]", seam, tool, i)
		rc := RenderedConstraint{Field: c.Field, Op: c.Op}
		switch c.Op {
		case OpIn, OpNotIn:
			vals, err := r.literalList(c)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			rc.Values = vals
		default:
			s, err := r.Values.substitute(c.Value, "")
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			asInt := numericOps[c.Op] || c.Literal == LiteralInt
			if c.Literal == LiteralString {
				asInt = false
			}
			if asInt {
				n, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("%s: op %q compares integers, and %q is not one", where, c.Op, s)
				}
				rc.Value = n
			} else {
				rc.Value = s
			}
		}
		out = append(out, rc)
	}
	// Applying SOME of a block is applying a bound wider than the one
	// written, which is the direction a mistake must never go.
	if len(out) > 0 && len(out) != len(cs) {
		return nil, fmt.Errorf("seam %q: bound %s: some constraints are conditional on a parameter that was "+
			"supplied and some on one that was not. A partially applied bound is WIDER than the one written; "+
			"put the whole block behind one `when:`", seam, tool)
	}
	return out, nil
}

// literalList resolves an `in`/`not_in` values expression. A reference to
// an int_list parameter becomes integers; a string_list or a literal
// becomes strings, unless `literal: int` says otherwise.
func (r *Rendered) literalList(c Constraint) ([]any, error) {
	expr := strings.TrimSpace(c.Values)
	if refs := references(expr); len(refs) == 1 && expr == "${"+refs[0]+"}" {
		if ints := r.Values.Ints(refs[0]); ints != nil && c.Literal != LiteralString {
			out := make([]any, 0, len(ints))
			for _, n := range ints {
				out = append(out, n)
			}
			return out, nil
		}
		if strs := r.Values.Strings(refs[0]); strs != nil {
			return toAny(strs, c.Literal == LiteralInt)
		}
	}
	s, err := r.Values.substitute(expr, "")
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("`values: %s` resolved to nothing", c.Values)
	}
	return toAny(parts, c.Literal == LiteralInt)
}

func toAny(in []string, asInt bool) ([]any, error) {
	out := make([]any, 0, len(in))
	for _, s := range in {
		if asInt {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("`literal: int` and %q is not a whole number", s)
			}
			out = append(out, n)
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *Rendered) renderStep(s Step, declared map[string][]string) ([]RenderedStep, error) {
	items := []string{""}
	if s.ForEach != "" {
		items = r.Values.Items(s.ForEach)
		if len(items) == 0 {
			// NOT nil: a step that vanishes is a step the run then
			// reports as done. If it should not happen without the list,
			// say so with `when:` — which is a statement in the file
			// rather than an accident of an empty slice.
			return nil, fmt.Errorf("step %q repeats over ${%s}, and nothing was supplied for it. Guard the step "+
				"with `when: %s` if it is optional, or make %s required", s.Name, s.ForEach, s.ForEach, s.ForEach)
		}
	}
	var out []RenderedStep
	for _, item := range items {
		rs := RenderedStep{Name: s.Name, Kind: s.Kind, Capture: s.Capture, Poll: s.Poll, Label: s.Name}
		if item != "" {
			rs.Label = s.Name + "[" + item + "]"
		}
		var err error
		if rs.Prompt, err = r.Values.substitute(s.Prompt, item); err != nil {
			return nil, fmt.Errorf("step %q: prompt: %w", rs.Label, err)
		}
		if s.Poll != nil {
			if rs.OnFailure, err = r.Values.substitute(s.Poll.OnFailure, item); err != nil {
				return nil, fmt.Errorf("step %q: poll.on_failure: %w", rs.Label, err)
			}
		}
		if s.Exec != nil {
			e := &RenderedExec{Script: s.Exec.Script, Requires: s.Exec.Requires, Env: map[string]string{}}
			for _, list := range []struct {
				in  []string
				out *[]string
				of  string
			}{
				{s.Exec.Command, &e.Command, "command"},
				{s.Exec.Args, &e.Args, "args"},
				{s.Exec.Preview, &e.Preview, "preview"},
			} {
				for _, arg := range list.in {
					v, err := r.Values.substitute(arg, item)
					if err != nil {
						return nil, fmt.Errorf("step %q: exec.%s: %w", rs.Label, list.of, err)
					}
					*list.out = append(*list.out, v)
				}
			}
			for _, k := range sortedKeys(s.Exec.Env) {
				v, err := r.Values.substituteEnv(s.Exec.Env[k], item)
				if err != nil {
					return nil, fmt.Errorf("step %q: exec.env.%s: %w", rs.Label, k, err)
				}
				e.Env[k] = v
			}
			rs.Exec = e
		}
		if s.Call != nil {
			rs.Upstream, rs.Tool, rs.Ungoverned = s.Call.Upstream, s.Call.Tool, s.Call.Ungoverned
			rs.Args = map[string]string{}
			for _, k := range sortedKeys(s.Call.Args) {
				v, err := r.Values.substitute(s.Call.Args[k], item)
				if err != nil {
					return nil, fmt.Errorf("step %q: argument %q: %w", rs.Label, k, err)
				}
				rs.Args[k] = v
			}
			rs.ArgTypes = s.Call.Types
			rs.PolicyFields = r.Blueprint.Seams[s.Call.Upstream].Requires[s.Call.Tool]
			if declared != nil {
				if fields, ok := declared[s.Call.Tool]; ok {
					rs.PolicyFields = fields
				}
			}
		}
		out = append(out, rs)
	}
	return out, nil
}

// ArgsJSON is the call's arguments as the plane's admin API takes them:
// one JSON object, typed by the blueprint's `call.types`. The gateway
// canonicalises what it receives and the digest is over that, so 41 and
// "41" are different calls — which is why the type is declared and not
// guessed from the shape of the value.
func (s RenderedStep) ArgsJSON() (string, error) {
	obj := map[string]any{}
	for k, v := range s.Args {
		switch s.ArgTypes[k] {
		case ArgInt:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return "", fmt.Errorf("argument %q is declared an integer and %q is not one", k, v)
			}
			obj[k] = n
		case ArgBool:
			switch v {
			case "true":
				obj[k] = true
			case "false":
				obj[k] = false
			default:
				return "", fmt.Errorf("argument %q is declared a boolean and %q is not one", k, v)
			}
		default:
			obj[k] = v
		}
	}
	out, err := json.Marshal(obj)
	return string(out), err
}

// Summary is the human-readable line the plane files this call under:
// the declared policy fields, in declared order, exactly as the audit's
// arg_summary renders them. The driver matches a pending request on it,
// so a selector less specific than this could match a request a human
// then approves by mistake.
func (s RenderedStep) Summary() string {
	parts := make([]string, 0, len(s.PolicyFields))
	for _, f := range s.PolicyFields {
		if v, ok := s.Args[f]; ok {
			parts = append(parts, f+" "+v)
		}
	}
	return s.Tool + ": " + strings.Join(parts, ", ")
}

// Fragment is the overlay ConfigMap fragment this blueprint's constraints
// become: the same `{"standing_constraints": {...}}` shape
// scripts/release-bind.sh writes, because it goes through the same P15
// overlay and the plane parses it with the same parser.
//
// An overlay may carry `tool_upstreams` too, and this deliberately does
// not. plane/internal/config/overlay.go refuses `credential_file`,
// `credential_header`, `internet`, `ca_file` and `extra_headers` on an
// overlay entry, so a blueprint could only ever define a keyless
// in-cluster seam — and a format that defined SOME seams and not others
// is a format whose failure mode is discovered on the day somebody points
// it at a hosted server. Seams are NAMED, never defined; `kmx tools add`
// is where an in-cluster one is onboarded and the committed table is
// where a hosted one is reviewed.
func (r *Rendered) Fragment() (string, error) {
	if len(r.Constraints) == 0 {
		return "", nil
	}
	doc := map[string]any{"standing_constraints": constraintsJSON(r.Constraints)}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

func constraintsJSON(in map[string]map[string][]RenderedConstraint) map[string]any {
	creds := map[string]any{}
	for cred, tools := range in {
		byTool := map[string]any{}
		for tool, cs := range tools {
			list := make([]any, 0, len(cs))
			for _, c := range cs {
				entry := map[string]any{"field": c.Field, "op": c.Op}
				if c.Values != nil {
					entry["values"] = c.Values
				} else {
					entry["value"] = c.Value
				}
				list = append(list, entry)
			}
			byTool[tool] = list
		}
		creds[cred] = byTool
	}
	return creds
}

// FragmentKey is where this blueprint's constraints live in the overlay
// ConfigMap. Derived from the blueprint name so two blueprints cannot
// collide silently, and prefixed so a fragment a workflow wrote is
// distinguishable from one `kmx tools add` wrote.
func (b *Blueprint) FragmentKey() string { return "workflow-" + b.Name + ".json" }

// Normalize re-emits a JSON document with sorted keys and a fixed indent,
// so two documents that say the same thing compare equal regardless of
// which program wrote them. It is what the equivalence test diffs, and
// what it normalises away is key ORDER and whitespace — never a value,
// never a type, never a present-versus-absent key.
func Normalize(raw []byte) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
