package blueprint_test

// THE PROPERTY, not the crash.
//
// W35 shipped `kmx workflow show` and `kmx workflow run` answering "what
// is in this run?" in two places. `show` filtered the steps on their
// `when:` guard; `run` bound EVERY step regardless, so a parameter that
// only a conditional step needs was demanded in order to leave that step
// out — `publish` is guarded by `when: ado_builds` and `ado_builds` is
// `required_for: [publish]`, and no parameter set could satisfy both.
// The two commands described different workflows, and the one that
// described it correctly was the one that does nothing.
//
// So the assertion here is not "a run starts". It is that the two
// commands agree, on the CARRIED release blueprint, for every parameter
// set an operator would plausibly type — which is the thing that has to
// stay true after this lane.

import (
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/blueprint"
)

// runCases are parameter sets in the order an operator reaches them: the
// first is the partial one W35's driver could not start at all.
var runCases = []struct {
	name string
	set  map[string]string
	// want is every step LABEL the run contains, in order. `for_each`
	// expansion is part of it: a run over two pipelines is two steps,
	// and show has to say so too.
	want []string
}{{
	name: "GitHub only, nothing built — the set that could not start",
	set:  map[string]string{"repo": "Contoso/widget", "version": "v1.2.3"},
	want: []string{"propose", "compose", "cut"},
}, {
	name: "with GitHub Actions builds",
	set: map[string]string{"repo": "Contoso/widget", "version": "v1.2.3",
		"gh_workflows": "build-app-win.yml,build-app-mac.yml"},
	want: []string{"propose", "compose", "cut",
		"build-github[build-app-win.yml]", "build-github[build-app-mac.yml]", "watch-github"},
}, {
	name: "with Azure DevOps pipelines",
	set: map[string]string{"repo": "Contoso/widget", "version": "v1.2.3",
		"ado_org": "contoso", "ado_project": "widget", "ado_pipelines": "41,42"},
	want: []string{"propose", "compose", "cut", "build-ado[41]", "build-ado[42]", "watch-ado"},
}, {
	name: "the resumed publish, with the build ids the operator read off the builds",
	set: map[string]string{"repo": "Contoso/widget", "version": "v1.2.3",
		"ado_org": "contoso", "ado_project": "widget", "ado_pipelines": "41,42",
		"ado_builds": "9001,9002"},
	want: []string{"propose", "compose", "cut", "build-ado[41]", "build-ado[42]", "watch-ado", "publish"},
}}

func TestShowAndRunAgreeOnWhatARunContains(t *testing.T) {
	b := loadRelease(t)
	for _, tc := range runCases {
		t.Run(tc.name, func(t *testing.T) {
			// `run`: the two-pass bind, then the render a run drives.
			values, active, err := b.BindRun(tc.set, b.StepNames())
			if err != nil {
				t.Fatalf("the run could not start with these parameters: %v", err)
			}
			run, err := b.Render(values, active, nil)
			if err != nil {
				t.Fatalf("the run's render failed: %v", err)
			}

			// `show`: the same parameters, through the command that is
			// asked BEFORE every value is known.
			shown, err := b.Bind(tc.set, nil)
			if err != nil {
				t.Fatalf("`show` could not bind these parameters: %v", err)
			}
			review, err := b.RenderForReview(shown, nil)
			if err != nil {
				t.Fatalf("`show` failed to render: %v", err)
			}

			if got := labels(run.Steps); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("the run contains:\n  %s\nwant:\n  %s",
					strings.Join(got, ", "), strings.Join(tc.want, ", "))
			}
			if got := labels(unblocked(review.Steps)); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("`kmx workflow show` says the run contains:\n  %s\n`kmx workflow run` runs:\n  %s\n"+
					"The two commands must describe the same workflow", strings.Join(got, ", "),
					strings.Join(tc.want, ", "))
			}

			// And the other half: every step show marked blocked is a
			// step the run left out. A command that hid steps would
			// agree with the run and still be wrong.
			inRun := map[string]bool{}
			for _, s := range run.Steps {
				inRun[s.Name] = true
			}
			for _, s := range review.Steps {
				if s.Blocked != "" && inRun[s.Name] {
					t.Fatalf("`show` says step %q is not in this run (%s), and the run runs it", s.Name, s.Blocked)
				}
			}
		})
	}
}

// TestEveryStepIsReachableFromSomeParameterSet is the check that would
// have failed on W35's driver for every case above: the cases only prove
// agreement if between them they turn every conditional step ON. A test
// whose parameter sets all left `publish` out would have agreed happily
// with a command that could never run it.
func TestEveryStepIsReachableFromSomeParameterSet(t *testing.T) {
	b := loadRelease(t)
	reached := map[string]bool{}
	for _, tc := range runCases {
		_, active, err := b.BindRun(tc.set, b.StepNames())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for _, s := range active {
			reached[s] = true
		}
	}
	for _, name := range b.StepNames() {
		if !reached[name] {
			t.Fatalf("no parameter set in this test ever runs step %q, so nothing here proves it can run", name)
		}
	}
}

// TestABlockedStepCarriesNoPolicyBoundArguments asserts the STRUCT, not a
// line of output. `kmx workflow show` exits zero while rendering blocked
// steps, so every check on that path was a string in its output — which
// is why #109's CI assertion had to pin a rendered line. What matters is
// that a step nobody can resolve carries no arguments at all: an empty
// argument in a policy-bound call is a constraint that binds nothing.
func TestABlockedStepCarriesNoPolicyBoundArguments(t *testing.T) {
	b := loadRelease(t)
	// repo only: `version` is unsupplied, so the steps that reference it
	// cannot resolve, and three more are behind `when:` guards.
	v, err := b.Bind(map[string]string{"repo": "Contoso/widget"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.RenderForReview(v, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]blueprint.RenderedStep{}
	for _, s := range r.Steps {
		byName[s.Name] = s
	}
	for _, tc := range []struct{ step, because string }{
		// Excluded by `when:`: a guard nobody met.
		{"build-ado", "not in this run — needs --set ado_pipelines"},
		{"publish", "not in this run — needs --set ado_builds"},
		// Unresolvable: `cut` has no guard and reaches ${version}.
		{"cut", "unresolved reference"},
	} {
		s, ok := byName[tc.step]
		if !ok {
			t.Fatalf("step %q was not rendered for review at all; an operator cannot see what they are being "+
				"asked to supply values for", tc.step)
		}
		if s.Blocked == "" {
			t.Fatalf("step %q rendered as runnable with no %s", tc.step, tc.because)
		}
		if !strings.Contains(s.Blocked, tc.because) {
			t.Fatalf("step %q is blocked with %q, want it to say %q", tc.step, s.Blocked, tc.because)
		}
		if s.Args != nil {
			t.Fatalf("blocked step %q carries arguments %v. A policy-bound argument is ABSENT when it cannot "+
				"be resolved, never empty", tc.step, s.Args)
		}
		if s.Tool != "" || s.Upstream != "" {
			t.Fatalf("blocked step %q names the call %s/%s, and there is no call to name — the driver files a "+
				"request from Args and from nothing else", tc.step, s.Upstream, s.Tool)
		}
	}
	// A render that blocked EVERY step would pass the assertions above
	// while telling an operator nothing. With ${version} supplied the
	// unguarded steps resolve, and only the guarded ones stay blocked.
	v, err = b.Bind(map[string]string{"repo": "Contoso/widget", "version": "v1.2.3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err = b.RenderForReview(v, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Steps {
		switch s.Name {
		case "propose", "compose", "cut":
			if s.Blocked != "" {
				t.Fatalf("step %q is blocked (%s) with the parameters it needs supplied", s.Name, s.Blocked)
			}
		}
	}
}

// TestBindingEveryStepStillDemandsWhatOnlyAConditionalStepNeeds pins the
// defect itself, so the fix cannot be mistaken for a loosening of Bind.
// Bind is unchanged: asked to run `publish`, it still demands the build
// ids `publish` needs. What changed is that a run no longer ASKS to run a
// step whose guard nobody met.
func TestBindingEveryStepStillDemandsWhatOnlyAConditionalStepNeeds(t *testing.T) {
	b := loadRelease(t)
	set := map[string]string{"repo": "Contoso/widget", "version": "v1.2.3"}

	if _, err := b.Bind(set, b.StepNames()); err == nil {
		t.Fatal("binding every step accepted a parameter set with no build ids; the demand `publish` makes " +
			"has been lost, and this test no longer pins anything")
	} else if !strings.Contains(err.Error(), "ado_builds") {
		t.Fatalf("binding every step failed for some other reason: %v", err)
	}

	if _, _, err := b.BindRun(set, b.StepNames()); err != nil {
		t.Fatalf("the run still cannot start with a partial parameter set: %v", err)
	}
}

// TestAskingForOneConditionalStepWithoutItsGuardRunsNothing: `--step
// publish` without --set ado_builds. It must resolve to an empty run —
// said out loud by the driver — rather than binding as if it would run.
func TestAskingForOneConditionalStepWithoutItsGuardRunsNothing(t *testing.T) {
	b := loadRelease(t)
	set := map[string]string{"repo": "Contoso/widget", "version": "v1.2.3"}
	_, active, err := b.BindRun(set, []string{"publish"})
	if err != nil {
		t.Fatalf("binding refused instead of reporting an empty run: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("`--step publish` with no --set ado_builds ran %v", active)
	}
	if got := b.StepGuard("publish"); got != "ado_builds" {
		t.Fatalf("the driver would not know which flag turns `publish` on: StepGuard = %q", got)
	}
	// Supply it and the same request runs exactly that step.
	set["ado_org"], set["ado_project"], set["ado_builds"] = "contoso", "widget", "9001"
	_, active, err = b.BindRun(set, []string{"publish"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(active, ",") != "publish" {
		t.Fatalf("`--step publish` with its guard supplied ran %v", active)
	}
}

func labels(steps []blueprint.RenderedStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Label)
	}
	return out
}

func unblocked(steps []blueprint.RenderedStep) []blueprint.RenderedStep {
	var out []blueprint.RenderedStep
	for _, s := range steps {
		if s.Blocked == "" {
			out = append(out, s)
		}
	}
	return out
}
