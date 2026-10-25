package localtools

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// Definition returns fresh fixed schema data; remote descriptions are never trusted.
func Definition() api.ToolDefinition {
	return api.ToolDefinition{Name: Operation, Description: "List up to 20 merged pull requests in the host-selected repository.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
}

type PullRequest struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Repository string `json:"repository"`
	Merged     bool   `json:"merged"`
}

// Host holds ambient authority outside all serialized model/harness configuration.
// The trusted read callback is repeat-safe; no transport or credential lookup is implied.
type Host struct {
	binding     Binding
	credential  string
	read        func(context.Context) ([]PullRequest, error)
	definitions []api.ToolDefinition
	execute     controller.ToolFunc
	check       func(...string) error
}

var errUnsafeContent = errors.New("local tools: unsafe content refused")
var errInvalidCall = errors.New("local tools: invalid read call")
var errReadFailed = errors.New("local tools: read unavailable")
var errInvalidResult = errors.New("local tools: invalid read result")

func NewHost(binding Binding, credential string, read func(context.Context) ([]PullRequest, error)) (*Host, error) {
	if !binding.valid() || credential == "" || read == nil {
		return nil, errInvalidBinding
	}
	host := &Host{binding: binding, credential: credential, read: read}
	if host.Check(binding.repository, binding.endpoint, binding.credentialEnv) != nil {
		return nil, errUnsafeContent
	}
	return host, nil
}

// Check must run before any supplied text reaches a journal or model.
func (h *Host) Check(values ...string) error {
	if h == nil || h.credential == "" && h.check == nil {
		return errUnsafeContent
	}
	for _, value := range values {
		if h.credential != "" && strings.Contains(value, h.credential) || secretshapes.Match(value) != nil {
			return errUnsafeContent
		}
	}
	if h.check != nil {
		return h.check(values...)
	}
	return nil
}

func (h *Host) Execute(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
	if h != nil && h.execute != nil {
		return h.executeTrusted(ctx, scope, call)
	}
	if h == nil || h.read == nil || !h.binding.valid() || ctx.Err() != nil || strings.TrimSpace(scope.SessionUID) == "" || strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.IdempotencyKey) == "" || call.Tool != Operation || call.Mediation != api.MediationControllerMediated || len(call.Args) != 0 {
		return api.ToolResult{}, errInvalidCall
	}
	if err := h.Check(scope.SessionUID, call.ID, call.IdempotencyKey, call.Tool); err != nil {
		return api.ToolResult{}, err
	}
	rows, err := h.read(ctx)
	if err != nil || ctx.Err() != nil {
		return api.ToolResult{}, errReadFailed
	}
	if len(rows) > 20 {
		return api.ToolResult{}, errInvalidResult
	}
	projected := make([]any, 0, len(rows))
	for _, pr := range rows {
		if pr.Number <= 0 || len(pr.Title) > 512 || !utf8.ValidString(pr.Title) || pr.Repository != h.binding.repository || !pr.Merged {
			return api.ToolResult{}, errInvalidResult
		}
		if err := h.Check(pr.Title, pr.Repository, strconv.Itoa(pr.Number)); err != nil {
			return api.ToolResult{}, err
		}
		projected = append(projected, map[string]any{"number": pr.Number, "title": pr.Title})
	}
	output := map[string]any{"repository": h.binding.repository, "merged": true, "pullRequests": projected}
	// Check fixed projected keys/attribution too, even for short credentials.
	if err := h.Check("repository", "merged", "true", "pullRequests", "number", "title", h.binding.repository); err != nil {
		return api.ToolResult{}, err
	}
	return api.ToolResult{ID: call.ID, Output: output}, nil
}
