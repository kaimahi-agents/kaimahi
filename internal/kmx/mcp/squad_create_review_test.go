package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSquadMCPRejectsExistingRosterWithoutBrief(t *testing.T) {
	root, _, creator := createdSquadMember(t)
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	application := &app.App{}
	client := connectFixture(t, WithSquadOperations(Operations(files, application, nil), creator, application, nil))
	before, err := creator.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	lead := squadLead()
	lead.ID = "other-lead"
	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-members", "lead": squadToolMember(lead), "specialists": []any{}, "brief": "A different team."}})
	if err != nil || !result.IsError {
		t.Fatal("existing roster accepted another team")
	}
	after, err := creator.snapshot()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("refused team creation changed native files")
	}
}

func TestSquadCreateRechecksRosterInsideGate(t *testing.T) {
	root, program := squadSetupRoot(t)
	creator, err := OpenSquadSetup(root, program)
	if err != nil {
		t.Fatal(err)
	}
	defer creator.Close()
	other, err := OpenSquadSetup(root, program)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	waiting := &squadGateWaitContext{Context: ctx, entered: make(chan struct{})}
	creator.gate <- struct{}{}
	held := true
	defer func() {
		if held {
			<-creator.gate
		}
	}()
	lead, brief := squadLead(), "Queued proposal."
	lead.ID = "queued-lead"
	type outcome struct {
		result SquadResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := creator.Create(waiting, SquadPlan{Lead: lead, Brief: &brief, RequireEmpty: true})
		done <- outcome{result, err}
	}()
	select {
	case <-waiting.entered:
	case <-ctx.Done():
		t.Fatal("Create did not reach the held gate")
	}
	if _, err := other.Create(ctx, SquadPlan{Lead: squadLead()}); err != nil {
		t.Fatal(err)
	}
	before, err := creator.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	<-creator.gate
	held = false
	select {
	case got := <-done:
		var detail *Error
		if !errors.As(got.err, &detail) || detail.Code != "already_exists" || got.result.EffectsUnknown || len(got.result.ChangedPaths) != 0 {
			t.Fatal("queued Create did not refuse the new roster before effects")
		}
	case <-ctx.Done():
		t.Fatal("queued Create did not finish")
	}
	after, err := creator.snapshot()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("queued Create changed native files")
	}
	if _, err := os.Stat(filepath.Join(root, ".squad/agents/queued-lead")); !os.IsNotExist(err) {
		t.Fatal("queued member was cast")
	}
}

func TestSquadNativeRolePreflight(t *testing.T) {
	for _, tc := range []struct {
		role, display string
		allowed       bool
	}{
		{"developer", "Security Reviewer", false},
		{"security", "security", false},
		{"security", "Security Engineer", true},
		{"SECURITY", "Security Engineer", true},
		{"SECURİTY", "SECURİTY", true},
		{"SECURİTY", "Security Engineer", false},
		{"marKeting-strategist", "Marketing Strategist", true},
		{"Product   Manager", "Product Manager", true},
		{"Product   Manager", "Product   Manager", false},
		{"Security Reviewer", "Security Reviewer", true},
	} {
		member := enrichMember()
		member.Role, member.DisplayRole = tc.role, tc.display
		if err := validateSquadMember(member); (err == nil) != tc.allowed {
			t.Fatalf("native role validation allowed=%v, want %v", err == nil, tc.allowed)
		}
	}
}

func TestSquadMCPRejectsMismatchedUnicodeRoleBeforeSetup(t *testing.T) {
	root, program := squadSetupRoot(t)
	creator, err := OpenSquadSetup(root, program)
	if err != nil {
		t.Fatal(err)
	}
	defer creator.Close()
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	application := &app.App{}
	client := connectFixture(t, WithSquadOperations(Operations(files, application, nil), creator, application, nil))
	member := enrichMember()
	member.Role, member.DisplayRole = "SECURİTY", "Security Engineer"
	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-members", "lead": squadToolMember(squadLead()), "specialists": []any{squadToolMember(member)}}})
	if err != nil || !result.IsError {
		t.Fatal("mismatched native Unicode role was not refused", err)
	}
	for _, name := range []string{".init-called", ".squad"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("role mismatch reached native setup")
		}
	}
}

func TestSquadEmptyRosterSeparatesSupportAndPartialMemberRows(t *testing.T) {
	const header = "# Squad Team\n\n## Members\n\n| Name | Role | Charter | Status |\n|------|------|---------|--------|\n"
	for _, tc := range []struct {
		name, roster string
		empty        bool
	}{
		{"empty", header, true},
		{"native-support", header + "\n## Built-in Support Agents\n\n| Scribe | Decision Merger | `.squad/agents/scribe/charter.md` | Silent |\n", true},
		{"member", header + "| leader | Lead / Architect | `.squad/agents/leader/charter.md` | Active |\n", false},
		{"partial-member", header + "| | Lead | `.squad/agents/leader/charter.md` | Active |\n", false},
		{"duplicate-members", header + "\n## Members\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, program := squadTestRoot(t)
			if err := os.WriteFile(filepath.Join(root, ".squad/team.md"), []byte(tc.roster), 0644); err != nil {
				t.Fatal(err)
			}
			creator, err := OpenSquad(root, program)
			if err != nil {
				t.Fatal(err)
			}
			defer creator.Close()
			seen, err := creator.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if err := creator.requireEmptyTeam(seen); (err == nil) != tc.empty {
				t.Fatal("native roster emptiness was misclassified")
			}
		})
	}
}
