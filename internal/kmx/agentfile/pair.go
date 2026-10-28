package agentfile

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// TestsSelection names a physical target, not a search policy. Key binds its
// normalized absolute path; Explicit records intent but is not part of identity.
type TestsSelection struct {
	Path, Key string
	Explicit  bool
}

// TestsSource distinguishes an external suite, inline Examples (even []), an
// absent default companion and an agent with no companion rule (none).
type TestsSource struct {
	State, Revision string
	Selection       TestsSelection
	Suite           *Tests
}

// Pair keeps source roles separate: Agent is the original parsed agent, never
// an executable merge of external cases. SnapshotRevision is the combined revision,
// not a metadata-sensitive source version or write authority. Checked observations
// are available only through LoadPair.
type Pair struct {
	Agent                              *Agent
	AgentPath                          string
	Tests                              TestsSource
	SnapshotRevision                   string
	agentObservation, testsObservation *pairObservation
}

// DefaultTestsPath implements fixed pairing without discovery. Only the exact .github/agents
// parent uses sibling agent-tests; deeper folders follow the ordinary beside rule.
func DefaultTestsPath(agentPhysical string) (string, bool) {
	leaf := filepath.Base(agentPhysical)
	if !strings.HasSuffix(leaf, ".agent.md") || leaf == ".agent.md" {
		return "", false
	}
	parent := filepath.Dir(agentPhysical)
	if filepath.Base(parent) == "agents" && filepath.Base(filepath.Dir(parent)) == ".github" {
		parent = filepath.Join(filepath.Dir(parent), "agent-tests")
	}
	return filepath.Join(parent, strings.TrimSuffix(leaf, ".md")+".test.md"), true
}

// ComposePair composes caller-checked reads at absolute resolved target paths.
// It performs no IO, grants no read/write permission and has no checked-currentness
// observations. A caller must not install external cases in an execution path
// without separately binding their source and capture destination.
func ComposePair(agentPhysical string, agent *Agent, selection TestsSelection, testsBytes []byte, present bool) (*Pair, error) {
	fail := func(problem string) (*Pair, error) { return nil, refuse(agentPhysical, 1, 1, "Tests", problem) }
	if agent == nil {
		return fail("provide a parsed agent file")
	}
	if !filepath.IsAbs(agentPhysical) || hasParentTraversal(agentPhysical) {
		return fail("provide an absolute resolved agent file path without parent traversal")
	}
	agentPhysical = filepath.Clean(agentPhysical)
	if strings.HasSuffix(filepath.Base(agentPhysical), ".agent.test.md") {
		return fail("select an agent file, not a tests file")
	}
	_, supported := DefaultTestsPath(agentPhysical)
	if selection.Path != "" {
		if !supported {
			return fail("select a supported *.agent.md file for companion tests")
		}
		if !filepath.IsAbs(selection.Path) || hasParentTraversal(selection.Path) {
			return fail("provide an absolute resolved tests path without parent traversal")
		}
		selection.Path = filepath.Clean(selection.Path)
		if selection.Path == agentPhysical {
			return fail("select different agent and tests files")
		}
		selection.Key = pairHash("kmx-tests-selection-v1", selection.Path)
	} else {
		selection.Key = ""
		if present || selection.Explicit {
			return fail("select an existing explicit tests file")
		}
	}
	if selection.Explicit && !present {
		return fail("select an existing explicit tests file")
	}
	_, inline := agent.Locations["Examples"]
	source := TestsSource{Selection: selection, State: "none"}
	switch {
	case present:
		if inline && !selection.Explicit {
			return fail("choose explicit tests to override inline Examples; default companion conflicts with inline tests")
		}
		suite, err := ParseTests(selection.Path, testsBytes)
		if err != nil {
			return nil, err
		}
		validation := *agent
		validation.Examples = suite.Examples
		if err := checkExampleTools(selection.Path, &validation); err != nil {
			return nil, err
		}
		source.State, source.Revision, source.Suite = "external", suite.Revision, suite
	case inline:
		source.State, source.Revision = "inline", agent.Revision
		source.Suite = &Tests{Examples: agent.Examples, Revision: agent.Revision}
	case selection.Path != "":
		source.State = "absent"
	}
	p := &Pair{Agent: agent, AgentPath: agentPhysical, Tests: source}
	p.SnapshotRevision = pairHash("kmx-agent-tests-snapshot-v1", agentPhysical, agent.Revision, selection.Key, source.State, source.Revision)
	return p, nil
}

// SameSourceVersion compares two checked loads, without reopening paths. Equal
// combined revisions (including pure compositions) never establish observed currentness.
func (p *Pair) SameSourceVersion(other *Pair) bool {
	return p != nil && other != nil && p.agentObservation != nil && other.agentObservation != nil && p.SnapshotRevision == other.SnapshotRevision &&
		p.agentObservation.same(other.agentObservation) && p.testsObservation.same(other.testsObservation)
}

func pairHash(domain string, values ...string) string {
	h := sha256.New()
	for _, value := range append([]string{domain}, values...) {
		fmt.Fprintf(h, "%d:%s", len(value), value)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
