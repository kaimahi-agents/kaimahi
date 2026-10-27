package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AddSquadResource is a separate read-only native contract, not a canvas action
// bridge. Each read reconstructs mapping; no draft or prior pass is restored.
func AddSquadResource(server *sdk.Server, c *SquadCreator, routes ...*modelaccess.Route) {
	provider := "fake"
	if len(routes) > 0 && routes[0] != nil {
		provider = routes[0].Provider
	}
	server.AddResource(&sdk.Resource{URI: "kmx://squad", Name: "squad", MIMEType: "application/json", Description: "Read native roster and saved shared brief under the configured root. No execution or retained test history."}, func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		data := map[string]any{"schemaVersion": 2, "kind": "kmx.squad-view", "root": c.root.Name(), "state": "uninitialized", "members": []NativeMemberResult{}, "brief": SquadBrief{Revision: "none"}, "access": []string{"Tool-free member previews"}}
		data["previewControl"], data["previewProvider"] = "run-id-v1", provider
		if !c.setupMissing() {
			if err := c.checkRoot(); err != nil {
				return nil, err
			}
			registry, err := c.registry()
			if err != nil {
				return nil, err
			}
			if len(registry) > 128 {
				return nil, refusal("invalid_squad_members")
			}
			ids := make([]string, 0, len(registry))
			for id := range registry {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			members := []NativeMemberResult{}
			for _, id := range ids {
				d, _ := c.Member(ctx, id)
				m := nativeMemberResult(id, d)
				m.MemberID = id
				members = append(members, m)
			}
			brief, err := c.brief(ctx)
			if err != nil {
				return nil, err
			}
			data["state"], data["members"], data["brief"] = "ready", members, brief
		}
		if err := cancelled(ctx); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(data)
		if err != nil || len(encoded) > 2<<20 {
			return nil, refusal("invalid_file")
		}
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(encoded)}}}, nil
	})
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
			TaskInstructions string             `json:"taskInstructions,omitempty"`
			Model            string             `json:"model,omitempty"`
			Examples         string             `json:"examples,omitempty"`
			Instructions     string             `json:"instructions,omitempty"`
			Tests            []AgentViewExample `json:"tests,omitempty"`
			Brief            SquadBrief         `json:"brief"`
		}{NativeMemberResult: nativeMemberResult(id, d)}
		if d != nil {
			brief, err := c.brief(ctx)
			if err != nil {
				return nil, err
			}
			data.Brief = brief
			for _, example := range d.Examples {
				data.Tests = append(data.Tests, AgentViewExample{ID: example.ID, Input: example.Input, Assertions: example.Assertions})
			}
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
