package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// ListTypedAgents uses the same typed rows and inventory loaders as the console.
// Local files remain visible even if an independently requested cluster fails.
func ListTypedAgents(ctx context.Context, a *App, out io.Writer, directory, kind, namespace string) error {
	return ListTypedAgentsOutput(ctx, a, out, directory, kind, namespace, "table")
}
func ListTypedAgentsOutput(ctx context.Context, a *App, out io.Writer, directory, kind, namespace, output string) error {
	var rows []agentTUIAgent
	var problem error
	if kind != "orka" {
		local, err := markdownAgentInventory(directory)
		if err != nil {
			return err
		}
		rows = append(rows, local...)
	}
	if kind != "md" && a != nil {
		if _, err := exec.LookPath("kubectl"); err == nil {
			env := agentTUIEnvironment{Name: a.Cfg.KubeContext}
			remote, e := a.agentTUIInventory(ctx, env, namespace)
			rows = append(rows, remote...)
			problem = e
		} else if kind == "orka" {
			problem = errors.New("kubectl is required to list Orka agents")
		}
	}
	var cells [][]string
	if output == "json" || output == "yaml" {
		type rowView struct {
			Name       string `json:"name"`
			Type       string `json:"type"`
			State      string `json:"state"`
			Model      string `json:"model"`
			Source     string `json:"source"`
			TestStatus string `json:"testStatus,omitempty"`
		}
		values := []rowView{}
		for _, r := range rows {
			source := r.DefinitionPath
			if source == "" {
				source = r.Namespace + "/" + r.Name
			}
			values = append(values, rowView{r.Name, r.Runtime, r.Ready, r.Model, source, r.TestStatus})
		}
		return errors.Join(problem, cliui.WriteStructured(out, output, values))
	}
	for _, row := range rows {
		status, source := row.Ready, row.Namespace+"/"+row.Name
		if row.Runtime == "md" {
			status, source = row.TestStatus, row.DefinitionPath
		}
		cells = append(cells, []string{safeTerminal(row.Name), row.Runtime, safeTerminal(status), safeTerminal(row.Model), safeTerminal(source)})
	}
	_, writeErr := fmt.Fprintln(out, cliui.New(out).TableWithRoles([]string{"NAME", "TYPE", "STATUS", "MODEL", "SOURCE"}, cells, []cliui.ColumnRole{cliui.ColumnText, cliui.ColumnText, cliui.ColumnState}))
	return errors.Join(writeErr, problem)
}

type LocalEnvironmentStatus struct {
	Environment    string             `json:"environment"`
	Directory      string             `json:"directory"`
	CopilotCLI     string             `json:"copilotCLI"`
	Authentication string             `json:"authentication"`
	LocalInference string             `json:"localInference"`
	Agents         []LocalAgentStatus `json:"agents"`
	NextSteps      []string           `json:"nextSteps"`
	Problems       []string           `json:"problems"`
}
type LocalAgentStatus struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Definition string `json:"definition"`
	Tests      string `json:"tests"`
	Status     string `json:"status"`
}

// LocalStatus is a first-run read path: no workspace marker, cluster config,
// preference creation, login flow, model request or test execution is required.
func LocalStatus(ctx context.Context, out io.Writer, directory, output string) error {
	return localStatus(ctx, out, directory, output, detectCopilotCLI, copilotLoginStatus)
}
func localStatus(ctx context.Context, out io.Writer, directory, output string, detect func() string, auth func(context.Context, string) string) error {
	if output != "table" && output != "json" {
		return errors.New("status output must be table or json")
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	report := LocalEnvironmentStatus{Environment: "local", Directory: root, CopilotCLI: "not installed", Authentication: "not checked", LocalInference: "not configured", Agents: []LocalAgentStatus{}, NextSteps: []string{}, Problems: []string{}}
	prefs, err := ReadMarkdownPreferences()
	if err != nil {
		report.LocalInference = "unavailable"
		report.Problems = append(report.Problems, "Local inference preferences unavailable: "+err.Error())
	} else if prefs.LocalAgents.DefaultProvider != "" {
		report.LocalInference = prefs.LocalAgents.DefaultProvider + " (saved default)"
	} else if prefs.LocalAgents.CopilotChoice == "declined" {
		report.LocalInference = "not configured (Copilot declined)"
	}
	executable := detect()
	if executable != "" {
		report.CopilotCLI = "installed"
		switch auth(ctx, executable) {
		case "logged in":
			report.Authentication = "signed in"
		case "not logged in":
			report.Authentication = "not signed in"
			report.NextSteps = append(report.NextSteps, "Sign in: copilot login")
		default:
			report.Authentication = "unknown"
			report.Problems = append(report.Problems, "Copilot sign-in status unavailable; check copilot --version and copilot login")
		}
	} else {
		report.NextSteps = append(report.NextSteps, "Install Copilot CLI: https://docs.github.com/en/copilot/how-tos/copilot-cli/cli-getting-started")
	}
	if err == nil && prefs.LocalAgents.DefaultProvider == "" {
		report.NextSteps = append(report.NextSteps, "Choose local inference: kmx settings --provider copilot")
	}
	rows, inventoryErr := markdownAgentInventory(root)
	if inventoryErr != nil {
		report.Problems = append(report.Problems, "Local agent inventory unavailable: "+inventoryErr.Error())
	}
	for _, row := range rows {
		report.Agents = append(report.Agents, LocalAgentStatus{Name: row.Name, Type: row.Runtime, Definition: row.DefinitionPath, Tests: row.TestsPath, Status: row.TestStatus})
		if row.PromptError != "" {
			report.Problems = append(report.Problems, row.PromptError)
		}
	}
	if inventoryErr == nil && len(rows) == 0 {
		report.NextSteps = append(report.NextSteps, "Create an agent: kmx create hello --type md --directory "+fmt.Sprintf("%q", root))
	}
	var writeErr error
	if output == "json" {
		writeErr = json.NewEncoder(out).Encode(report)
	} else {
		ui := cliui.New(out)
		sections := []string{ui.Fields([]cliui.Field{
			{Label: "Environment", Value: report.Environment},
			{Label: "Directory", Value: safeTerminal(root)},
			{Label: "Copilot CLI", Value: report.CopilotCLI},
			{Label: "Authentication", Value: report.Authentication},
			{Label: "Local inference", Value: report.LocalInference},
			{Label: "Agents", Value: fmt.Sprintf("%d md", len(report.Agents))},
		})}
		if len(report.Agents) > 0 {
			var cells [][]string
			for _, a := range report.Agents {
				cells = append(cells, []string{safeTerminal(a.Name), a.Type, safeTerminal(a.Status)})
			}
			sections = append(sections, ui.TableWithRoles([]string{"NAME", "TYPE", "TEST STATUS"}, cells, []cliui.ColumnRole{cliui.ColumnText, cliui.ColumnText, cliui.ColumnState}))
		}
		for _, problem := range report.Problems {
			sections = append(sections, ui.Fields([]cliui.Field{{Label: "Unavailable", Value: safeTerminal(problem)}}))
		}
		for _, next := range report.NextSteps {
			sections = append(sections, ui.Fields([]cliui.Field{{Label: "Next", Value: safeTerminal(next)}}))
		}
		_, writeErr = fmt.Fprintln(out, strings.Join(sections, "\n\n"))
	}
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), writeErr)
	}
	if len(report.Problems) > 0 {
		return errors.Join(errors.New("local status incomplete; see reported problems"), writeErr)
	}
	return writeErr
}
