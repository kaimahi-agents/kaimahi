package agentsessions

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/aramase/agentsessions/api/genpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type probeServer struct {
	v1.UnimplementedSessionsServer
	code             codes.Code
	block, oversized bool
}

func (s probeServer) ListSessions(ctx context.Context, req *v1.ListSessionsRequest) (*v1.ListSessionsResponse, error) {
	if s.block {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if s.oversized {
		return &v1.ListSessionsResponse{NextPageToken: strings.Repeat("x", 1<<20)}, nil
	}
	if req.PageSize != 1 || req.PageToken != "" || req.Project != "" {
		return nil, status.Error(codes.InvalidArgument, "wrong probe request")
	}
	if s.code != codes.OK {
		return nil, status.Error(s.code, "PRIVATE-REMOTE-PAYLOAD")
	}
	return &v1.ListSessionsResponse{Sessions: []*v1.Session{{Harness: "PRIVATE-REMOTE-PAYLOAD"}}}, nil
}

func TestProbeOnlyReadsSessionsAndPreservesSanitizedErrorCodes(t *testing.T) {
	for _, code := range []codes.Code{codes.OK, codes.PermissionDenied, codes.Unauthenticated, codes.Unavailable, codes.Unimplemented, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				calls.Add(1)
				if info.FullMethod != "/agentsessions.v1.Sessions/ListSessions" {
					return nil, status.Error(codes.InvalidArgument, "mutation attempted")
				}
				return handler(ctx, req)
			}))
			v1.RegisterSessionsServer(server, probeServer{code: code})
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			client, err := Dial(Options{Address: listener.Addr().String()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = client.Probe(ctx)
			if status.Code(err) != code || calls.Load() != 1 {
				t.Fatalf("code=%s err=%v calls=%d", code, err, calls.Load())
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE-REMOTE-PAYLOAD") {
				t.Fatal("remote error leaked")
			}
		})
	}
}

func TestProbeBoundsResponseAndBlockedRPC(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server probeServer
		code   codes.Code
	}{
		{"blocked", probeServer{block: true}, codes.DeadlineExceeded},
		{"oversized", probeServer{oversized: true}, codes.ResourceExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			v1.RegisterSessionsServer(server, tc.server)
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			client, err := Dial(Options{Address: listener.Addr().String()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := client.Probe(ctx); status.Code(err) != tc.code {
				t.Fatalf("code=%s err=%v", tc.code, err)
			}
		})
	}
}

func TestProbeRequiresDeadline(t *testing.T) {
	client := &Client{}
	if err := client.Probe(context.Background()); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unbounded probe: %v", err)
	}
}
