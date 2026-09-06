package config

// Argument policy: what a tool's arguments MEAN to the plane, and
// which calls a credential may make without asking a human.
//
// Two declarations, both operator-committed config and never inferred:
//
//   - a tool's POLICY-RELEVANT FIELDS (`tools` inside a tool_upstreams
//     entry). One declaration serves two jobs: the approval digest
//     binds those fields, and the audit's human-readable summary is built
//     from them. Where a tool declares nothing, the digest binds the whole
//     canonical argument object — the brittle case, since an LLM
//     re-emitting a semantically identical call is not byte-stable.
//   - a credential's STANDING CONSTRAINTS (`standing_constraints`):
//     declarative bounds on those fields — "may call payment_schedule
//     when amount_cents <= 1000000, and never otherwise". A call inside
//     its bounds proceeds with no approval; a call outside them is denied
//     and files a request, exactly as an unlisted tool does today.
//
// The vocabulary is deliberately tiny — comparisons on declared fields
// and set membership, no expression language, which keeps the plane
// dependency-light. Everything malformed is refused at LOAD, like every
// other entry in the table: a constraint naming a field the tool does not
// declare is an error, never a silently-ignored rule.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// Constraint operators. Numeric comparisons are integer-only: money in
// this plane is integer cents, and an integer bound cannot drift the way
// a float one can.
const (
	OpEq    = "eq"
	OpNe    = "ne"
	OpLt    = "lt"
	OpLte   = "lte"
	OpGt    = "gt"
	OpGte   = "gte"
	OpIn    = "in"
	OpNotIn = "not_in"
)

// numericOps need an integer literal on both sides.
var numericOps = map[string]bool{OpLt: true, OpLte: true, OpGt: true, OpGte: true}

// ToolPolicy is one tool's declaration inside a tool_upstreams entry.
type ToolPolicy struct {
	// PolicyFields names the argument fields that are policy-relevant, in
	// the order the audit summary should read them. Required — a pointer
	// so that an explicit empty list ("this tool has no policy-relevant
	// arguments"; the binding is then verb-level) is distinguishable from
	// a declaration that forgot the key, which is refused at load.
	PolicyFields *[]string `json:"policy_fields"`
}

// Constraint is one declarative bound on one declared field. Exactly one
// of Value (scalar ops) and Values (set ops) is set.
type Constraint struct {
	Field  string    `json:"field"`
	Op     string    `json:"op"`
	Value  *Literal  `json:"value,omitempty"`
	Values []Literal `json:"values,omitempty"`
}

// Literal is a constraint literal: a JSON string, or an integer.
type Literal struct {
	Str *string
	Int *int64
}

func (l *Literal) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	switch t := raw.(type) {
	case string:
		l.Str = &t
		return nil
	case json.Number:
		n, err := strconv.ParseInt(t.String(), 10, 64)
		if err != nil {
			return fmt.Errorf("numeric constraint literals must be integers (money here is integer cents), got %s", t)
		}
		l.Int = &n
		return nil
	}
	return fmt.Errorf("constraint literals must be a string or an integer")
}

func (l Literal) String() string {
	switch {
	case l.Str != nil:
		return *l.Str
	case l.Int != nil:
		return strconv.FormatInt(*l.Int, 10)
	}
	return ""
}

// policyField bounds a declared argument name. Top-level argument names
// only: nested fields are not addressable as policy fields (documented in
// docs/tool-governance.md), which keeps the vocabulary small and the
// summary flat.
var policyField = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// PolicySet is the flattened argument-policy surface the gateway
// enforces on: tool -> declared fields, and (credential, tool) ->
// constraints. Flattened deliberately — the tool allowlist is
// per-credential and NOT per-upstream, so a tool name means one thing
// across the whole table; conflicting declarations of one tool name are
// refused at load rather than silently differing per route (which would
// let a constrained tool be called unconstrained through another
// upstream).
type PolicySet struct {
	declared    map[string][]string
	constraints map[string]map[string][]Constraint
}

// Declared returns a tool's policy-relevant fields. ok=false means the
// tool declares nothing: the digest then binds the whole canonical
// argument object.
func (p PolicySet) Declared(tool string) (fields []string, ok bool) {
	f, ok := p.declared[tool]
	return f, ok
}

// AllDeclared returns every tool's policy-relevant fields, as one map.
//
// A tool name means one thing across the whole table — conflicting
// declarations are refused at load — so this is the complete answer to
// "what does an approval bind here", which is what a caller declaring a
// workflow against this plane has to be able to ask. The returned
// map is a copy: the policy set is read by every request and must not be
// handed out for anybody to edit.
func (p PolicySet) AllDeclared() map[string][]string {
	out := make(map[string][]string, len(p.declared))
	for tool, fields := range p.declared {
		out[tool] = append([]string(nil), fields...)
	}
	return out
}

// Constraints returns the standing constraints for one credential and
// tool. ok=false means none is declared, which is today's behaviour:
// the allowlist and grants decide alone.
func (p PolicySet) Constraints(credential, tool string) (cs []Constraint, ok bool) {
	byTool, ok := p.constraints[credential]
	if !ok {
		return nil, false
	}
	cs, ok = byTool[tool]
	return cs, ok
}

// ConstrainedTools lists the tools a credential carries standing
// constraints for, sorted. They are callable right now for arguments
// inside those bounds, so the gateway includes them in the tools/list
// projection the way live grants are included.
func (p PolicySet) ConstrainedTools(credential string) []string {
	byTool := p.constraints[credential]
	out := make([]string, 0, len(byTool))
	for tool := range byTool {
		out = append(out, tool)
	}
	sort.Strings(out)
	return out
}

// Satisfied reports whether a call's canonical arguments are inside a
// standing constraint set, and names the first rule that failed. Every
// rule must hold. Fail closed throughout: a missing field, a value of
// the wrong shape, or a non-integer where an integer bound applies all
// count as OUTSIDE the constraint — such a call is denied and files an
// approval request, exactly as an unlisted tool does.
func Satisfied(cs []Constraint, args map[string]any) (bool, string) {
	for _, c := range cs {
		if !c.holds(args) {
			return false, c.describe()
		}
	}
	return true, ""
}

func (c Constraint) describe() string {
	if len(c.Values) > 0 {
		var vs []string
		for _, v := range c.Values {
			vs = append(vs, v.String())
		}
		return fmt.Sprintf("%s %s %v", c.Field, c.Op, vs)
	}
	return fmt.Sprintf("%s %s %s", c.Field, c.Op, c.Value.String())
}

func (c Constraint) holds(args map[string]any) bool {
	raw, present := args[c.Field]
	if !present {
		return false
	}
	switch c.Op {
	case OpIn, OpNotIn:
		found := false
		for _, v := range c.Values {
			if literalEquals(v, raw) {
				found = true
				break
			}
		}
		return found == (c.Op == OpIn)
	case OpEq:
		return literalEquals(*c.Value, raw)
	case OpNe:
		return !literalEquals(*c.Value, raw)
	}
	// Numeric comparison: both sides must be integers.
	got, ok := asInt(raw)
	if !ok || c.Value.Int == nil {
		return false
	}
	want := *c.Value.Int
	switch c.Op {
	case OpLt:
		return got < want
	case OpLte:
		return got <= want
	case OpGt:
		return got > want
	case OpGte:
		return got >= want
	}
	return false // unknown op: refused at load, denied here regardless
}

// literalEquals compares a constraint literal with a canonical argument
// value: strings by value, integers by value. Any other argument shape
// (an object, an array, a bool, a non-integer number) matches nothing.
func literalEquals(l Literal, raw any) bool {
	if l.Str != nil {
		s, ok := raw.(string)
		return ok && s == *l.Str
	}
	if l.Int != nil {
		got, ok := asInt(raw)
		return ok && got == *l.Int
	}
	return false
}

// asInt reads a canonical argument value as an int64. Canonical numbers
// are json.Number (canon.go normalizes integer literals), so this is the
// one place a policy decision turns text into arithmetic.
func asInt(raw any) (int64, bool) {
	num, ok := raw.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(num.String(), 10, 64)
	return i, err == nil
}

// buildPolicy flattens and validates the two declarations. Every failure
// is a load-time error: the mistake is loud at rollout, not silent at
// the first call.
func buildPolicy(c Config) (PolicySet, error) {
	p := PolicySet{declared: map[string][]string{}, constraints: map[string]map[string][]Constraint{}}
	for name, up := range c.ToolUpstreams {
		for tool, decl := range up.Tools {
			if !toolName.MatchString(tool) {
				return PolicySet{}, fmt.Errorf("config: tool upstream %q: invalid tool name %q", name, tool)
			}
			if decl.PolicyFields == nil {
				return PolicySet{}, fmt.Errorf("config: tool upstream %q tool %q: policy_fields is required (use [] to declare that no argument is policy-relevant)", name, tool)
			}
			fields := *decl.PolicyFields
			seen := map[string]bool{}
			for _, f := range fields {
				if !policyField.MatchString(f) {
					return PolicySet{}, fmt.Errorf("config: tool upstream %q tool %q: invalid policy field %q (top-level argument names only)", name, tool, f)
				}
				if seen[f] {
					return PolicySet{}, fmt.Errorf("config: tool upstream %q tool %q: duplicate policy field %q", name, tool, f)
				}
				seen[f] = true
			}
			if prev, ok := p.declared[tool]; ok && !sameFields(prev, fields) {
				return PolicySet{}, fmt.Errorf("config: tool %q is declared differently by two upstreams; a tool name means one thing across the table (the allowlist is per-credential, not per-upstream)", tool)
			}
			p.declared[tool] = fields
		}
	}
	for credential, byTool := range c.StandingConstraints {
		if !dnsLabel.MatchString(credential) {
			return PolicySet{}, fmt.Errorf("config: standing_constraints: credential %q must be a lowercase DNS label", credential)
		}
		for tool, rules := range byTool {
			if !toolName.MatchString(tool) {
				return PolicySet{}, fmt.Errorf("config: standing_constraints %q: invalid tool name %q", credential, tool)
			}
			fields, declared := p.declared[tool]
			if !declared {
				return PolicySet{}, fmt.Errorf("config: standing_constraints %q tool %q: no upstream declares this tool's policy_fields; a constraint on undeclared arguments cannot be enforced", credential, tool)
			}
			if len(rules) == 0 {
				return PolicySet{}, fmt.Errorf("config: standing_constraints %q tool %q: an empty constraint list would admit every call; remove the entry or state the bounds", credential, tool)
			}
			for _, rule := range rules {
				if err := validateConstraint(rule, fields); err != nil {
					return PolicySet{}, fmt.Errorf("config: standing_constraints %q tool %q: %w", credential, tool, err)
				}
			}
			if p.constraints[credential] == nil {
				p.constraints[credential] = map[string][]Constraint{}
			}
			p.constraints[credential][tool] = rules
		}
	}
	return p, nil
}

func validateConstraint(c Constraint, declared []string) error {
	if !containsString(declared, c.Field) {
		return fmt.Errorf("field %q is not declared policy-relevant by the tool (declared: %v)", c.Field, declared)
	}
	switch c.Op {
	case OpIn, OpNotIn:
		if len(c.Values) == 0 || c.Value != nil {
			return fmt.Errorf("%s on %q needs a non-empty \"values\" list and no \"value\"", c.Op, c.Field)
		}
		return nil
	case OpEq, OpNe, OpLt, OpLte, OpGt, OpGte:
		if c.Value == nil || len(c.Values) > 0 {
			return fmt.Errorf("%s on %q needs a single \"value\" and no \"values\"", c.Op, c.Field)
		}
		if numericOps[c.Op] && c.Value.Int == nil {
			return fmt.Errorf("%s on %q needs an integer bound", c.Op, c.Field)
		}
		return nil
	}
	return fmt.Errorf("unknown op %q on %q (want eq, ne, lt, lte, gt, gte, in, not_in)", c.Op, c.Field)
}

func sameFields(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
