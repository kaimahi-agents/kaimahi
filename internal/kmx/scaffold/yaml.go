package scaffold

import (
	"fmt"
	"strings"
)

// A hand-audited YAML emitter, deliberately not a library.
//
// This is #16's reasoning, kept: the generator's whole job is to turn
// operator-supplied text — a name, a description, a file of instructions —
// into a document that a cluster will then obey. The interesting failure is
// not a malformed file, it is a well-formed file that says something the
// operator did not write. Two rules make that impossible here:
//
//   - a block scalar indents EVERY line uniformly, so a line inside an
//     instruction file cannot dedent out of the scalar and become a sibling
//     key (a second `tools:`, say);
//   - a scalar that would have to span lines in flow style is REFUSED
//     rather than escaped, because escaping is where the subtle bugs live.
//
// (CWE-74, found in review on #16: an unquoted tool name containing a
// newline closed the sequence and appended a tool nobody had reviewed.)

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

// literalBlock renders `<key>: |<n>` and the indented body beneath it.
//
// The indentation indicator (`|2`) is the load-bearing part, and it is why
// this is one function rather than a caller gluing a key to a body. Without
// it YAML INFERS the block's indentation from the first non-empty line — so
// an instructions file whose first line happens to be indented sets a deeper
// block indent than the one every following line is emitted at, and those
// following lines fall out of the scalar. Reproduced during review: a file
// beginning with four spaces produced a manifest that would not parse at all.
// The indicator states the indentation up front, so the content can no longer
// change it, and every line is content whatever it looks like.
//
// The indicator is relative to the key's own indentation, hence the
// subtraction; YAML allows 1-9.
func literalBlock(key, value string, keyIndent, contentIndent int) (string, error) {
	relative := contentIndent - keyIndent
	if relative < 1 || relative > 9 {
		return "", fmt.Errorf("block scalar indentation %d is not expressible", relative)
	}
	body, err := blockScalar(value, contentIndent)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s: |%d\n%s", strings.Repeat(" ", keyIndent), key, relative, body), nil
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
