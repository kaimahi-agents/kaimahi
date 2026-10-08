// Package agentsessions owns the transport and journal evidence for a single chat evaluation.
package agentsessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/canon"
	sdk "github.com/aramase/agentsessions/client"
	"github.com/aramase/agentsessions/wire"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const maxAnswerBytes = 1 << 20

type Options struct{ Address, CAFile string }
type CaseRequest struct{ PortableDigest, CasesDigest, Instructions, Model, Input string }
type JournalHead struct {
	Seq  int64
	Hash string
}
type CaseResult struct {
	SessionUID, Harness, Model string
	// ModelMixed is sticky and clears Model when distinct safe models are observed.
	ModelMixed bool
	Head       JournalHead
	Output     string
}
type Client struct{ sdk *sdk.Client }

func (c *Client) Close() error { return c.sdk.Close() }

// RunCase creates a fresh chat session and executes exactly one turn. The caller must
// supply a deadline and an expected model. Model is observed, never selected by metadata.
// On failure, the result retains safe identity and the last verified journal prefix.
func (c *Client) RunCase(ctx context.Context, request CaseRequest) (CaseResult, error) {
	var result CaseResult
	deadline, bounded := ctx.Deadline()
	if !bounded || !safeIdentity(request.Model) || !utf8.ValidString(request.Instructions) || !utf8.ValidString(request.Input) {
		return result, status.Error(codes.InvalidArgument, "agentsessions: deadline and valid case request required")
	}
	if err := ctx.Err(); err != nil {
		return result, safeRPCError(err, "agentsessions: case context ended")
	}
	config, err := json.Marshal(struct {
		SystemPrompt string `json:"system_prompt"`
	}{request.Instructions})
	if err != nil {
		return result, status.Error(codes.InvalidArgument, "agentsessions: invalid case config")
	}
	session, err := c.sdk.CreateSession(ctx, &v1.Session{
		Harness: "chat",
		Labels: map[string]string{
			"kmx.portable-digest": request.PortableDigest,
			"kmx.cases-digest":    request.CasesDigest,
		},
	})
	if err != nil {
		return result, safeRPCError(err, "agentsessions: session creation failed")
	}
	if safeIdentity(session.GetMetadata().GetUid()) {
		result.SessionUID = session.GetMetadata().GetUid()
	}
	if safeIdentity(session.GetHarness()) {
		result.Harness = session.GetHarness()
	}
	if !freshChatSession(session) {
		return result, status.Error(codes.FailedPrecondition, "agentsessions: invalid created session identity")
	}

	// The SDK's ExecOptions does not expose config or deadline; use its official stub.
	input := wire.MessageToProto(api.TextMessage("user", request.Input))
	zero := int64(0)
	stream, err := c.sdk.Sessions().Exec(ctx, &v1.ExecRequest{
		Session:         result.SessionUID,
		Harness:         "chat",
		Inputs:          []*v1.Message{input},
		Config:          config,
		ExpectedLastSeq: &zero,
		DeadlineUnix:    deadline.Unix(),
	})
	if err != nil {
		return result, safeRPCError(err, "agentsessions: execution failed")
	}

	var evidenceErr error
	fail := func(code codes.Code, diagnostic string) {
		if evidenceErr == nil {
			evidenceErr = status.Error(code, diagnostic)
		}
	}
	var seenSession, chainValid, ended, seenStart, seenInput bool
	var executionID, firstModel string
	var output strings.Builder
	var outputOverflow bool
	for {
		update, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, safeRPCError(err, "agentsessions: execution stream failed")
		}
		if update == nil || len(update.ProtoReflect().GetUnknown()) != 0 {
			chainValid = false
			fail(codes.DataLoss, "agentsessions: malformed execution frame")
			continue
		}
		if !seenSession {
			seenSession = true
			s := update.GetSession()
			chainValid = freshChatSession(s) && s.GetMetadata().GetUid() == result.SessionUID
			if !chainValid {
				fail(codes.DataLoss, "agentsessions: invalid execution session identity")
			}
			continue
		}
		// Drain even after invalid evidence. Once the chain breaks, later records
		// cannot repair it; model/completion failures alone do not break the chain.
		if !chainValid {
			continue
		}
		switch frame := update.GetUpdate().(type) {
		case *v1.ExecUpdate_Record:
			record := frame.Record
			event := record.GetEvent()
			if ended || !validEvent(event) || len(record.ProtoReflect().GetUnknown()) != 0 || record.GetSeq() != result.Head.Seq+1 || record.GetPrevHash() != result.Head.Hash || executionID != "" && event.GetExecutionId() != executionID {
				chainValid = false
				fail(codes.DataLoss, "agentsessions: malformed journal record")
				continue
			}
			hash, err := canon.HashRecord(record.GetPrevHash(), record.GetSeq(), wire.EventFromProto(event))
			if err != nil || hash != record.GetContentHash() {
				chainValid = false
				fail(codes.DataLoss, "agentsessions: journal integrity check failed")
				continue
			}
			result.Head = JournalHead{Seq: record.Seq, Hash: hash}
			executionID = event.ExecutionId
			if event.Kind != v1.EventKind_EVENT_EXECUTION_START && event.Kind != v1.EventKind_EVENT_INPUT && (!seenStart || !seenInput) {
				fail(codes.DataLoss, "agentsessions: incomplete invocation prefix")
			}
			switch event.Kind {
			case v1.EventKind_EVENT_EXECUTION_START:
				start := event.GetExecutionStart()
				if seenStart || record.Seq != 1 || !bytes.Equal(start.GetConfig(), config) || start.GetResumeFromSeq() != 0 || start.InputCount == nil || start.GetInputCount() != 1 {
					fail(codes.DataLoss, "agentsessions: invocation start mismatch")
				}
				seenStart = true
			case v1.EventKind_EVENT_INPUT:
				if !seenStart || seenInput || record.Seq != 2 || !proto.Equal(event.GetMessage(), input) {
					fail(codes.DataLoss, "agentsessions: invocation input mismatch")
				}
				seenInput = true
			case v1.EventKind_EVENT_MODEL_CALL:
				model := event.GetModel().GetModel()
				if !safeIdentity(model) {
					fail(codes.FailedPrecondition, "agentsessions: missing or invalid observed model")
					continue
				}
				if firstModel == "" {
					firstModel = model
					result.Model = model
				} else if model != firstModel {
					result.ModelMixed = true
					result.Model = ""
				}
				if model != request.Model || result.ModelMixed {
					fail(codes.FailedPrecondition, "agentsessions: observed model mismatch")
				}
			case v1.EventKind_EVENT_OUTPUT:
				if outputOverflow {
					continue
				}
				text := wire.MessageFromProto(event.GetMessage()).Text()
				if len(text) > maxAnswerBytes-output.Len() {
					outputOverflow = true
					result.Output = ""
					output.Reset()
					fail(codes.ResourceExhausted, "agentsessions: committed answer exceeds 1 MiB")
					continue
				}
				output.WriteString(text)
				result.Output = output.String()
			case v1.EventKind_EVENT_ERROR:
				fail(remoteCode(event.GetError().GetCode()), "agentsessions: execution reported an error")
			case v1.EventKind_EVENT_END:
				ended = true
				if event.GetEnd().GetError() != nil {
					fail(remoteCode(event.GetEnd().GetError().GetCode()), "agentsessions: execution completion reported an error")
				} else if event.GetEnd().GetState() != "COMPLETED" {
					fail(codes.FailedPrecondition, "agentsessions: execution did not complete")
				}
			}
		case *v1.ExecUpdate_Delta:
			d := frame.Delta
			if d == nil || ended || executionID == "" || d.GetExecutionId() != executionID || d.GetPartIndex() < 0 {
				chainValid = false
				fail(codes.DataLoss, "agentsessions: malformed streaming delta")
			}
		default:
			chainValid = false
			fail(codes.DataLoss, "agentsessions: malformed execution frame")
		}
	}
	if !seenSession {
		fail(codes.DataLoss, "agentsessions: missing execution session")
	}
	if !seenStart || !seenInput {
		fail(codes.DataLoss, "agentsessions: missing invocation prefix")
	}
	if firstModel == "" {
		fail(codes.FailedPrecondition, "agentsessions: missing observed model")
	}
	if !ended {
		fail(codes.FailedPrecondition, "agentsessions: missing execution completion")
	}
	return result, evidenceErr
}

func freshChatSession(s *v1.Session) bool {
	return s != nil && safeIdentity(s.GetMetadata().GetUid()) && s.GetHarness() == "chat" && s.GetLastSeq() == 0 && s.GetParentUid() == "" && s.GetForkSeq() == 0
}

// Identities may be printed or persisted by the caller; never retain unbounded,
// control-laden, or credential-shaped values supplied by a remote host.
func safeIdentity(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, c := range value {
		if c < '!' || c > '~' {
			return false
		}
	}
	return secretshapes.Match(value) == nil
}

func safeRPCError(err error, diagnostic string) error {
	code := status.Code(err)
	if errors.Is(err, context.Canceled) {
		code = codes.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = codes.DeadlineExceeded
	}
	return status.Error(code, diagnostic)
}

func remoteCode(code int32) codes.Code {
	if code <= int32(codes.OK) || code > int32(codes.Unauthenticated) {
		return codes.Unknown
	}
	return codes.Code(code)
}

func validEvent(e *v1.Event) bool {
	if e == nil || e.GetExecutionId() == "" || e.GetTs() != nil && e.GetTs().CheckValid() != nil {
		return false
	}
	var body bool
	switch e.Kind {
	case v1.EventKind_EVENT_INPUT, v1.EventKind_EVENT_OUTPUT:
		body = e.GetMessage() != nil
	case v1.EventKind_EVENT_MODEL_CALL:
		body = e.GetModel() != nil
	case v1.EventKind_EVENT_TOOL_CALL:
		body = e.GetTool() != nil
	case v1.EventKind_EVENT_TOOL_RESULT:
		body = e.GetResult() != nil
	case v1.EventKind_EVENT_APPROVAL_REQUEST:
		body = e.GetApproval() != nil
	case v1.EventKind_EVENT_APPROVAL_RESULT:
		body = e.GetApprovalResult() != nil
	case v1.EventKind_EVENT_USAGE:
		body = e.GetUsage() != nil
	case v1.EventKind_EVENT_LIFECYCLE:
		body = e.GetLifecycle() != nil
	case v1.EventKind_EVENT_END:
		body = e.GetEnd() != nil
	case v1.EventKind_EVENT_ERROR:
		body = e.GetError() != nil
	case v1.EventKind_EVENT_EXECUTION_START:
		body = e.GetExecutionStart() != nil
	}
	// Fail closed if the upstream conversion would discard unknown wire content:
	// only the lossless domain representation is covered by HashRecord.
	return body && proto.Equal(e, wire.EventToProto(wire.EventFromProto(e)))
}
