package runtime

import "fmt"

// Verb names are the four static lifecycle operations a runtime may decline.
// They are the exact words UnsupportedVerbError prints, so one runtime's
// refusal reads identically to another's.
const (
	VerbRender   = "render"
	VerbDeploy   = "deploy"
	VerbStatus   = "status"
	VerbEvaluate = "evaluate"
)

// UnsupportedVerbError is the single typed error every runtime returns for a
// lifecycle verb it does not support. There is deliberately no second error
// model: callers recover it with errors.As rather than matching a message.
type UnsupportedVerbError struct {
	Runtime ID
	Verb    string
}

func (e *UnsupportedVerbError) Error() string {
	return fmt.Sprintf("runtime %s does not support %s", e.Runtime, e.Verb)
}
