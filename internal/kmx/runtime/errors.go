package runtime

import "fmt"

// Verb names identify the four static LifecycleAdapter operations a runtime
// may decline, plus the two app-owned presentation verbs (list, show) that
// share this exact error type without joining Capabilities or the neutral
// LifecycleAdapter interface. They are the exact words UnsupportedVerbError
// prints, so a caller comparing its message against DESIGN.md §4's example
// ("runtime kagent does not support render") sees precisely one of these.
const (
	VerbRender   = "render"
	VerbDeploy   = "deploy"
	VerbStatus   = "status"
	VerbEvaluate = "evaluate"
	// VerbList and VerbShow name DESIGN.md §1's app-owned list/show
	// presentation operations, which live in app rather than as
	// LifecycleAdapter methods: they are inventory/presentation, not
	// lifecycle verbs, so no Capabilities flag governs them. List is
	// dispatched by runtime ID, and an ID with no registered handler returns
	// this same shared error naming VerbList. Show has no such registry —
	// `kmx agent show` is Orka's own chain view — so VerbShow is the agreed
	// word for that verb whenever one is needed, and never a second error
	// model.
	VerbList = "list"
	VerbShow = "show"
)

// UnsupportedVerbError is the one shared typed error every LifecycleAdapter
// verb, and every app-owned list/show presentation lookup, returns when a
// runtime does not support that verb. DESIGN.md §1 is explicit that "W94
// does not add a second error/registry model" — every runtime and every verb
// share this exact type; callers recover it with errors.As, not by matching
// a message string.
type UnsupportedVerbError struct {
	Runtime ID
	Verb    string
}

// Error prints plainly, matching DESIGN.md §4 exactly:
// "runtime kagent does not support render".
func (e *UnsupportedVerbError) Error() string {
	return fmt.Sprintf("runtime %s does not support %s", e.Runtime, e.Verb)
}
