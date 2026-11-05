package mcp

import (
	"context"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type squadPreviewKey struct {
	session     *sdk.ServerSession
	member, run string
}

// Controls are scoped to the calling MCP connection and active invocation.
type squadPreviewControls struct {
	mu   sync.Mutex
	runs map[squadPreviewKey]context.CancelFunc
}

func (p *squadPreviewControls) begin(ctx context.Context, session *sdk.ServerSession, member, run string) (context.Context, func(), error) {
	if run == "" {
		return ctx, func() {}, nil
	}
	if session == nil {
		return nil, nil, refusal("invalid_options")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := squadPreviewKey{session, member, run}
	if p.runs[key] != nil || len(p.runs) >= 64 {
		return nil, nil, refusal("busy")
	}
	if p.runs == nil {
		p.runs = make(map[squadPreviewKey]context.CancelFunc)
	}
	child, cancel := context.WithCancel(ctx)
	p.runs[key] = cancel
	return child, func() {
		cancel()
		p.mu.Lock()
		delete(p.runs, key)
		p.mu.Unlock()
	}, nil
}

func (p *squadPreviewControls) cancel(session *sdk.ServerSession, member, run string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cancel := p.runs[squadPreviewKey{session, member, run}]; cancel != nil {
		cancel()
		return true
	}
	return false
}
