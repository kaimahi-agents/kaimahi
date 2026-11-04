package agentfile

import (
	"fmt"
	"os"
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
	agent, err := ParseOrkaSource(path, agentBytes)
	if err != nil {
		return nil, err
	}
	source := TestsSource{State: "absent", Selection: TestsSelection{Path: target, Explicit: explicit}}
	if present {
		source.Suite, err = ParseTests(target, testsBytes)
		if err != nil {
			return nil, err
		}
		source.State, source.Revision = "external", source.Suite.Revision
	}
	source.Selection.Key, _ = SourceRevisions(path, agent.Revision, target, source.State, source.Revision)
	_, snapshot := SourceRevisions(path, agent.Revision, target, source.State, source.Revision)
	p := &OrkaSourcePair{AgentPath: path, Agent: agent, Tests: source, SnapshotRevision: snapshot, Unsupported: append([]OrkaUnsupported{}, agent.Unsupported...), agentObservation: agentObs, testsObservation: testsObs}
	if source.Suite == nil || len(source.Suite.Examples) == 0 {
		p.Unsupported = append(p.Unsupported, OrkaUnsupported{"Tests", "unrepresented"})
	} else {
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
	}
	// Recheck after parsing both sources, retaining the shared reader's absence,
	// file and parent replacement observations without claiming a write lock.
	if err := agentObs.check("File"); err != nil {
		return nil, err
	}
	if err := testsObs.check("Tests"); err != nil {
		return nil, err
	}
	return p, nil
}

// SameSourceVersion requires observations from checked loads, not copied hashes.
func (p *OrkaSourcePair) SameSourceVersion(other *OrkaSourcePair) bool {
	return p != nil && other != nil && p.agentObservation != nil && other.agentObservation != nil && p.SnapshotRevision == other.SnapshotRevision && p.agentObservation.same(other.agentObservation) && p.testsObservation.same(other.testsObservation)
}
