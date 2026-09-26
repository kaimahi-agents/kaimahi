package app

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"golang.org/x/term"
)

var errCreateCancelled = errors.New("agent creation cancelled")

const createBaseURLHint = "For a custom or local model endpoint, supply --base-url <HTTP(S) URL>; no endpoint is inferred from the model name."
const createTaskAuthorityNotice = "Applying authorizes Task execution. The temporary token has the account's full authority; v0.1.3 does not enforce Task-read RBAC."

func refuseWizardCredentials(values ...string) error {
	for _, value := range values {
		if err := scaffold.RefuseKeyShapes(value); err != nil {
			return fmt.Errorf("refusing credential-shaped wizard input; supply references, never credentials")
		}
	}
	return nil
}

// Both terminal collectors validate native options before offering confirmation.
// This is local validation only; CreateAgent owns schema/admission and execution.
func finishCreateWizardOptions(opt *CreateOptions) error {
	if opt.Out == "-" {
		opt.NoApply = true
	}
	if opt.NoApply && opt.DryRun {
		return fmt.Errorf("--no-apply (including --out -) and --dry-run cannot be used together")
	}
	if opt.SchemaTarget != "" && !opt.NoApply {
		return fmt.Errorf("--schema-target is offline only; online creation uses installed CRDs")
	}
	if opt.Instructions == "" && opt.InstructionText == "" {
		opt.InstructionText = "You are " + opt.Name + ", a declarative Orka agent. Your purpose is: " + opt.Description + "\nAnswer briefly and say plainly when you do not know something."
		for _, tool := range strings.Split(opt.Tools, ",") {
			if strings.TrimSpace(tool) == quickstartK8sTool {
				opt.InstructionText += "\n" + quickstartK8sInstructions
				break
			}
		}
	}
	if err := validateOrkaResultOptions(opt); err != nil {
		return err
	}
	if _, err := createOrkaBundle(*opt); err != nil {
		return err
	}
	if opt.Out == "" {
		opt.Out = filepath.Join("agents", opt.Name+".yaml")
	}
	return nil
}

func (a *App) CreateAgentInteractive(opt CreateOptions) error {
	completed, cancelled, err := a.collectCreateAgentInteractive(opt)
	if err != nil {
		return err
	}
	if cancelled {
		a.notef("Agent creation cancelled. Nothing was written or applied.")
		return nil
	}
	return a.CreateAgent(completed)
}

func (a *App) collectCreateAgentInteractive(opt CreateOptions) (CreateOptions, bool, error) {
	errFile, visible := a.Err.(*os.File)
	if a.Stdin == nil || !visible || !term.IsTerminal(int(a.Stdin.Fd())) || !term.IsTerminal(int(errFile.Fd())) {
		return opt, false, fmt.Errorf("kmx agent create needs a name in non-interactive input: kmx agent create <name> [flags]")
	}
	if os.Getenv("TERM") == "dumb" {
		completed, err := collectCreateOptions(bufio.NewScanner(a.Stdin), a.Err, opt)
		if errors.Is(err, errCreateCancelled) {
			return opt, true, nil
		}
		return completed, false, err
	}
	completed, err := runCreateWizard(a.Stdin, a.Err, opt)
	if errors.Is(err, errCreateCancelled) {
		return opt, true, nil
	}
	return completed, false, err
}

func collectCreateOptions(scanner lineScanner, out io.Writer, opt CreateOptions) (CreateOptions, error) {
	if err := resolveOrkaInstructions(&opt); err != nil {
		return opt, err
	}
	if opt.Out == "-" {
		opt.NoApply = true
	}
	if opt.NoApply && opt.DryRun {
		return opt, fmt.Errorf("--no-apply (including --out -) and --dry-run cannot be used together")
	}
	if opt.BaseURL == "" {
		fmt.Fprintln(out, createBaseURLHint)
	}
	var err error
	if opt.Description == "" {
		opt.Description, err = promptValue(scanner, out, "Describe this agent", "", true)
		if err != nil {
			return opt, err
		}
	}

	if err := refuseWizardCredentials(opt.Description, opt.Name); err != nil {
		return opt, err
	}
	defaultName := opt.Name
	if defaultName == "" {
		defaultName = slugAgentName(opt.Description)
	}
	for {
		opt.Name, err = promptValue(scanner, out, "Agent name", defaultName, true)
		if err != nil {
			return opt, err
		}
		if err := refuseWizardCredentials(opt.Name); err != nil {
			return opt, err
		}
		if err := scaffold.ValidateName(opt.Name); err != nil {
			fmt.Fprintf(out, "  %v\n", err)
			defaultName = ""
			continue
		}
		break
	}
	for _, field := range []struct {
		label string
		value *string
	}{
		{"Namespace the Orka controller watches", &opt.Namespace},
		{"Provider type (openai or anthropic)", &opt.ProviderType},
		{"Provider model identifier", &opt.Model},
		{"Existing Provider Secret name (not its value)", &opt.Secret},
	} {
		if *field.value == "" {
			*field.value, err = promptValue(scanner, out, field.label, "", true)
			if err != nil {
				return opt, err
			}
		}
	}
	if opt.Task != "" && !opt.NoApply && opt.Out != "-" && !opt.DryRun && opt.ResultServiceAccount == "" {
		opt.ResultServiceAccount, err = promptValue(scanner, out, "Existing result-reader ServiceAccount", "", true)
		if err != nil {
			return opt, err
		}
	}
	if err := finishCreateWizardOptions(&opt); err != nil {
		return opt, err
	}
	if opt.Out == "-" || opt.NoApply {
		opt.NoApply = true
		return opt, nil
	}
	if opt.DryRun {
		return opt, nil
	}
	if opt.Task != "" {
		fmt.Fprintln(out, createTaskAuthorityNotice)
	}
	for {
		apply, err := promptValue(scanner, out, fmt.Sprintf("Create Agent %q (Orka agent) in namespace %q? (Y/n)", opt.Name, opt.Namespace), "y", true)
		if err != nil {
			return opt, err
		}
		switch strings.ToLower(apply) {
		case "y", "yes":
			opt.NoApply = false
			return opt, nil
		case "n", "no":
			return opt, errCreateCancelled
		default:
			fmt.Fprintln(out, "  Answer y or n.")
		}
	}
}

func promptValue(scanner lineScanner, out io.Writer, label, fallback string, required bool) (string, error) {
	for {
		if fallback == "" {
			fmt.Fprintf(out, "%s: ", label)
		} else {
			fmt.Fprintf(out, "%s [%s]: ", label, fallback)
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		value := strings.TrimSpace(scanner.Text())
		if value == "" {
			value = fallback
		}
		if value != "" || !required {
			return value, nil
		}
		fmt.Fprintln(out, "  A value is required.")
	}
}

func slugAgentName(description string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(description) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	full := strings.Trim(b.String(), "-")
	const maxDefault = 32
	if len(full) <= maxDefault {
		return full
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(description))))
	suffix := fmt.Sprintf("%x", sum[:3])
	prefix := strings.TrimRight(full[:maxDefault-len(suffix)-1], "-")
	if boundary := strings.LastIndexByte(prefix, '-'); boundary >= 12 {
		prefix = prefix[:boundary]
	}
	if prefix == "" {
		prefix = "agent"
	}
	return prefix + "-" + suffix
}
