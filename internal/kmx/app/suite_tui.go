package app

// SuiteConsoleOptions supplies image build/transfer inputs to the shared console.
// Source review, evaluation consent and lift review belong to agent_tui_create.
type SuiteConsoleOptions struct {
	Build               SuiteBuildOptions
	Workspace, Registry string
	PlainHTTP           bool
}

func (a *App) SuiteConsole(opt SuiteConsoleOptions) error {
	return a.AgentTUI(AgentTUIOptions{Workspace: opt.Workspace, Suite: opt})
}
