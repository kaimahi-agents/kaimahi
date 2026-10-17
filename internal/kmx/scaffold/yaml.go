package scaffold

import (
	"errors"
	"fmt"
	"strings"
)

// A hand-audited YAML emitter, deliberately not a library.
//
// Operator-supplied names, descriptions and instructions must remain values,
// not become new YAML keys or sequence entries. The escaping and indentation
// rules prevent well-formed documents from expressing injected behavior:
//
//   - a block scalar indents EVERY line uniformly, so a line inside an
//     instruction file cannot dedent out of the scalar and become a sibling
//     key (a second `tools:`, say);
//   - a scalar that would have to span lines in flow style is REFUSED
//     rather than escaped, because escaping is where the subtle bugs live.
//
// This prevents YAML injection (CWE-74), such as a newline in a tool name
// introducing another sequence entry.

// ErrUnresolvedPlaceholder classifies a documentation token that cannot be used
// as runtime identity. It carries no authored value.
var ErrUnresolvedPlaceholder = errors.New("unresolved documentation placeholder")

// ValidateSingleLineText states the control-character policy a value rendered
// as a single-line YAML scalar is held to, without rendering anything. It is
// exported so that a caller which only decides whether text is acceptable —
// the portable authoring document, which validates before any renderer is
// chosen — holds it to exactly the rule quote enforces, rather than to a
// second copy of it that could drift into accepting what rendering rejects.
//
// The error never quotes the value, because a caller refusing operator text
// may be about to print it and the value may be the thing worth hiding.
func ValidateSingleLineText(value string) error {
	for _, r := range value {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') || r == 0x7f {
			return fmt.Errorf("must not contain line breaks or other control characters")
		}
	}
	return nil
}

// ValidateModelIdentifier requires a concrete identity rather than a whole-value
// documentation placeholder such as <model>. Ordinary model spellings remain
// runtime-owned and otherwise follow the single-line text contract.
func ValidateModelIdentifier(value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("an explicit exact model identifier is required")
	}
	if err := ValidateSingleLineText(value); err != nil {
		return fmt.Errorf("model identifier %w", err)
	}
	if strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">") {
		return fmt.Errorf("model identifier: %w", ErrUnresolvedPlaceholder)
	}
	return nil
}

// ValidateBlockText is ValidateSingleLineText for text rendered as a literal
// block scalar, which is the one place multi-line text is expressible: line
// breaks and tabs are content, every other control character is refused.
func ValidateBlockText(value string) error {
	for _, r := range value {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return fmt.Errorf("must not contain control characters")
		}
	}
	return nil
}

// quote renders a scalar as a double-quoted YAML string. A value that
// contains a newline or any other control character is refused: nothing in
// an Agent's names, namespaces or tool lists legitimately spans lines, so a
// value that does is either a mistake or an attempt to inject one.
func quote(value string) (string, error) {
	if err := ValidateSingleLineText(value); err != nil {
		return "", fmt.Errorf("refusing to emit %q: a single-line YAML value may not contain control characters", value)
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// blockScalar renders the body of a literal block scalar, every line carrying
// the same indent. Trailing whitespace is stripped per line so a line of
// spaces cannot change the block's shape, and a line that is entirely empty is
// emitted empty rather than as indentation.
func blockScalar(value string, indent int) (string, error) {
	if err := ValidateBlockText(value); err != nil {
		return "", fmt.Errorf("refusing to emit a block scalar containing control characters")
	}
	pad := strings.Repeat(" ", indent)
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(value, "\n"), "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(pad)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String(), nil
}
