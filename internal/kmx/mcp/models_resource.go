package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AddModelResource exposes runtime choices without adding an execution tool.
// The catalog callback runs only on an explicit resource read, never at startup.
func AddModelResource(server *sdk.Server, list func(context.Context) ([]modelaccess.Route, error)) {
	server.AddResource(&sdk.Resource{URI: "kmx://models", Name: "models", MIMEType: "application/json", Description: "Read available Copilot model choices for this session; does not start sign-in or inference."}, func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if list == nil {
			return nil, errors.New("Model choices are unavailable.")
		}
		routes, err := list(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("Model choices are unavailable.")
		}
		body := struct {
			SchemaVersion int      `json:"schemaVersion"`
			Kind          string   `json:"kind"`
			DefaultModel  string   `json:"defaultModel"`
			Models        []string `json:"models"`
		}{1, "kmx.models", modelaccess.CopilotDefaultModel, copilot.CatalogIDs(routes)}
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, errors.New("Model choices are unavailable.")
		}
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(encoded)}}}, nil
	})
}
