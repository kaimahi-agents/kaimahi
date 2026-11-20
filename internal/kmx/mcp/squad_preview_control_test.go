package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSquadPreviewCancellationIsConnectionAndInvocationScoped(t *testing.T) {
	controls := &squadPreviewControls{}
	one, two := &sdk.ServerSession{}, &sdk.ServerSession{}
	ctx, done, err := controls.begin(t.Context(), one, "lead", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if controls.cancel(two, "lead", "run-1") || controls.cancel(one, "other", "run-1") || controls.cancel(one, "lead", "run-2") || ctx.Err() != nil {
		t.Fatal("foreign cancellation reached the run")
	}
	if _, _, err := controls.begin(t.Context(), one, "lead", "run-1"); err == nil {
		t.Fatal("duplicate active run accepted")
	}
	if !controls.cancel(one, "lead", "run-1") || ctx.Err() != context.Canceled {
		t.Fatal("owned cancellation did not stop its context")
	}
}

type waitingSquadPreview struct{ started chan struct{} }

func (r *waitingSquadPreview) Run(ctx context.Context, _ agentpreview.Request) (agentpreview.Outcome, error) {
	close(r.started)
	<-ctx.Done()
	return agentpreview.Outcome{State: agentpreview.Cancelled}, ctx.Err()
}

func TestSquadMCPStopsActivePreviewWithoutSessionAbort(t *testing.T) {
	root, program := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, program)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m := squadLead()
	m.Model = "fake"
	if _, err := c.Create(t.Context(), SquadPlan{Lead: m}); err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	runner := &waitingSquadPreview{started: make(chan struct{})}
	a := &app.App{FileRunner: runner}
	client, notifications := connectProgressFixture(t, WithSquadOperations(Operations(files, a, nil), c, a, nil))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan *sdk.CallToolResult, 1)
	request := &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": m.ID, "runId": "run-1"}}
	request.SetProgressToken("progress-1")
	go func() { result, _ := client.CallTool(ctx, request); done <- result }()
	select {
	case <-runner.started:
	case <-ctx.Done():
		t.Fatal("preview did not start")
	}
	select {
	case notification := <-notifications:
		var data map[string]any
		if json.Unmarshal([]byte(notification.Message), &data) != nil || data["kind"] != "kmx.squad-preview" || data["state"] != "started" || data["runId"] != "run-1" {
			t.Fatal("missing correlated start progress")
		}
	case <-ctx.Done():
		t.Fatal("no start progress")
	}
	result, err := client.CallTool(ctx, &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-preview-cancel", "memberId": m.ID, "runId": "run-1"}})
	if err != nil || result.IsError {
		t.Fatalf("cancel: %v %+v", err, result)
	}
	select {
	case final := <-done:
		if final == nil {
			t.Fatal("missing final result")
		}
		bytes, _ := json.Marshal(final.StructuredContent)
		var member NativeMemberResult
		if json.Unmarshal(bytes, &member) != nil || member.RunID != "run-1" || member.Preview == nil || member.Preview.State != "cancelled" || member.Preview.FullPass {
			t.Fatalf("incorrect final outcome: %s", bytes)
		}
	case <-ctx.Done():
		t.Fatal("preview did not stop")
	}
}
