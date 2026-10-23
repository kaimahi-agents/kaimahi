package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Import runs one tool-free inference pass; each candidate is separately accepted
// as source text. Acceptance never executes a probe or grants runtime permissions.
func OfferImportedTools(ctx context.Context, path, model string, in io.Reader, out io.Writer) error {
	return offerImportedTools(ctx, path, model, in, out, SuggestCLICommandTools)
}
func offerImportedTools(ctx context.Context, path, model string, in io.Reader, out io.Writer, suggest func(context.Context, string, string) (MarkdownToolProposal, error)) error {
	a, err := LoadMarkdownAgent(path, "")
	if err != nil {
		return err
	}
	if len(a.Tools) > 0 || len(a.CLITools) > 0 {
		_, err = fmt.Fprintf(out, "Existing tools retained. Use kmx tools suggest %q to review additional command proposals separately.\n", path)
		return err
	}
	if _, err = fmt.Fprintln(out, "\nChecking the instructions for up to three useful scoped CLI tools. This sends the definition to Copilot; no tool commands execute."); err != nil {
		return err
	}
	p, err := suggest(ctx, path, model)
	if err != nil {
		return err
	}
	if p.SchemaVersion != 2 || validateCLIChoices(p.CLIChoices, p.Gaps) != nil {
		return errors.New("invalid scoped command proposal")
	}
	if len(p.CLIChoices) == 0 {
		fmt.Fprintln(out, "No additional tools needed.")
	}
	for _, gap := range p.Gaps {
		fmt.Fprintln(out, "Not suggested:", safeTerminal(gap))
	}
	scanner := bufio.NewScanner(mdImportLineReader{in})
	scanner.Buffer(make([]byte, 1024), 4096)
	selected := []string{}
	for i, c := range p.CLIChoices {
		fmt.Fprintf(out, "\nTool suggestion %d/%d\n", i+1, len(p.CLIChoices))
		if err = RenderCLIChoice(out, c); err != nil {
			return err
		}
		if len(c.MissingScope) > 0 {
			continue
		}
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			fmt.Fprintf(out, "Add %s? [allow/deny, default deny]: ", c.Tool.Name)
			if !scanner.Scan() {
				if err = scanner.Err(); err != nil {
					return err
				}
				fmt.Fprintln(out, "Tool review cancelled; no tools added.")
				return nil
			}
			switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
			case "allow", "a", "yes", "y":
				selected = append(selected, c.Tool.Name)
			case "", "deny", "d", "no", "n":
				fmt.Fprintln(out, "Denied:", c.Tool.Name)
			case "cancel", "q":
				fmt.Fprintln(out, "Tool review cancelled; no tools added.")
				return nil
			default:
				fmt.Fprintln(out, "Enter allow or deny.")
				continue
			}
			break
		}
	}
	if len(selected) == 0 {
		_, err = fmt.Fprintln(out, "No tools added; imported definition unchanged.")
		return err
	}
	if err = AddSuggestedCLITools(ctx, p, selected); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Added: %s\nNo commands executed and no permissions granted. If you have paired tests, add mocks for the new tools before running them. Review PATH resolution and approve execution with:\n  kmx tools approve %q --apply\n", strings.Join(selected, ", "), path)
	return err
}
