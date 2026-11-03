package mcp

import "github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"

// Editable source metadata is deliberately not an executable agentfile.Pair.
// A partial save can leave independently valid sources with invalid composition.
type pairSources struct {
	Agent            *agentfile.Agent
	AgentPath        string
	Tests            agentfile.TestsSource
	SnapshotRevision string
}

func sourcesFor(p *agentfile.Pair) pairSources {
	return pairSources{p.Agent, p.AgentPath, p.Tests, p.SnapshotRevision}
}
func authoringSources(path string, a *agentfile.Agent, selection agentfile.TestsSelection, raw []byte) (pairSources, error) {
	if path == selection.Path {
		return pairSources{}, refusal("invalid_file")
	}
	if _, ok := agentfile.DefaultTestsPath(path); !ok {
		return pairSources{}, refusal("invalid_file")
	}
	suite, err := agentfile.ParseTests(selection.Path, raw)
	if err != nil {
		return pairSources{}, refusal("invalid_file")
	}
	key, snapshot := agentfile.SourceRevisions(path, a.Revision, selection.Path, "external", suite.Revision)
	selection.Key = key
	return pairSources{a, path, agentfile.TestsSource{State: "external", Revision: suite.Revision, Selection: selection, Suite: suite}, snapshot}, nil
}
