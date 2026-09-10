package adapter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SDK v1.7.0 doesn't close the connection when rejecting a negotiated protocol
// version. Retain only its lifecycle handle, leaving all protocol work to SDK.
// Its Connection.Close is idempotent; cleanup must not replace a tool outcome.
type closingTransport struct {
	*mcp.StreamableClientTransport
	connection mcp.Connection
}

func (t *closingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.StreamableClientTransport.Connect(ctx)
	t.connection = conn
	return conn, err
}

func (t *closingTransport) Close() {
	if t.connection != nil {
		_ = t.connection.Close()
	}
}

type invocationTransport struct {
	base       http.RoundTripper
	headers    http.Header
	invocation context.Context
}

func (t *invocationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var cancel context.CancelFunc = func() {}
	// SDK deletion and cancellation notifications detach from the call context.
	// Allow one second for each cleanup operation, even if Client.Timeout has
	// already installed its own (longer) deadline on the HTTP request.
	if req.Method == http.MethodDelete || t.invocation.Err() != nil {
		ctx, cancel = context.WithTimeout(ctx, time.Second)
	}
	req = req.Clone(ctx)
	req.GetBody = nil // Never make POST bodies replayable, even after an EOF.
	for name, values := range t.headers {
		req.Header[name] = values
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, remaining: maxResponseBytes, cancel: cancel}
	return resp, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
	cancel    context.CancelFunc
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n != 0 {
			return 0, errors.New("upstream response too large")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}
