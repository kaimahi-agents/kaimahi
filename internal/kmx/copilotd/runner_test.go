package copilotd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"strings"
	"testing"
)

// Checker-only setup failures still protect printable source identity.
type checkerAccess struct {
	check    func(...string) error
	validate func(context.Context) error
}

func (a checkerAccess) Client() *ahp.Client                { return nil }
func (a checkerAccess) Info() (string, string)             { return "", "" }
func (a checkerAccess) CheckContent(s ...string) error     { return a.check(s...) }
func (a checkerAccess) Validate(ctx context.Context) error { return a.validate(ctx) }
func (a checkerAccess) Close(context.Context) error        { return nil }

func TestRunnerSetupFailureChecksFilename(t *testing.T) {
	access := checkerAccess{
		check: func(fields ...string) error {
			for _, field := range fields {
				if strings.Contains(field, "opaque-private-value") {
					return copilot.ErrCredentialContent
				}
			}
			return nil
		},
	}
	r := NewRunner(Options{Access: func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
		return access, copilot.ErrAuthRevoked
	},
		Snapshot: func(context.Context, func(...string) error) (agentpreview.SourceSnapshot, error) {
			t.Fatal("snapshot after setup failure")
			return agentpreview.SourceSnapshot{}, nil
		},
	})
	req := wireRequest()
	req.FileLabel = "opaque-private-value.agent.md"
	_, err := r.Run(t.Context(), req)
	if !errors.Is(err, copilot.ErrCredentialContent) {
		t.Fatalf("filename not withheld: %v", err)
	}
	req.FileLabel = "reader.agent.md"
	_, err = r.Run(t.Context(), req)
	if !errors.Is(err, copilot.ErrAuthRevoked) {
		t.Fatalf("setup failure lost: %v", err)
	}
}

func TestRunnerChecksDecodedExampleTitlesBeforeEffects(t *testing.T) {
	raw := "---\nschemaVersion: 1\n---\n```yaml agent-test\nid: titled\ntitle: \"\\u006fpaque-private-value\"\ninput: read\nassertions: [{id: check, type: contains, value: read}]\n```\n"
	suite, err := agentfile.ParseTests("reader.agent.test.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"selected", "unselected", "source"} {
		for _, setupErr := range []error{nil, copilot.ErrAuthRevoked} {
			t.Run(fmt.Sprintf("%s/setup-error=%t", field, setupErr != nil), func(t *testing.T) {
				req := wireRequest()
				req.SourceText = []string{raw}
				switch field {
				case "selected":
					req.Examples = suite.Examples
				case "unselected":
					req.Agent.Examples = suite.Examples
				case "source":
					req.SourceAgent = &agentfile.Agent{Examples: suite.Examples}
				}
				req.BeforeExecute = func() error {
					t.Error("credential reached execution")
					return errors.New("unexpected execution")
				}
				access := checkerAccess{check: func(fields ...string) error {
					for _, value := range fields {
						if strings.Contains(value, "opaque-private-value") {
							return copilot.ErrCredentialContent
						}
					}
					return nil
				}}
				r := NewRunner(Options{
					Access: func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
						return access, setupErr
					},
					Snapshot: func(context.Context, func(...string) error) (agentpreview.SourceSnapshot, error) {
						t.Fatal("credential reached snapshot")
						return agentpreview.SourceSnapshot{}, nil
					},
				})
				out, err := r.Run(t.Context(), req)
				if !errors.Is(err, copilot.ErrCredentialContent) || len(out.Examples) != 0 {
					t.Fatalf("title not withheld: %+v %v", out, err)
				}
			})
		}
	}
}

func TestRunnerSnapshotInvalidationCleansCopy(t *testing.T) {
	for _, want := range []error{copilot.ErrAuthRevoked, context.DeadlineExceeded, context.Canceled} {
		t.Run(want.Error(), func(t *testing.T) {
			closed := 0
			access := checkerAccess{check: func(...string) error { return nil }, validate: func(context.Context) error { return want }}
			r := NewRunner(Options{Access: func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
				return access, nil
			},
				Snapshot: func(context.Context, func(...string) error) (agentpreview.SourceSnapshot, error) {
					return agentpreview.SourceSnapshot{Close: func(context.Context) error { closed++; return nil }}, nil
				},
			})
			_, err := r.Run(t.Context(), wireRequest())
			if !errors.Is(err, want) || closed != 1 {
				t.Fatalf("cause=%v cleaned=%d", err, closed)
			}
		})
	}
}

func TestRunnerUnsupportedBeforeAuth(t *testing.T) {
	for _, req := range []agentpreview.Request{
		{Examples: []agentfile.Example{{Mocks: &agentfile.Mocks{}}}},
		{Examples: []agentfile.Example{{Assertions: []runtime.EvaluationAssertion{{Type: "llmJudge"}}}}},
	} {
		touched := false
		r := NewRunner(Options{Access: func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
			touched = true
			return nil, errors.New("auth")
		}})
		out, err := r.Run(t.Context(), req)
		if err == nil || touched || !out.NoJournal {
			t.Fatalf("%v %v %+v", err, touched, out)
		}
	}
}
func TestFindAgent(t *testing.T) {
	expected := &agentfile.Agent{Title: "Reader", Identity: agentfile.Identity{ID: "reader", Purpose: "Read files"}, Model: "authored", Tools: []string{"bash", "read"}}
	node := `{"type":"agent","name":"Reader","uri":"copilot-agent:/reader","description":"Read files","model":"authored","tools":["read","bash"]}`
	for _, tc := range []struct {
		name, tree string
		want       error
	}{
		{"unique reordered tools", "[" + node + "]", nil},
		{"nested unique", `[{"type":"directory","enabled":true,"children":[` + node + `]}]`, nil},
		{"missing", "[]", ErrAgentNotDiscovered},
		{"missing name", "[" + strings.Replace(node, `"Reader"`, `""`, 1) + "]", ErrAgentNotDiscovered},
		{"id is not name", "[" + strings.Replace(node, `"Reader"`, `"reader"`, 1) + "]", ErrAgentNotDiscovered},
		{"missing uri", "[" + strings.Replace(node, `"copilot-agent:/reader"`, `""`, 1) + "]", ErrAgentNotDiscovered},
		{"disabled", "[" + strings.Replace(node, `"type":"agent"`, `"type":"agent","enabled":false`, 1) + "]", ErrAgentNotDiscovered},
		{"disabled ancestor", `[{"type":"directory","enabled":false,"children":[` + node + `]}]`, ErrAgentNotDiscovered},
		{"duplicate uri", "[" + node + "," + node + "]", ErrAgentAmbiguous},
		{"nested ambiguity plus unique", `[{"type":"directory","children":[` + node + "," + node + "]}," + node + "]", ErrAgentAmbiguous},
		{"disabled collision", "[" + node + "," + strings.Replace(node, `"type":"agent"`, `"type":"agent","enabled":false`, 1) + "]", ErrAgentAmbiguous},
		{"disabled parent collision", "[" + node + `,{"type":"directory","enabled":false,"children":[` + node + "]}]", ErrAgentAmbiguous},
		{"metadata only one matches", "[" + node + "," + strings.Replace(node, "authored", "stale", 1) + "]", ErrAgentAmbiguous},
		{"missing metadata", `[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader"}]`, ErrAgentMetadataMismatch},
		{"description mismatch", "[" + strings.Replace(node, "Read files", "Different", 1) + "]", ErrAgentMetadataMismatch},
		{"stale model", "[" + strings.Replace(node, "authored", "stale", 1) + "]", ErrAgentMetadataMismatch},
		{"tools mismatch", "[" + strings.Replace(node, `["read","bash"]`, `["read","write"]`, 1) + "]", ErrAgentMetadataMismatch},
		{"tools subset", "[" + strings.Replace(node, `["read","bash"]`, `["read"]`, 1) + "]", ErrAgentMetadataMismatch},
		{"tools superset", "[" + strings.Replace(node, `["read","bash"]`, `["read","bash","write"]`, 1) + "]", ErrAgentMetadataMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tree []ahptypes.Customization
			if err := json.Unmarshal([]byte(tc.tree), &tree); err != nil {
				t.Fatal(err)
			}
			uri, err := findAgent(tree, expected)
			if !errors.Is(err, tc.want) || tc.want == nil && uri != "copilot-agent:/reader" || tc.want != nil && uri != "" {
				t.Fatalf("uri=%q err=%v want=%v", uri, err, tc.want)
			}
		})
	}
}

func TestFindAgentEmptyTools(t *testing.T) {
	// Public hosts omit both unspecified and explicitly empty tool lists; neither
	// is evidence that tools are disabled.
	for _, tools := range [][]string{nil, {}} {
		for _, raw := range []string{
			`[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader"}]`,
			`[{"type":"agent","name":"Reader","uri":"copilot-agent:/reader","tools":[]}]`,
		} {
			var tree []ahptypes.Customization
			if err := json.Unmarshal([]byte(raw), &tree); err != nil {
				t.Fatal(err)
			}
			if _, err := findAgent(tree, &agentfile.Agent{Title: "Reader", Tools: tools}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRunnerCancelledBeforeEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	touched := false
	r := NewRunner(Options{Access: func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
		touched = true
		return nil, errors.New("auth")
	}})
	_, err := r.Run(ctx, agentpreview.Request{})
	if !errors.Is(err, context.Canceled) || touched {
		t.Fatalf("%v %v", err, touched)
	}
}
