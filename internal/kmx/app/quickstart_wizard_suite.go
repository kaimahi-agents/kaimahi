package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const quickstartInstructionsPurpose createWizardStep = createDone + 1

// This form shares the text input only, never live-create collection or bundle
// rendering. Source review and execution consent belong to the shared pane.
func newQuickstartSourceForm(opt CreateOptions) (createWizardModel, error) {
	if err := refuseWizardCredentials(opt.Name, opt.Description); err != nil {
		return createWizardModel{}, err
	}
	if opt.Name != "" {
		if err := scaffold.ValidateName(opt.Name); err != nil {
			return createWizardModel{}, err
		}
	}
	if err := resolveOrkaInstructions(&opt); err != nil {
		return createWizardModel{}, err
	}
	input := textinput.New()
	input.Prompt = "> "
	input.CharLimit = 0
	input.SetWidth(60)
	m := createWizardModel{opt: opt, input: input}
	startQuickstartSourceField(&m)
	return m, nil
}

func startQuickstartSourceField(m *createWizardModel) {
	switch {
	case strings.TrimSpace(m.opt.Description) == "":
		m.startReferenceStep(createDescription, "What should this agent do?", requiredDescription)
		m.input.SetValue(m.opt.descriptionDefault)
	case m.opt.Name == "":
		m.startReferenceStep(createName, "agent-name", validateAgentName)
		m.input.SetValue(slugAgentName(m.opt.Description))
	case m.opt.Namespace == "":
		m.startReferenceStep(createNamespace, "Namespace the Orka controller watches", scaffold.ValidateNamespace)
	case m.opt.Model == "":
		m.startReferenceStep(createModel, "Exact model identifier", scaffold.ValidateSingleLineText)
	case m.opt.Instructions == "" && m.opt.InstructionText == "":
		m.startReferenceStep(quickstartInstructionsPurpose, "Purpose for generated instructions", requiredDescription)
		m.input.SetValue(m.opt.Description)
	default:
		m.input.Blur()
		m.step = createConfirm
		m.selection = 0
	}
}

func updateQuickstartSourceForm(m createWizardModel, msg tea.Msg) (createWizardModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if m.step == createConfirm {
			switch key.Code {
			case tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown, tea.KeyTab, 'j', 'k':
				m.selection = 1 - m.selection
			case tea.KeyEnter:
				m.cancelled = m.selection == 1
				m.step = createDone
			}
			return m, nil
		}
		if key.Code == tea.KeyEnter {
			value := strings.TrimSpace(m.input.Value())
			if err := refuseWizardCredentials(value); err != nil {
				m.err = err
				return m, nil
			}
			if m.input.Validate != nil {
				m.err = m.input.Validate(value)
			}
			if m.err != nil {
				return m, nil
			}
			switch m.step {
			case createDescription:
				m.opt.Description = value
			case createName:
				m.opt.Name = value
			case createNamespace:
				m.opt.Namespace = value
			case createModel:
				m.opt.Model = value
			case quickstartInstructionsPurpose:
				m.opt.InstructionText = "You are " + m.opt.Name + ", a native Orka agent. Your purpose is: " + value + "\nAnswer briefly and say plainly when you do not know something."
			}
			startQuickstartSourceField(&m)
			return m, textinput.Blink
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.err = m.input.Err
	return m, cmd
}

func validateQuickstartSourceOptions(opt CreateOptions) error {
	remedy := "quickstart authors a tool-free native Orka workspace; author source with kmx create, then review cases and use eval → push → deploy"
	switch {
	case opt.Tools != "":
		return fmt.Errorf("quickstart tool shortcuts are retired; suite ToolProvider support is a future requirement; %s", remedy)
	case opt.Skills != "":
		return fmt.Errorf("quickstart --skills is unsupported; %s", remedy)
	case opt.ProviderType != "" && opt.ProviderType != "openai", opt.Runtime != "" && opt.Runtime != "orka", opt.KagentRuntime != "", opt.AzureDeployment != "", opt.AzureAPIVersion != "":
		return fmt.Errorf("quickstart requires native Orka with a Ready openai Provider and exact model; %s", remedy)
	case opt.Out != "", opt.BundlePath != "", opt.NoApply, opt.DryRun, opt.SchemaTarget != "":
		return fmt.Errorf("quickstart manifest/bundle/preview flags are unsupported; %s", remedy)
	case opt.BaseURL != "", opt.Secret != "", opt.SecretKey != "", opt.AgentRequestsPerMinute != "", opt.AgentTokensPerMinute != "", opt.ProviderRequestsPerMinute != "", opt.ProviderTokensPerMinute != "", opt.Coordination, len(opt.AllowedAgents) > 0:
		return fmt.Errorf("quickstart Provider/limit/coordination overrides are unsupported; select the existing Ready openai Provider during workspace review; %s", remedy)
	case opt.ResultServiceAccount != "", opt.OrkaAPIService != "", opt.ResultPort != "":
		return fmt.Errorf("quickstart result transport overrides are unsupported; configure native result access separately; %s", remedy)
	case opt.Task != "" && strings.TrimSpace(opt.Task) == "":
		return fmt.Errorf("quickstart --task requires a nonblank question after workspace Apply and Verify; %s", remedy)
	}
	return nil
}

// Each invocation owns a fresh private source, outside the prepared cache.
// The source survives cancellation and is never converted into a legacy bundle.
func (a *App) authorQuickstartSource(ctx context.Context, opt CreateOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateQuickstartSourceOptions(opt); err != nil {
		return "", err
	}
	if err := resolveOrkaInstructions(&opt); err != nil {
		return "", err
	}
	if err := scaffold.ValidateNamespace(opt.Namespace); err != nil {
		return "", err
	}
	base, err := privateWorkspaceState("quickstart-authored")
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(base, "instructions-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	instructions, err := resolveOrkaInstructionText(opt)
	if err != nil {
		return "", err
	}
	input := filepath.Join(stage, "instructions.md")
	if err := os.WriteFile(input, []byte(instructions), 0600); err != nil {
		return "", err
	}
	source := workspace.CreateOptions{Name: opt.Name, Description: opt.Description, Model: opt.Model, InstructionsFile: input}
	if err := workspace.ValidateCreateOptions(source); err != nil {
		return "", err
	}
	root, err := os.MkdirTemp(base, opt.Name+"-")
	if err != nil {
		return "", err
	}
	if err := workspace.Create(ctx, root, source); err != nil {
		return root, err
	}
	return root, nil
}
