package agentfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// OrkaSourcePair retains native source roles separately. Its raw source revision
// is not package identity, execution evidence or write permission.
type OrkaSourcePair struct {
	AgentPath                          string
	Agent                              *OrkaSource
	Tests                              TestsSource
	SnapshotRevision                   string
	Unsupported                        []OrkaUnsupported
	agentObservation, testsObservation *pairObservation
}

// LoadOrkaPair reads explicit native source and selected companion without config.
func LoadOrkaPair(agentFile, testsOverride string) (*OrkaSourcePair, error) {
	if !orkaSourceName(agentFile) {
		return nil, unsupportedOrka(agentFile, "File", "select a NAME.agent.md native source")
	}
	path, err := pairAbsolute(agentFile, "File")
	if err != nil {
		return nil, err
	}
	target, _ := DefaultTestsPath(path)
	explicit := testsOverride != ""
	if explicit {
		target, err = pairAbsolute(testsOverride, "Tests")
		if err != nil {
			return nil, err
		}
	}
	if path == target {
		return nil, refuse(path, 1, 1, "Tests", "select different agent and tests files")
	}
	agentBytes, agentObs, err := readPairSource(path, "File", false)
	if err != nil {
		return nil, err
	}
	testsBytes, testsObs, err := readPairSource(target, "Tests", !explicit)
	if err != nil {
		return nil, err
	}
	present := testsObs.fileInfo != nil
	if present && os.SameFile(agentObs.fileInfo, testsObs.fileInfo) {
		return nil, refuse(path, 1, 1, "Tests", "select different agent and tests files")
	}
	p, err := ComposeOrkaSourcePair(path, agentBytes, TestsSelection{Path: target, Explicit: explicit}, testsBytes, present)
	if err != nil {
		return nil, err
	}
	// Recheck after parsing both sources, retaining the shared reader's absence,
	// file and parent replacement observations without claiming a write lock.
	if err := agentObs.check("File"); err != nil {
		return nil, err
	}
	if err := testsObs.check("Tests"); err != nil {
		return nil, err
	}
	p.agentObservation, p.testsObservation = agentObs, testsObs
	return p, nil
}

// ComposeOrkaSourcePair validates caller-held native source bytes without IO. It
// performs the same tuple validation as LoadOrkaPair but carries no filesystem
// observations, so the pure result never satisfies SameSourceVersion and grants no
// read or write permission. The selection key is recomputed from its cleaned path.
// A non-explicit selection must name the agent's default companion; an explicit
// one must name an existing (present) file. ParseOrkaSource and ParseTests copy both byte slices.
func ComposeOrkaSourcePair(agentPhysical string, agentBytes []byte, selection TestsSelection, testsBytes []byte, present bool) (*OrkaSourcePair, error) {
	if !orkaSourceName(agentPhysical) {
		return nil, unsupportedOrka(agentPhysical, "File", "select a NAME.agent.md native source")
	}
	if !filepath.IsAbs(agentPhysical) || hasParentTraversal(agentPhysical) {
		return nil, refuse(agentPhysical, 1, 1, "File", "provide an absolute resolved agent file path without parent traversal")
	}
	path := filepath.Clean(agentPhysical)
	if selection.Path == "" || !filepath.IsAbs(selection.Path) || hasParentTraversal(selection.Path) {
		return nil, refuse(path, 1, 1, "Tests", "provide an absolute resolved tests path without parent traversal")
	}
	target := filepath.Clean(selection.Path)
	if path == target {
		return nil, refuse(path, 1, 1, "Tests", "select different agent and tests files")
	}
	if defaultTarget, _ := DefaultTestsPath(path); !selection.Explicit && target != defaultTarget {
		return nil, refuse(path, 1, 1, "Tests", "select explicit tests for a non-default tests path")
	}
	if selection.Explicit && !present {
		return nil, refuse(target, 1, 1, "Tests", "select an existing explicit tests file")
	}
	agent, err := ParseOrkaSource(path, agentBytes)
	if err != nil {
		return nil, err
	}
	source := TestsSource{State: "absent", Selection: TestsSelection{Path: target, Explicit: selection.Explicit}}
	if present {
		source.Suite, err = ParseTests(target, testsBytes)
		if err != nil {
			return nil, err
		}
		source.State, source.Revision = "external", source.Suite.Revision
	}
	var snapshot string
	source.Selection.Key, snapshot = SourceRevisions(path, agent.Revision, target, source.State, source.Revision)
	p := &OrkaSourcePair{AgentPath: path, Agent: agent, Tests: source, SnapshotRevision: snapshot, Unsupported: append([]OrkaUnsupported{}, agent.Unsupported...)}
	if source.Suite == nil || len(source.Suite.Examples) == 0 {
		p.Unsupported = append(p.Unsupported, OrkaUnsupported{"Tests", "unrepresented"})
		return p, nil
	}
	seen := map[string]bool{}
	for i, example := range source.Suite.Examples {
		id := strings.ToLower(example.ID)
		if seen[id] {
			return nil, refuse(target, 1, 1, "Tests", "use test IDs distinct without regard to letter case")
		}
		seen[id] = true
		if example.Mocks != nil {
			p.Unsupported = append(p.Unsupported, OrkaUnsupported{fmt.Sprintf("Tests[%d].Mocks", i), "unrepresented"})
		}
		for j, assertion := range example.Assertions {
			field := fmt.Sprintf("Tests[%d].Assertions[%d]", i, j)
			switch assertion.Type {
			case "toolCalled", "toolNotCalled":
				p.Unsupported = append(p.Unsupported, OrkaUnsupported{field, "not_yet_supported"})
			case "llmJudge":
				if runtime.ValidateEvaluationAssertions([]runtime.EvaluationAssertion{assertion}) != nil {
					p.Unsupported = append(p.Unsupported, OrkaUnsupported{field, "unrepresented"})
				}
			}
		}
	}
	return p, nil
}

// SameSourceVersion requires observations from checked loads, not copied hashes.
func (p *OrkaSourcePair) SameSourceVersion(other *OrkaSourcePair) bool {
	return p != nil && other != nil && p.agentObservation != nil && other.agentObservation != nil && p.SnapshotRevision == other.SnapshotRevision && p.agentObservation.same(other.agentObservation) && p.testsObservation.same(other.testsObservation)
}
