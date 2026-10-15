package guard

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// ConfirmPipeline asks once for a disclosed pipeline. Every selected concrete
// remote context is named (all-local pipelines name their local contexts).
// Order is stable and deduplicated; the caller separately binds every subject.
// This returns consent only, never a mutation-guard bypass token.
func ConfirmPipeline(ctx context.Context, cfg *Kubeconfig, contexts []string, confirm, disclosure string, out io.Writer, in *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var selected, remote []string
	seen := map[string]bool{}
	for _, name := range contexts {
		posture, err := Classify(cfg, name)
		if err != nil || posture.Host == "" {
			return fmt.Errorf("pipeline context unavailable; refusing")
		}
		if !seen[name] {
			selected = append(selected, name)
			if !posture.Local {
				remote = append(remote, name)
			}
			seen[name] = true
		}
	}
	if len(remote) > 0 {
		selected = remote
	}
	challenge := strings.Join(selected, ",")
	if challenge == "" {
		return fmt.Errorf("pipeline contexts required")
	}
	if confirm != "" {
		if confirm != challenge {
			return fmt.Errorf("KAIMAHI_CONFIRM must exactly equal %q for the disclosed pipeline", challenge)
		}
		return nil
	}
	if !isTerminal(in) {
		return fmt.Errorf("pipeline consent requires KAIMAHI_CONFIRM=%q without a terminal", challenge)
	}
	if _, err := fmt.Fprintf(out, "Confirm %s; contexts %s — type %s: ", disclosure, challenge, challenge); err != nil {
		return err
	}
	answer, err := readLineContext(ctx, in)
	if err != nil {
		return err
	}
	if answer != challenge {
		return fmt.Errorf("pipeline not confirmed; no effects authorized")
	}
	return nil
}
