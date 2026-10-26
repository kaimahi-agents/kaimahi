package agentsessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/wire"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// RunToolPreviewCase is the distinct bounded local-tool contract. It never
// relaxes the suite/chat verifier or manufactures native evaluation evidence.
func (c *Client) RunToolPreviewCase(ctx context.Context, r PreviewCaseRequest) (CaseResult, error) {
	var out CaseResult
	deadline, ok := ctx.Deadline()
	if !ok || !safeIdentity(r.Model) || !safeIdentity(r.Agent) || len(r.Revision) != 71 || r.Revision[:7] != "sha256:" || !verifyDigest(r.Revision[7:]) || r.Example != "" && !safeIdentity(r.Example) {
		return out, status.Error(codes.InvalidArgument, "agentsessions: valid tool preview request and deadline required")
	}
	settings, err := localtools.CopySettings(r.ToolSettings, r.ToolDefinitions)
	if err != nil {
		return out, status.Error(codes.InvalidArgument, "agentsessions: invalid tool settings")
	}
	defs := r.ToolDefinitions
	if defs == nil {
		defs = []api.ToolDefinition{localtools.Definition()}
	}
	if r.ToolSources.Validate(defs) != nil {
		return out, status.Error(codes.InvalidArgument, "agentsessions: invalid tool sources")
	}
	config, err := json.Marshal(localtools.Config{Instructions: r.Instructions, Definitions: r.ToolDefinitions, ToolSettings: settings, Sources: r.ToolSources})
	if err != nil || len(config) > 1<<20 || len(r.Input) > 16<<10 {
		return out, status.Error(codes.InvalidArgument, "agentsessions: tool preview exceeds bounds")
	}
	labels := map[string]string{PreviewLabel: "true", "kmx.agent": r.Agent, "kmx.agent-revision": r.Revision, "kmx.example": r.Example}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s, err := c.sdk.CreateSession(ctx, &v1.Session{Harness: localtools.HarnessName, Labels: labels})
	if err != nil {
		return out, safeRPCError(err, "agentsessions: tool preview creation failed")
	}
	if !freshToolSession(s) || r.UsedSessionUIDs[s.GetMetadata().GetUid()] {
		return out, status.Error(codes.FailedPrecondition, "agentsessions: invalid tool preview identity")
	}
	out.SessionUID = s.GetMetadata().GetUid()
	out.Harness = localtools.HarnessName
	zero := int64(0)
	stream, err := c.sdk.Sessions().Exec(ctx, &v1.ExecRequest{Session: out.SessionUID, Harness: localtools.HarnessName, Inputs: []*v1.Message{wire.MessageToProto(api.TextMessage("user", r.Input))}, Config: config, ExpectedLastSeq: &zero, DeadlineUnix: deadline.Unix()}, grpc.MaxCallRecvMsgSize(maxSuiteBytes))
	if err != nil {
		return out, safeRPCError(err, "agentsessions: tool preview execution failed")
	}
	var records []eventlog.Record
	frames, total := 0, 0
	for {
		u, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return out, safeRPCError(recvErr, "agentsessions: tool preview stream failed")
		}
		frames++
		total += proto.Size(u)
		if u == nil || frames > 64 || total > maxSuiteBytes || len(u.ProtoReflect().GetUnknown()) != 0 {
			return out, status.Error(codes.DataLoss, "agentsessions: invalid tool preview stream")
		}
		if frames == 1 {
			if !freshToolSession(u.GetSession()) || u.GetSession().GetMetadata().GetUid() != out.SessionUID {
				return out, status.Error(codes.DataLoss, "agentsessions: tool preview session mismatch")
			}
			continue
		}
		frame, ok := u.GetUpdate().(*v1.ExecUpdate_Record)
		if !ok || frame.Record == nil || !validEvent(frame.Record.GetEvent()) || len(frame.Record.ProtoReflect().GetUnknown()) != 0 || frame.Record.Seq != out.Head.Seq+1 || frame.Record.PrevHash != out.Head.Hash {
			return out, status.Error(codes.DataLoss, "agentsessions: tool preview record mismatch")
		}
		record := eventlog.RecordFromProto(proto.Clone(frame.Record).(*v1.LogRecord))
		hash, err := canon.HashRecord(record.PrevHash, record.Seq, record.Event)
		if err != nil || hash != record.Hash {
			return out, status.Error(codes.DataLoss, "agentsessions: tool preview hash mismatch")
		}
		out.Head = JournalHead{Seq: record.Seq, Hash: hash}
		records = append(records, record)
	}
	trace, err := verifyToolPreviewRequest(ctx, records, r.Model, config, r.Input)
	if err != nil {
		return out, status.Error(codes.DataLoss, "agentsessions: tool preview reconstruction failed")
	}
	out.Output = trace.Answer
	out.ToolCalls = trace.CallsByName
	if out.ToolCalls == nil {
		out.ToolCalls = map[string]int{}
	}
	out.ToolEvidenceComplete = true
	return out, nil
}

// verifyToolPreviewRequest binds reconstruction to the invocation we submitted,
// not merely to an internally consistent transcript supplied by the host.
func verifyToolPreviewRequest(ctx context.Context, records []eventlog.Record, model string, config []byte, input string) (localtools.Trace, error) {
	trace, err := localtools.VerifyTrace(ctx, records, model)
	if err != nil {
		return localtools.Trace{}, err
	}
	if !bytes.Equal(records[0].Event.ExecutionStart.Config, config) || records[1].Event.Message.Text() != input {
		return localtools.Trace{}, status.Error(codes.DataLoss, "agentsessions: tool preview invocation mismatch")
	}
	return trace, nil
}

// inspectToolPreview distinguishes invalid/incomplete retained content from an
// interrupted read. Inspection never retries or evaluates assertions.
func inspectToolPreview(ctx context.Context, out PreviewSession, records []eventlog.Record) (PreviewSession, error) {
	trace, err := localtools.VerifyTrace(ctx, records, out.Model)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err == nil {
		out.Outcome, out.Answer, out.ToolCalls = "completed", trace.Answer, trace.ToolCalls
		out.ToolSources = trace.Sources
	}
	return out, nil
}

func freshToolSession(s *v1.Session) bool {
	return s != nil && safeIdentity(s.GetMetadata().GetUid()) && s.GetHarness() == localtools.HarnessName && s.GetLastSeq() == 0 && s.GetParentUid() == "" && s.GetForkSeq() == 0
}
