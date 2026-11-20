package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSquadBriefCredentialsRefuseBeforeNativeCreation(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			root, program := squadSetupRoot(t)
			creator, err := OpenSquadSetup(root, program)
			if err != nil {
				t.Fatal(err)
			}
			defer creator.Close()
			brief := "Shared context: " + shape.Example
			result, err := creator.Create(t.Context(), SquadPlan{Lead: squadLead(), Brief: &brief})
			var detail *Error
			if !errors.As(err, &detail) || detail.Code != "invalid_squad_brief" || result.EffectsUnknown || len(result.ChangedPaths) != 0 {
				t.Fatal("credential-bearing brief did not refuse before effects")
			}
			if strings.Contains(err.Error(), shape.Example) {
				t.Fatal("credential appeared in the refusal")
			}
			for _, name := range []string{".init-called", ".squad"} {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatal("credential-bearing brief caused native creation")
				}
			}
		})
	}
}

func TestSquadBriefCredentialsRefuseBeforeEditAndPreview(t *testing.T) {
	root, _, creator := createdSquadMember(t)
	lead := squadLead()
	const safeBrief = "Preserve reviewed team context."
	file, err := creator.EditBrief(t.Context(), "none", safeBrief)
	if err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	runner := &previewFixture{}
	application := &app.App{FileRunner: runner}
	client := connectFixture(t, WithSquadOperations(Operations(files, application, nil), creator, application, nil))
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			before, err := creator.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			_, err = creator.EditBrief(t.Context(), file.Revision, "Shared context: "+shape.Example)
			var detail *Error
			if !errors.As(err, &detail) || detail.Code != "invalid_squad_brief" {
				t.Fatal("credential-bearing brief edit was not refused")
			}
			after, err := creator.snapshot()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("refused brief edit changed native files")
			}
			if err := os.WriteFile(filepath.Join(root, squadBriefPath), []byte(shape.Example), 0644); err != nil {
				t.Fatal(err)
			}
			defer os.WriteFile(filepath.Join(root, squadBriefPath), []byte(safeBrief), 0644)
			for _, operation := range []string{"run", "test"} {
				result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": lead.ID}})
				if err != nil || !result.IsError || runner.calls != 0 {
					t.Fatal("persisted credential-bearing brief reached preview")
				}
				encoded, _ := json.Marshal(result.StructuredContent)
				if strings.Contains(string(encoded), shape.Example) {
					t.Fatal("credential appeared in preview refusal")
				}
			}
		})
	}
}
