package blueprint

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Parameter types. Four, and no more without a reason: every type is
// something the plane's policy vocabulary can actually bind, or a list
// the driver can repeat a step over.
const (
	// TypeString is one scalar. It reaches argv, a policy field and an
	// approval summary, so a `pattern` is strongly encouraged.
	TypeString = "string"
	// TypeStringList is a comma-separated list of scalars — workflow
	// file names, artifact names.
	TypeStringList = "string_list"
	// TypeIntList is a list of integers: the `in` operator's `values`,
	// and pipeline or build ids.
	TypeIntList = "int_list"
	// TypeGitHubRepo is "owner/name". It is its own type because GitHub
	// names a repository that way, a fine-grained token is scoped that
	// way, and the plane binds `owner` and `repo` as two SEPARATE policy
	// fields — so one operator-typed value has to become two bound
	// values, and doing that with string surgery would put a `split`
	// operator into a format that deliberately has no expressions.
	TypeGitHubRepo = "github_repo"
)

// ParameterTypes is the vocabulary, for messages.
var ParameterTypes = []string{TypeString, TypeStringList, TypeIntList, TypeGitHubRepo}

// Constraint operators, mirrored from plane/internal/config/policy.go.
// Mirrored rather than imported: the plane is a separate Go module and
// kmx cannot import it. The mirror is asserted against the plane's own
// source by TestTheConstraintVocabularyMatchesThePlanes.
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

// ConstraintOps is that vocabulary.
var ConstraintOps = []string{OpEq, OpNe, OpLt, OpLte, OpGt, OpGte, OpIn, OpNotIn}

// Argument JSON types, for `call.types`.
const (
	ArgString = "string"
	ArgInt    = "int"
	ArgBool   = "bool"
)

// ArgTypes is that vocabulary.
var ArgTypes = []string{ArgString, ArgInt, ArgBool}

// Literal kinds, for the `literal:` override on a constraint.
const (
	LiteralString = "string"
	LiteralInt    = "int"
)

// numericOps take an integer literal on both sides.
var numericOps = map[string]bool{OpLt: true, OpLte: true, OpGt: true, OpGte: true}

func validOp(op string) bool { return contains(ConstraintOps, op) }

var (
	paramNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	// dnsLabelRE is the shape a name has to satisfy to be a credential
	// name, a ConfigMap key fragment and a Kubernetes object name at
	// once, which is what a blueprint's name, agent and steps become.
	dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,40}[a-z0-9])?$`)
	// upstreamNameRE, toolNameRE and policyFieldRE are held to exactly
	// the shapes internal/kmx/scaffold and plane/internal/config hold
	// them to. A blueprint that accepted a name the plane refuses would
	// be a format that produces a config which will not load.
	upstreamNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	toolNameRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	policyFieldRE  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// scriptNameRE is a bundled script: a bare file name. No directory
	// part, so a blueprint cannot reach out of its own bundle, and no
	// `..` for the same reason.
	scriptNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	envNameRE    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	// referenceRE finds ${…} references. Deliberately not a template
	// engine: no functions, no conditionals, no arithmetic — the same
	// restraint the standing constraints take, for the same
	// reason. A policy you need an interpreter to predict is a policy
	// nobody reviewed.
	referenceRE = regexp.MustCompile(`\$\{((?:capture\.)?[a-z][a-z0-9_]*(?:\.[a-z]+)?)\}`)
)

const (
	// capturePrefix marks a reference to a previous step's captured
	// reply: ${capture.notes}.
	capturePrefix = "capture."
	// fileSuffix asks for a path to a file holding the capture rather
	// than the text: ${capture.notes.file}. A release body is a
	// multi-line document, and putting one through argv or an
	// environment variable is how a newline becomes somebody's problem.
	fileSuffix = ".file"
	// itemRef is the current element of a `for_each` step.
	itemRef = "item"
)

// compilePattern anchors a declared pattern. An unanchored pattern that
// matches a substring is a validation that reads like a check and is not
// one.
func compilePattern(p string) (*regexp.Regexp, error) {
	if !strings.HasPrefix(p, "^") || !strings.HasSuffix(p, "$") {
		return nil, fmt.Errorf("pattern %q must be anchored with ^ and $ — an unanchored pattern matches a "+
			"substring and is not the check it looks like", p)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", p, err)
	}
	return re, nil
}

// references returns the names referenced by ${…} in a string, in order.
func references(s string) []string {
	var out []string
	for _, m := range referenceRE.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// Values are the operator's supplied parameters, type-checked.
type Values struct {
	// scalars holds every reference name that resolves to one string:
	// "version", and "repo.owner"/"repo.name" for a github_repo.
	scalars map[string]string
	// ints and strs hold the two list types, as their literal elements.
	ints map[string][]int64
	strs map[string][]string
	// supplied names the parameters the operator actually gave, which is
	// what `when:` and `required_for` are answered from. A defaulted
	// parameter counts as supplied; an absent one does not.
	supplied map[string]bool
}

// Supplied reports whether this parameter has a value. It is what
// `when:` asks.
func (v Values) Supplied(name string) bool { return v.supplied[name] }

// Items returns a list parameter's elements as strings, for `for_each`.
//
// nil for a name that is not a bound list, and the distinction is
// load-bearing: expand() uses "is this a list?" to decide whether to
// join it, and an empty NON-nil slice there made every unresolved
// reference render as the empty string instead of being reported. A
// policy field silently substituted to "" is a constraint that binds
// nothing.
func (v Values) Items(name string) []string {
	if s, ok := v.strs[name]; ok {
		return s
	}
	ints, ok := v.ints[name]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(ints))
	for _, n := range ints {
		out = append(out, strconv.FormatInt(n, 10))
	}
	return out
}

// Ints returns an int_list parameter's values, or nil.
func (v Values) Ints(name string) []int64 { return v.ints[name] }

// Strings returns a string_list parameter's values, or nil.
func (v Values) Strings(name string) []string { return v.strs[name] }

// BindingError keeps the complete diagnostic while distinguishing missing
// parameters (useful for exploratory help) from invalid values or unknown keys.
type BindingError struct {
	Invalid bool
	message string
}

func (e *BindingError) Error() string { return e.message }

// Bind type-checks and resolves the operator's --set values against the
// blueprint's declarations. `steps` is the steps that will actually run,
// so a parameter `required_for` a step nobody asked for is not demanded.
//
// Every problem is collected and reported together. An operator setting
// up a release should be told all five things they left out at once, not
// discover them one failed command at a time.
func (b *Blueprint) Bind(set map[string]string, steps []string) (Values, error) {
	v := Values{
		scalars:  map[string]string{},
		ints:     map[string][]int64{},
		strs:     map[string][]string{},
		supplied: map[string]bool{},
	}
	var problems []string
	invalid := false

	for _, name := range sortedKeys(set) {
		if _, ok := b.Parameters[name]; !ok {
			invalid = true
			problems = append(problems, fmt.Sprintf("%q is not a parameter of blueprint %q (it declares: %s)",
				name, b.Name, strings.Join(b.ParameterNames(), ", ")))
		}
	}

	running := map[string]bool{}
	for _, s := range steps {
		running[s] = true
	}

	// Two passes: literal values first, then defaults that reference
	// them (`release/${version}`). Chained computed defaults are refused
	// at parse time, so one extra pass is enough and the resolution
	// order is not something a blueprint author has to reason about.
	deferred := map[string]string{}
	for _, name := range b.ParameterNames() {
		p := b.Parameters[name]
		raw := strings.TrimSpace(set[name])
		if raw == "" && p.Default != "" {
			if references(p.Default) != nil {
				deferred[name] = p.Default
				continue
			}
			raw = p.Default
		}
		if raw == "" {
			problems = append(problems, missing(name, p, running)...)
			continue
		}
		v.supplied[name] = true
		if err := v.bindOne(name, p, raw); err != nil {
			invalid = true
			problems = append(problems, err.Error())
		}
	}
	for _, name := range sortedKeys(deferred) {
		p := b.Parameters[name]
		raw, err := v.substitute(deferred[name], "")
		if err != nil {
			// A default that depends on a parameter nobody supplied is
			// simply absent, not an error — `release_branch` defaults
			// from `version`, and a run that needs neither should not be
			// told about either.
			problems = append(problems, missing(name, p, running)...)
			continue
		}
		v.supplied[name] = true
		if err := v.bindOne(name, p, raw); err != nil {
			invalid = true
			problems = append(problems, err.Error())
		}
	}

	// `needs` is checked last, so an operator hears about all of it at
	// once rather than one round trip per flag.
	for _, name := range b.ParameterNames() {
		if !v.supplied[name] {
			continue
		}
		for _, need := range b.Parameters[name].Needs {
			if !v.supplied[need] {
				problems = append(problems, fmt.Sprintf("--set %s=… also needs --set %s=…: %s",
					name, need, b.Parameters[need].Help))
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return Values{}, &BindingError{Invalid: invalid, message: fmt.Sprintf("blueprint %q: %d parameter problem(s):\n  - %s",
			b.Name, len(problems), strings.Join(problems, "\n  - "))}
	}
	return v, nil
}

// BindRun binds for a RUN and returns the steps that will actually run.
//
// The question is circular, which is what an earlier driver got wrong:
// `when:` is answered from the parameters, and which parameters are
// DEMANDED is answered from the steps `when:` left in. That driver bound
// every step unconditionally, so a parameter that only a conditional step
// needs was demanded in order to leave that step out — `publish` is
// guarded by `when: ado_builds` and `ado_builds` is
// `required_for: [publish]`, so
// the only runs that could start were the ones supplying every guarded
// parameter, build ids included. Which is to say: every run that used
// `when:` for what it is for failed at binding.
//
// Two passes settle it. The first demands nothing a step needs — exactly
// the call `kmx workflow show` makes — which is enough to know what was
// supplied, and `supplied` does not depend on the step list. The second
// demands what the steps that survived actually need.
func (b *Blueprint) BindRun(set map[string]string, steps []string) (Values, []string, error) {
	probe, err := b.Bind(set, nil)
	if err != nil {
		// Which steps are in the run cannot be answered from values that
		// did not bind. Report against every step asked for rather than
		// a shorter list derived from a guess — the problems named there
		// are a superset, and the operator's problem is upstream of the
		// question anyway.
		if _, full := b.Bind(set, steps); full != nil {
			return Values{}, nil, full
		}
		return Values{}, nil, err
	}
	active := b.ActiveSteps(probe, steps)
	v, err := b.Bind(set, active)
	if err != nil {
		return Values{}, nil, err
	}
	return v, active, nil
}

func missing(name string, p Parameter, running map[string]bool) []string {
	if p.Required {
		return []string{fmt.Sprintf("--set %s=… is required: %s", name, p.Help)}
	}
	for _, step := range p.RequiredFor {
		if running[step] {
			return []string{fmt.Sprintf("--set %s=… is required to run step %q: %s", name, step, p.Help)}
		}
	}
	return nil
}

func (v *Values) bindOne(name string, p Parameter, raw string) error {
	if p.Pattern != "" {
		re, err := compilePattern(p.Pattern)
		if err != nil {
			return err
		}
		if !re.MatchString(raw) {
			return fmt.Errorf("--set %s=%q does not match %s: %s", name, raw, p.Pattern, p.Help)
		}
	}
	switch p.Type {
	case TypeString:
		v.scalars[name] = raw
	case TypeGitHubRepo:
		owner, repo, ok := strings.Cut(raw, "/")
		if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return fmt.Errorf("--set %s=%q: want owner/name", name, raw)
		}
		v.scalars[name+".owner"] = owner
		v.scalars[name+".name"] = repo
	case TypeStringList:
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		if len(out) == 0 {
			return fmt.Errorf("--set %s=%q named nothing", name, raw)
		}
		v.strs[name] = out
	case TypeIntList:
		var out []int64
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return fmt.Errorf("--set %s=%q: %q is not a whole number (want a comma-separated list)", name, raw, part)
			}
			out = append(out, n)
		}
		if len(out) == 0 {
			return fmt.Errorf("--set %s=%q named nothing", name, raw)
		}
		v.ints[name] = out
	}
	return nil
}

// ParameterNames is the declared parameters, sorted.
func (b *Blueprint) ParameterNames() []string { return sortedKeys(b.Parameters) }

// substitute replaces ${…} references with bound values. An unresolved
// reference is an ERROR, never an empty string: a policy field silently
// substituted to "" is a constraint that binds nothing, and a request
// summary with a hole in it is one a human approves anyway.
//
// Captures are NOT resolved here — the driver resolves those, because
// they do not exist until a step has run. Anything reaching this with a
// capture reference is a bug in the caller, and it is reported as one
// rather than substituted away.
func (v Values) substitute(s string, item string) (string, error) {
	return v.expand(s, item, false)
}

// substituteEnv is substitute for an exec ENVIRONMENT, where a parameter
// nobody supplied means "unset" rather than "hole in a policy field".
// The release driver passes ADO_ARTIFACTS and ASSET_GLOBS as empty strings
// for exactly this reason. Never used for a call argument or a constraint.
func (v Values) substituteEnv(s string, item string) (string, error) {
	return v.expand(s, item, true)
}

func (v Values) expand(s string, item string, loose bool) (string, error) {
	var missing []string
	out := referenceRE.ReplaceAllStringFunc(s, func(m string) string {
		ref := referenceRE.FindStringSubmatch(m)[1]
		switch {
		case ref == itemRef:
			if item == "" {
				if loose {
					return ""
				}
				missing = append(missing, ref)
				return m
			}
			return item
		case strings.HasPrefix(ref, capturePrefix):
			return m
		}
		if val, ok := v.scalars[ref]; ok {
			return val
		}
		if items := v.Items(ref); items != nil {
			return strings.Join(items, ",")
		}
		if loose {
			return ""
		}
		missing = append(missing, ref)
		return m
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("unresolved reference(s) %s — the value was not supplied, and kmx will not "+
			"substitute an empty string into a policy-bound argument", strings.Join(quoteAll(missing), ", "))
	}
	return out, nil
}

func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, "${"+v+"}")
	}
	return out
}
