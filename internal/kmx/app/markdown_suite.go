package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

type MarkdownSuiteMapping struct {
	Source                   string `json:"source"`
	AgentDigest              string `json:"agentDigest"`
	TestsDigest              string `json:"testsDigest"`
	PairDigest               string `json:"pairDigest"`
	Workspace                string `json:"workspace"`
	Member                   string `json:"member"`
	Runtime                  string `json:"runtime"`
	PreviewModel             string `json:"previewModel"`
	DeploymentModel          string `json:"deploymentModel"`
	Cases                    int    `json:"cases"`
	RequiresNativeEvaluation bool   `json:"requiresNativeEvaluation"`
}

// MapMarkdownSuite creates editable native source; publication and deployment
// remain the existing evidence-gated operations. It never imports preview proof.
func MapMarkdownSuite(ctx context.Context, source, tests, destination, model, name string) (MarkdownSuiteMapping, error) {
	var report MarkdownSuiteMapping
	if strings.TrimSpace(destination) == "" {
		return report, errors.New("--workspace requires an output directory")
	}
	if strings.TrimSpace(model) == "" || model == "auto" || model == "default" {
		return report, errors.New("--model must name the exact model served by the target Orka inference Provider")
	}
	a, err := LoadMarkdownAgent(source, tests)
	if err != nil {
		return report, err
	}
	grant, _, err := readCurrentToolGrant(a.Path)
	if err != nil {
		return report, err
	}
	if grant != nil {
		return report, errors.New("local tool bindings cannot be mapped to native Orka deployment")
	}
	if len(a.Tools) > 0 {
		return report, errors.New("cannot map tool-enabled md agents: mock tools are not deployable tool bindings")
	}
	for _, mocks := range a.Mocks {
		if len(mocks) > 0 {
			return report, errors.New("cannot map mock fixtures into native deployment")
		}
	}
	if len(a.Examples) == 0 {
		return report, errors.New("mapping requires paired tests; provide --tests or a conventional companion")
	}
	if name == "" {
		name = a.Name
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return report, err
	}
	if err = a.CheckCurrent(); err != nil {
		return report, err
	}
	input := workspace.MappedAgent{Name: name, Description: a.Description, Model: model, Instructions: []byte(a.Instructions), Cases: a.Examples}
	if err = workspace.CreateMappedOrka(ctx, destination, input); err != nil {
		return report, err
	}
	return MarkdownSuiteMapping{Source: a.Path, AgentDigest: a.Revision, TestsDigest: a.TestsRevision, PairDigest: a.PairRevision, Workspace: destination, Member: name, Runtime: "orka", PreviewModel: a.Model, DeploymentModel: model, Cases: len(a.Examples), RequiresNativeEvaluation: true}, nil
}
