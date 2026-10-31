package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSquadAggregateReadValidatesEveryMemberAndRefreshesInventory(t *testing.T) {
	root, program := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, program)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	plan := SquadPlan{Lead: squadLead()}
	for i := 0; i < 7; i++ {
		member := enrichMember()
		member.ID = fmt.Sprintf("reviewer-%02d", i)
		plan.Specialists = append(plan.Specialists, member)
	}
	if _, err := c.Create(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	a := &app.App{}
	client := connectFixture(t, WithSquadOperations(Operations(files, a, nil), c, a, nil), func(s *sdk.Server) { AddSquadResource(s, c) })
	read := func() []NativeMemberResult {
		t.Helper()
		result, err := client.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://squad"})
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			Members []NativeMemberResult `json:"members"`
		}
		if json.Unmarshal([]byte(result.Contents[0].Text), &view) != nil || len(view.Members) != 8 {
			t.Fatal("incomplete roster")
		}
		return view.Members
	}
	if err := os.WriteFile(filepath.Join(root, ".squad/large-retained-file"), bytes.Repeat([]byte("x"), 4<<20), 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	rows := read()
	t.Logf("eight-member aggregate with 4 MiB retained content: %v", time.Since(started))
	for i, row := range rows {
		if row.State != "ready" || row.File == nil || row.File.Revision == "" || i > 0 && rows[i-1].MemberID >= row.MemberID {
			t.Fatalf("invalid or unordered row: %+v", row)
		}
	}
	bad := plan.Specialists[0]
	charter := filepath.Join(root, squadPath(bad))
	raw, err := os.ReadFile(charter)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(charter, bytes.Replace(raw, []byte(bad.DisplayRole), []byte("Wrong role"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	for _, row := range read() {
		want := "ready"
		if row.MemberID == bad.ID {
			want = "not_setup"
		}
		if row.State != want {
			t.Fatalf("member validation was not independent: %+v", row)
		}
	}
	marker := filepath.Join(root, ".squad/casting/registry.lock")
	if err := os.WriteFile(marker, []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, row := range read() {
		if row.State != "not_setup" {
			t.Fatal("a later read reused inventory across pending recovery")
		}
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".squad/casting/history.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://squad"}); err == nil {
		t.Fatal("a later read reused registry proof after corruption")
	}
}
