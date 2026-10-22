package agentsessions

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDialInProcessAndPreviewIdentityRefuseBeforeRPC(t *testing.T) {
	if _, err := DialInProcess(nil); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil dialer = %v", err)
	}
	var dials atomic.Int32
	client, err := DialInProcess(func(context.Context, string) (net.Conn, error) {
		dials.Add(1)
		return nil, net.ErrClosed
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	valid := "sha256:" + strings.Repeat("a", 64)
	for name, request := range map[string]PreviewCaseRequest{
		"revision":  {Revision: "sha256:abc", Agent: "greeter", Model: "fake"},
		"agent":     {Revision: valid, Agent: "bad agent", Model: "fake"},
		"example":   {Revision: valid, Agent: "greeter", Example: "bad example", Model: "fake"},
		"no model":  {Revision: valid, Agent: "greeter"},
		"no prefix": {Revision: strings.Repeat("a", 71), Agent: "greeter", Model: "fake"},
	} {
		if _, err := client.RunPreviewCase(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := client.InspectPreviewSession(context.Background(), "sess-1"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unbounded inspection = %v", err)
	}
	if dials.Load() != 0 {
		t.Fatalf("refusals dialed %d times", dials.Load())
	}
}
