package residenttools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const (
	// BoundedProcess is fixed local execution with explicit environment,
	// process-group cleanup and bounded output. It is not a sandbox.
	BoundedProcess = "bounded-process"
	brokerLimit    = 64 << 10
	brokerTimeout  = 30 * time.Second
)

// Plan is immutable broker startup authority. It is transferred over a
// protected descriptor and is never retained in local-agent lifecycle JSON.
type Plan struct {
	Backend  string                 `json:"backend"`
	Commands localtools.CommandPlan `json:"commands"`
}

func (p Plan) Revision() string       { return p.Commands.Revision }
func (p Plan) ConfigRevision() string { return p.Commands.ConfigRevision }

type request struct {
	Op         string       `json:"op"`
	ID         string       `json:"id,omitempty"`
	SessionUID string       `json:"sessionUid,omitempty"`
	Call       api.ToolCall `json:"call,omitempty"`
}

type response struct {
	OK          bool                 `json:"ok"`
	ID          string               `json:"id,omitempty"`
	Error       string               `json:"error,omitempty"`
	Definitions []api.ToolDefinition `json:"definitions,omitempty"`
	Result      *api.ToolResult      `json:"result,omitempty"`
}

func writeFrame(w io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw)+1 > brokerLimit {
		return errors.New("resident tool broker frame is unavailable")
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

func readFrame(r *bufio.Reader, value any) error {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return io.EOF
	}
	if err != nil || len(line) > brokerLimit {
		return errors.New("resident tool broker frame is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return errors.New("resident tool broker frame is unavailable")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("resident tool broker frame is unavailable")
	}
	return nil
}

// Client is the model worker's typed handle to one broker process.
type Client struct {
	conn net.Conn
	read *bufio.Reader
	mu   sync.Mutex
}

// Attach consumes an inherited broker descriptor and obtains the broker-owned
// fixed definitions before any model request is allowed.
func Attach(file *os.File) (*Client, *localtools.Host, error) {
	if file == nil {
		return nil, nil, errors.New("resident tool broker is unavailable")
	}
	conn, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return nil, nil, errors.New("resident tool broker is unavailable")
	}
	client := &Client{conn: conn, read: bufio.NewReaderSize(conn, brokerLimit+1)}
	client.mu.Lock()
	err = writeFrame(conn, request{Op: "describe"})
	var described response
	if err == nil {
		err = readFrame(client.read, &described)
	}
	client.mu.Unlock()
	if err != nil || !described.OK || len(described.Definitions) == 0 {
		_ = conn.Close()
		return nil, nil, errors.New("resident tool broker is unavailable")
	}
	host, err := localtools.NewTrustedHost(described.Definitions, client.execute, checkContent)
	if err != nil {
		_ = conn.Close()
		return nil, nil, errors.New("resident tool broker is unavailable")
	}
	return client, host, nil
}

func checkContent(values ...string) error {
	for _, value := range values {
		if secretshapes.Match(value) != nil {
			return errors.New("resident tool broker refused unsafe content")
		}
	}
	return nil
}

func (c *Client) execute(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return api.ToolResult{}, err
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return api.ToolResult{}, err
	}
	id := hex.EncodeToString(raw)
	if err := writeFrame(c.conn, request{Op: "execute", ID: id, SessionUID: scope.SessionUID, Call: call}); err != nil {
		return api.ToolResult{}, errors.New("resident tool broker request failed")
	}
	type outcome struct {
		response response
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		var value response
		done <- outcome{value, readFrame(c.read, &value)}
	}()
	select {
	case out := <-done:
		if out.err != nil || !out.response.OK || out.response.ID != id || out.response.Result == nil {
			return api.ToolResult{}, errors.New("resident tool broker execution failed")
		}
		return *out.response.Result, nil
	case <-ctx.Done():
		_ = writeFrame(c.conn, request{Op: "cancel", ID: id})
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			return api.ToolResult{}, ctx.Err()
		case <-timer.C:
			_ = c.conn.Close()
			return api.ToolResult{}, ctx.Err()
		}
	}
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
