package runtime

import "fmt"

// Verb names identify the four static LifecycleAdapter operations a runtime
// may decline. They are the exact words UnsupportedVerbError prints, so a
// caller comparing its message against DESIGN.md §4's example
// ("runtime kagent does not support render") sees precisely one of these.
const (
	VerbRender   = "render"
	VerbDeploy   = "deploy"
	VerbStatus   = "status"
	VerbEvaluate = "evaluate"
)

// UnsupportedVerbError is the one shared typed error every LifecycleAdapter
// verb returns when its declared Capabilities says it does not support that
// verb. DESIGN.md §1 is explicit that "W94 does not add a second
// error/registry model" — every runtime and every verb share this exact
// type; callers recover it with errors.As, not by matching a message
// string.
type UnsupportedVerbError struct {
	Runtime ID
	Verb    string
}

// Error prints plainly, matching DESIGN.md §4 exactly:
// "runtime kagent does not support render".
func (e *UnsupportedVerbError) Error() string {
	return fmt.Sprintf("runtime %s does not support %s", e.Runtime, e.Verb)
}
