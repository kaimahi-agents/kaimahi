package agentsessions

import (
	"context"

	v1 "github.com/aramase/agentsessions/api/genpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Probe checks only whether the Sessions API is readable, discarding returned
// session metadata. It never executes a turn, discovers a harness or qualifies
// the host version. The caller supplies the deadline and explicit destination.
func (c *Client) Probe(ctx context.Context) error {
	if _, bounded := ctx.Deadline(); !bounded {
		return status.Error(codes.InvalidArgument, "agentsessions: probe deadline required")
	}
	_, err := c.sdk.Sessions().ListSessions(ctx, &v1.ListSessionsRequest{PageSize: 1}, grpc.MaxCallRecvMsgSize(1<<20))
	if err != nil {
		return safeRPCError(err, "agentsessions: Sessions read failed")
	}
	return nil
}
