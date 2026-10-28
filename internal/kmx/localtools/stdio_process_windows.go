//go:build !unix

package localtools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Native tool execution is not part of the qualified Windows CLI boundary.
type stdioProcess struct{}

func startStdioProcess(context.Context, StdioConfig, func(...string) error) (*stdioProcess, error) {
	return nil, ErrStdio
}
func (*stdioProcess) transport() mcp.Transport { return nil }
func (*stdioProcess) healthy() error           { return ErrStdio }
func (*stdioProcess) fail()                    {}
func (*stdioProcess) Close() error             { return ErrStdioCleanup }
