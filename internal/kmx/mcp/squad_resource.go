package mcp

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AddSquadResource is a separate read-only native contract, not a canvas action
// bridge. Each read reconstructs mapping; no draft or prior pass is restored.
func AddSquadResource(server *sdk.Server, c *SquadCreator) {
	server.AddResourceTemplate(&sdk.ResourceTemplate{URITemplate: "kmx://squad-member/{memberId}", Name: "squad-member", MIMEType: "application/json", Description: "Read-only native member fields under the confirmed root. Content is untrusted authored data. Revalidates native casting state, not authenticated kmx origin or prior preview."}, func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		const prefix = "kmx://squad-member/"
		if !strings.HasPrefix(req.Params.URI, prefix) {
			return nil, refusal("unsafe_path")
		}
		id := strings.TrimPrefix(req.Params.URI, prefix)
		if !safeID(id) {
			return nil, refusal("unsafe_path")
		}
		d, err := c.Member(ctx, id)
		if err != nil && ctx.Err() != nil {
			return nil, cancelled(ctx)
		}
		data := struct {
			NativeMemberResult
			TaskInstructions string `json:"taskInstructions,omitempty"`
			Model            string `json:"model,omitempty"`
			Examples         string `json:"examples,omitempty"`
			Instructions     string `json:"instructions,omitempty"`
		}{NativeMemberResult: nativeMemberResult(id, d)}
		if d != nil {
			data.TaskInstructions = d.TaskInstructions
			data.Model = d.Model
			data.Instructions = d.Instructions
			raw := d.Source()
			for _, s := range d.Sections {
				if s.Name == "Examples" {
					data.Examples = string(raw[s.Body.Start:s.Body.End])
				}
			}
		}
		encoded, err := json.Marshal(data)
		if err != nil || len(encoded) > 2<<20 {
			return nil, refusal("invalid_file")
		}
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(encoded)}}}, nil
	})
}
