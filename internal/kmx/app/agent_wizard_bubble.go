package app

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type createWizardCancelMsg struct{}

type createWizardStep uint8

const (
	createDescription createWizardStep = iota
	createName
	createNamespace
	createProviderType
	createModel
	createAzureDeployment
	createAzureBaseURL
	createAzureAPIVersion
	createSecret
	createResultAccount
	createConfirm
	createDone
)

type createWizardModel struct {
	opt       CreateOptions
	input     textinput.Model
	step      createWizardStep
	selection int
	err       error
	cancelled bool
}

func (m *createWizardModel) startReferenceStep(step createWizardStep, placeholder string, validate func(string) error) {
	m.step = step
	m.input.Placeholder = placeholder
	m.input.CharLimit = 0
	m.input.Validate = func(value string) error {
		if value == "" {
			return fmt.Errorf("a value is required")
		}
		if validate != nil {
			return validate(value)
		}
		return nil
	}
	m.input.SetValue("")
	m.input.Focus()
}

func requiredDescription(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("a description is required")
	}
	return nil
}

func validateAgentName(value string) error {
	return scaffold.ValidateName(strings.TrimSpace(value))
}

func (m createWizardModel) Init() tea.Cmd {
	if m.step < createConfirm {
		return textinput.Blink
	}
	return nil
}
