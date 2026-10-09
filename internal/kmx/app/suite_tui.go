package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/imagelift"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type SuiteConsoleOptions struct {
	Build               SuiteBuildOptions
	Workspace, Registry string
	PlainHTTP           bool
}

type suiteConsoleResult struct {
	message string
	entries []SuiteWorkspaceEntry
	records []WorkspaceDeployment
	plan    *agentruntime.PreparedSuiteDeployment
	pub     SuitePublication
	env     imagelift.Environment
	err     error
}

type suiteConsole struct {
	app                  *App
	opt                  SuiteConsoleOptions
	ctx                  context.Context
	cancel               context.CancelFunc
	entries              []SuiteWorkspaceEntry
	records              []WorkspaceDeployment
	selected, deployment int
	width, height        int
	busy, quitting       bool
	form                 string
	step                 int
	values               []string
	input                textinput.Model
	output               viewport.Model
	plan                 *agentruntime.PreparedSuiteDeployment
	pub                  SuitePublication
	env                  imagelift.Environment
}

func newSuiteConsole(a *App, opt SuiteConsoleOptions) suiteConsole {
	input := textinput.New()
	input.CharLimit = 4096
	input.SetWidth(76)
	output := viewport.New(viewport.WithWidth(96), viewport.WithHeight(16))
	return suiteConsole{app: a, opt: opt, ctx: a.operationContext(), width: 100, height: 32, input: input, output: output}
}

func (m suiteConsole) Init() tea.Cmd { return m.reload() }

func (m suiteConsole) reload() tea.Cmd {
	return func() tea.Msg {
		entries, err := ListSuiteWorkspace(m.opt.Workspace)
		return suiteConsoleResult{entries: entries, err: err, message: "Select a local suite, or press n to create source. Model selection happens at lift."}
	}
}

func (m *suiteConsole) work(label string, fn func(context.Context) suiteConsoleResult) tea.Cmd {
	m.busy = true
	m.output.SetContent(label + "\nEsc cancels and waits for the operation to stop.")
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	return func() tea.Msg { return fn(ctx) }
}

func (m *suiteConsole) startForm(kind string) {
	m.form, m.step, m.values = kind, 0, nil
	m.input.SetValue("")
	m.input.Focus()
	if kind == "lift" {
		m.input.Placeholder = "path to environment JSON (model/connection and Secret references)"
	}
	if kind == "create" {
		m.input.Placeholder = "suite name"
	}
	if kind == "chat" {
		m.input.Placeholder = "prompt for deployed agent"
	}
}

func (m suiteConsole) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.output.SetWidth(max(20, msg.Width-4))
		m.output.SetHeight(max(4, msg.Height-14))
		m.input.SetWidth(max(20, msg.Width-6))
		return m, nil
	case suiteConsoleResult:
		m.busy = false
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		if m.quitting {
			return m, tea.Quit
		}
		if msg.entries != nil {
			m.entries = msg.entries
			if m.selected >= len(m.entries) {
				m.selected = 0
			}
		}
		if msg.records != nil {
			m.records = msg.records
			if m.deployment >= len(m.records) {
				m.deployment = 0
			}
		}
		if msg.plan != nil {
			m.plan, m.pub, m.env = msg.plan, msg.pub, msg.env
		}
		if msg.err != nil {
			m.output.SetContent("Operation stopped: " + tuiOneLine(msg.err.Error()) + "\nCompleted writes remain inspectable; refresh before retrying.")
		} else {
			m.output.SetContent(safeTerminal(msg.message))
		}
		m.output.GotoTop()
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if m.busy {
			if key == "esc" || key == "ctrl+c" {
				m.cancel()
				m.output.SetContent("Cancelling; waiting for operation outcome…")
			}
			return m, nil
		}
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.form != "" {
			if key == "esc" {
				m.form = ""
				return m, nil
			}
			if key == "enter" {
				return m.submitForm()
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		if key == "q" {
			return m, tea.Quit
		}
		if key == "esc" {
			m.plan = nil
			m.output.SetContent("Review cancelled; no deployment submitted.")
			return m, nil
		}
		if key == "n" {
			m.plan = nil
			m.startForm("create")
			return m, textinput.Blink
		}
		if key == "r" {
			m.plan = nil
			m.records = nil
			return m, m.reload()
		}
		if len(m.entries) == 0 {
			return m, nil
		}
		entry := m.entries[m.selected]
		switch key {
		case "j", "down", "k", "up":
			if key == "j" || key == "down" {
				m.selected = (m.selected + 1) % len(m.entries)
			} else {
				m.selected = (m.selected + len(m.entries) - 1) % len(m.entries)
			}
			m.plan = nil
			m.records = nil
			return m, nil
		case "b":
			if m.opt.Registry == "" {
				m.output.SetContent("Supply --registry <registry/repository> to build and publish.")
				return m, nil
			}
			m.plan = nil
			return m, m.work("Building/publishing suite; checking existing images for reuse…", func(ctx context.Context) suiteConsoleResult {
				pub, err := m.app.PublishSuiteWorkspace(ctx, m.opt.Workspace, entry.Name, m.opt.Registry, m.opt.PlainHTTP, m.opt.Build)
				entries, _ := ListSuiteWorkspace(m.opt.Workspace)
				return suiteConsoleResult{entries: entries, err: err, message: "Published: " + DescribePublication(pub) + "\nPress l to bind inference and lift."}
			})
		case "l":
			m.plan = nil
			m.startForm("lift")
			return m, textinput.Blink
		case "enter":
			if m.plan == nil {
				return m, nil
			}
			prepared, pub, env := m.plan, m.pub, m.env
			m.plan = nil
			return m, m.work("Deploying reviewed suite to "+env.Context+"…", func(ctx context.Context) suiteConsoleResult {
				receipt, err := m.app.ApplyWorkspaceLift(ctx, m.opt.Workspace, entry.Name, pub, env, prepared, prepared.Summary().PlanDigest)
				records, _ := ReadWorkspaceDeployments(m.opt.Workspace, entry.Name)
				return suiteConsoleResult{records: records, err: err, message: "Deployment: " + receipt.State + "\nPress s for status, d to select a deployment, c to chat."}
			})
		case "s":
			m.plan = nil
			return m, m.work("Reading recorded deployments and live status…", func(ctx context.Context) suiteConsoleResult {
				records, err := ReadWorkspaceDeployments(m.opt.Workspace, entry.Name)
				var text strings.Builder
				for _, record := range records {
					fmt.Fprintf(&text, "%s · %s / %s\n", record.Environment.Name, record.Environment.Context, record.Environment.Namespace)
					statuses, e := m.app.SuiteDeploymentStatus(ctx, record)
					if e != nil {
						fmt.Fprintf(&text, "  unknown: %s\n", tuiOneLine(e.Error()))
						continue
					}
					for _, status := range statuses {
						fmt.Fprintf(&text, "  %s · ready=%t · model=%s\n  image=%s\n", status.Agent, status.Ready, status.Model, status.Image)
					}
				}
				if len(records) == 0 {
					text.WriteString("No recorded deployments. Build and lift this source first.")
				}
				return suiteConsoleResult{records: records, err: err, message: text.String()}
			})
		case "d":
			m.plan = nil
			if len(m.records) > 0 {
				m.deployment = (m.deployment + 1) % len(m.records)
			}
			return m, nil
		case "c":
			m.plan = nil
			if len(m.records) == 0 {
				m.output.SetContent("Press s to discover recorded deployments first.")
				return m, nil
			}
			m.startForm("chat")
			return m, textinput.Blink
		}
	}
	var cmd tea.Cmd
	m.output, cmd = m.output.Update(msg)
	return m, cmd
}

func (m suiteConsole) submitForm() (tea.Model, tea.Cmd) {
	value := m.input.Value()
	if strings.TrimSpace(value) == "" {
		return m, nil
	}
	if m.form == "create" {
		m.values = append(m.values, value)
		m.step++
		prompts := []string{"suite name", "instructions", "minimum context tokens (e.g. 8192)", "minimum output tokens (e.g. 1024)"}
		if m.step < len(prompts) {
			m.input.SetValue("")
			m.input.Placeholder = prompts[m.step]
			return m, nil
		}
		m.form = ""
		values := append([]string(nil), m.values...)
		return m, m.work("Creating local source…", func(context.Context) suiteConsoleResult {
			contextTokens, e1 := strconv.Atoi(values[2])
			outputTokens, e2 := strconv.Atoi(values[3])
			if e1 != nil || e2 != nil {
				return suiteConsoleResult{err: errors.New("token limits must be integers")}
			}
			if filepath.Base(values[0]) != values[0] || strings.ContainsAny(values[0], "/\\") {
				return suiteConsoleResult{err: errors.New("invalid source name")}
			}
			err := agentsuite.CreateHTTPSource(filepath.Join(m.opt.Workspace, values[0]), agentsuite.CreateRequest{Name: values[0], Instructions: values[1], Inference: agentsuite.InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: contextTokens, OutputTokens: outputTokens}})
			entries, _ := ListSuiteWorkspace(m.opt.Workspace)
			return suiteConsoleResult{entries: entries, err: err, message: "Local source created. Press b to build and publish; l to lift."}
		})
	}
	entry := m.entries[m.selected]
	if m.form == "lift" {
		m.form = ""
		return m, m.work("Resolving images, inference capabilities and whole-suite preflight…", func(ctx context.Context) suiteConsoleResult {
			pub, err := ReadSuitePublication(m.opt.Workspace, entry.Name)
			if err != nil {
				return suiteConsoleResult{err: err}
			}
			if err := CheckWorkspacePublication(m.opt.Workspace, entry.Name, pub); err != nil {
				return suiteConsoleResult{err: err}
			}
			env, err := ReadSuiteEnvironment(value)
			if err != nil {
				return suiteConsoleResult{err: err}
			}
			env, err = BindSuitePublication(pub, env)
			if err != nil {
				return suiteConsoleResult{err: err}
			}
			prepared, err := m.app.PrepareSuiteLift(ctx, pub.Suite, env)
			if err != nil {
				return suiteConsoleResult{err: err}
			}
			var text strings.Builder
			fmt.Fprintf(&text, "REVIEW LIFT\nTarget: %s / %s\nCluster UID: %s\nSuite: %s\nPlan: %s\n", env.Context, env.Namespace, env.ClusterUID, pub.Suite, prepared.Summary().PlanDigest)
			for _, member := range prepared.Summary().Members {
				binding := env.Members[member.Agent]
				fmt.Fprintf(&text, "\n%s → %s\nImage: %s\n", member.Agent, member.Name, binding.Image)
				if binding.Inference != nil {
					fmt.Fprintf(&text, "Model: %s\nEndpoint: %s\nCapabilities: operator-declared\n", binding.Inference.Model, binding.Inference.Endpoint)
				}
			}
			text.WriteString("\nEnter deploys this exact plan. Esc cancels. PgUp/PgDn scroll.")
			return suiteConsoleResult{plan: prepared, pub: pub, env: env, message: text.String()}
		})
	}
	if m.form == "chat" {
		m.form = ""
		record := m.records[m.deployment]
		var members []string
		for id := range record.Environment.Members {
			members = append(members, id)
		}
		sort.Strings(members)
		if len(members) != 1 {
			m.output.SetContent("Multi-member chat: use kmx suite run --member <id> to select the member explicitly.")
			return m, nil
		}
		return m, m.work("Invoking deployed image on "+record.Environment.Context+"…", func(ctx context.Context) suiteConsoleResult {
			answer, err := m.app.RunSuiteDeployment(ctx, record, members[0], value)
			return suiteConsoleResult{err: err, message: "YOU\n" + value + "\n\nAGENT\n" + answer + "\n\nPress c for another independent request."}
		})
	}
	return m, nil
}

func (m suiteConsole) View() tea.View {
	var text strings.Builder
	text.WriteString("AGENTSUITE WORKSPACE · build once, bind inference at lift\n")
	fmt.Fprintf(&text, "Workspace: %s\nRegistry: %s\n", tuiOneLine(m.opt.Workspace), tuiOneLine(m.opt.Registry))
	for i, entry := range m.entries {
		if i > 3 && i != m.selected {
			continue
		}
		marker := "  "
		if i == m.selected {
			marker = "› "
		}
		state := "source"
		if entry.Publication != nil {
			state = "published"
		}
		fmt.Fprintf(&text, "%s%s · %s\n", marker, tuiOneLine(entry.Name), state)
	}
	if len(m.records) > 0 {
		record := m.records[m.deployment]
		fmt.Fprintf(&text, "Selected deployment: %s · %s\n", tuiOneLine(record.Environment.Name), tuiOneLine(record.Environment.Context))
	}
	text.WriteString("n create · b build/publish · l lift · s status · d deployment · c chat · r refresh · q quit\n\n")
	text.WriteString(m.output.View())
	if m.form != "" {
		text.WriteString("\n" + m.form + " · " + m.input.View() + "\nEnter accepts · Esc cancels")
	}
	v := tea.NewView(text.String())
	v.AltScreen = true
	return v
}

func (a *App) SuiteConsole(opt SuiteConsoleOptions) error {
	if a.Stdin == nil || !isTerminalFile(a.Stdin) || !isInteractiveTerminal(a.Out) {
		return errors.New("suite console requires an interactive terminal")
	}
	if err := os.MkdirAll(opt.Workspace, 0755); err != nil {
		return err
	}
	worker := *a
	worker.Err = io.Discard
	m := newSuiteConsole(&worker, opt)
	filter := func(model tea.Model, msg tea.Msg) tea.Msg {
		if _, ok := msg.(tea.InterruptMsg); ok {
			return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		}
		return msg
	}
	_, err := tea.NewProgram(m, tea.WithInput(a.Stdin), tea.WithOutput(a.Out), tea.WithFilter(filter)).Run()
	return err
}
