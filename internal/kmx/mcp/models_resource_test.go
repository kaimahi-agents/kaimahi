package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestModelResourceIsLazyAndReadOnly(t *testing.T) {
	f, _, _ := canonicalFiles(t)
	calls := 0
	cs := connectFixture(t, Operations(f, &app.App{}, nil), func(server *sdk.Server) {
		AddModelResource(server, func(ctx context.Context) ([]modelaccess.Route, error) {
			calls++
			return []modelaccess.Route{
				{Provider: "copilot", Model: "choice-b", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"},
				{Provider: "copilot", Model: "choice-a", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"},
			}, nil
		})
	})
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 5 || calls != 0 {
		t.Fatal("model discovery added a tool or accessed auth at startup")
	}
	result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://models"})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		SchemaVersion      int `json:"schemaVersion"`
		Kind, DefaultModel string
		Models             []string
	}
	if len(result.Contents) != 1 || json.Unmarshal([]byte(result.Contents[0].Text), &body) != nil || body.SchemaVersion != 1 || body.Kind != "kmx.models" || body.DefaultModel != "gpt-5.6-terra" || strings.Join(body.Models, ",") != "choice-a,choice-b" || calls != 1 {
		t.Fatalf("invalid model choices: %+v", result)
	}
}

func TestModelResourceDoesNotExposeProviderError(t *testing.T) {
	f, _, _ := canonicalFiles(t)
	cs := connectFixture(t, Operations(f, &app.App{}, nil), func(server *sdk.Server) {
		AddModelResource(server, func(context.Context) ([]modelaccess.Route, error) {
			return nil, errors.New("private-model-catalog-secret")
		})
	})
	_, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://models"})
	if err == nil || strings.Contains(err.Error(), "private-model-catalog-secret") {
		t.Fatalf("unsafe catalog error: %v", err)
	}
}
