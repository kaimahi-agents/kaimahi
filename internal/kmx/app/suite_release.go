package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/imagelift"
)

// ErrImageReleaseEvidence keeps experimental image placement behind the common
// release gate until a supported image execution receipt can authorize it.
var ErrImageReleaseEvidence = errors.New("image-http release lift requires image evaluation evidence; this profile is not yet supported by the shared release gate")

func cloneReleaseEnvironment(env imagelift.Environment) imagelift.Environment {
	data, _ := json.Marshal(env)
	var copy imagelift.Environment
	_ = json.Unmarshal(data, &copy)
	return copy
}

// CheckImageExecution checks the complete packaged image profile, not a flag.
func CheckImageExecution(ctx context.Context, root string) error {
	return workspace.ValidateImageDirectory(ctx, root)
}
