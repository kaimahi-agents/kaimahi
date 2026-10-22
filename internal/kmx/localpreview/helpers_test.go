package localpreview

import "github.com/agent-primitives/sessions/api"

func modelRequest(turn Turn) api.ModelRequest {
	return api.ModelRequest{Model: turn.Model, Messages: []api.Message{*api.TextMessage("system", turn.Instructions), *api.TextMessage("user", turn.Input)}}
}
