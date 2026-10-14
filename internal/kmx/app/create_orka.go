package app

import (
	"fmt"
	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Resolve user file I/O only before terminal collection or named creation.
// No signal handler is installed here: a stalled read remains interruptible by
// the process's default signal behavior, without a blocked reader goroutine or
// touching borrowed stdin. Wizard validation and final generation reuse bytes.
func resolveOrkaInstructions(opt *CreateOptions) error {
	if opt.Instructions == "" {
		return nil
	}
	if opt.InstructionText != "" {
		return fmt.Errorf("supply only one instructions source")
	}
	if err := scaffold.RefuseKeyShapes(opt.Instructions); err != nil {
		return fmt.Errorf("refusing credential-shaped create input; supply references, never credentials")
	}
	if opt.instructionFileText != nil {
		return nil
	}
	body, err := os.ReadFile(opt.Instructions)
	if err != nil {
		return fmt.Errorf("cannot read the instructions file")
	}
	text := string(body)
	opt.instructionFileText = &text
	return nil
}

// resolveOrkaInstructionText returns the instructions these flags state,
// from whichever single source stated them. It is shared by the wizard's
// validation and by the portable document the create command renders from,
// so neither can read a different prompt than the other.
func resolveOrkaInstructionText(opt CreateOptions) (string, error) {
	if opt.Instructions == "" {
		return opt.InstructionText, nil
	}
	if opt.InstructionText != "" {
		return "", fmt.Errorf("supply only one instructions source")
	}
	if opt.instructionFileText == nil {
		return "", fmt.Errorf("instructions file must be resolved before validation")
	}
	return *opt.instructionFileText, nil
}

func validateOrkaBundle(bundle *scaffold.OrkaBundle, validator *orkaschema.Validator) error {
	if err := bundle.Validate(); err != nil {
		return err
	}
	for _, doc := range bundle.Documents()[1:] {
		if err := validator.Validate(doc); err != nil {
			return err
		}
	}
	return nil
}

// Resolve user file I/O only before terminal collection or named creation.
// No signal handler is installed here: a stalled read remains interruptible by
// the process's default signal behavior, without a blocked reader goroutine or
// touching borrowed stdin. Wizard validation and final generation reuse bytes.

// resolveOrkaInstructionText returns the instructions these flags state,
// from whichever single source stated them. It is shared by the wizard's
// validation and by the portable document the create command renders from,
// so neither can read a different prompt than the other.
