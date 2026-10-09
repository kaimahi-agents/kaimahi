package agentsessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/canon"
	"github.com/aramase/agentsessions/controller"
	"github.com/aramase/agentsessions/eventlog"
	"github.com/aramase/agentsessions/harness/chatagent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ReferenceRevision identifies the local chat semantics, not the remote host's code.
const ReferenceRevision = "v0.1.3-0.20261008190619-e680ea10d3b4"

const (
	maxVerifyRecords = 20000
	maxVerifyBytes   = 64 << 20
)

type VerifyRequest struct {
	SessionUID, Harness, Model string
	Head                       JournalHead
	AnswerSHA256               string
}
type VerifyResult struct {
	Status       string `json:"status"`
	ConfigSHA256 string `json:"configSHA256,omitempty"`
	AnswerSHA256 string `json:"answerSHA256,omitempty"`
	ModelCalls   int    `json:"modelCalls"`
	Records      int    `json:"records"`
	Detail       string `json:"detail,omitempty"`
}

// VerifyCase checks a receipt-bound prefix against the pinned reference chat
// harness. Equivalent means reference equivalence for this invocation only: a
// custom host harness named chat is not source/version-attested by this proof.
// Mismatch/unsupported are evidence results; invalid requests, resource limits,
// cancellation and RPC failures return unknown with a fixed, sanitized error.
func (c *Client) VerifyCase(ctx context.Context, request VerifyRequest) (VerifyResult, error) {
	result := VerifyResult{Status: "unknown"}
	evidence := func(state, detail string) (VerifyResult, error) {
		result.Status = state
		result.Detail = detail
		return result, nil
	}
	fail := func(code codes.Code, detail string) (VerifyResult, error) {
		result.Detail = detail
		return result, status.Error(code, detail)
	}
	_, bounded := ctx.Deadline()
	if !bounded || !safeIdentity(request.SessionUID) || !safeIdentity(request.Harness) || !safeIdentity(request.Model) || request.Head.Seq <= 0 || !verifyDigest(request.Head.Hash) || !verifyDigest(request.AnswerSHA256) {
		return fail(codes.InvalidArgument, "agentsessions: deadline and valid verification request required")
	}
	if err := ctx.Err(); err != nil {
		result.Detail = "agentsessions: verification context ended"
		return result, safeRPCError(err, result.Detail)
	}
	if request.Harness != "chat" {
		return evidence("unsupported", "agentsessions: reference harness unavailable")
	}
	if request.Head.Seq > maxVerifyRecords {
		return fail(codes.ResourceExhausted, "agentsessions: journal exceeds verification bounds")
	}

	// The SDK convenience method buffers without limits. Use its official stub
	// and cancel the stream on every early refusal; never Exec, Resume or fence a
	// live session. The caller chooses the connection, not journal metadata.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.sdk.Sessions().Replay(ctx, &v1.ReplayRequest{Session: request.SessionUID, FromSeq: 1, ToSeq: request.Head.Seq}, grpc.MaxCallRecvMsgSize(maxVerifyBytes))
	if err != nil {
		result.Detail = "agentsessions: replay request failed"
		return result, safeRPCError(err, result.Detail)
	}
	var records []eventlog.Record
	var head JournalHead
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			result.Detail = "agentsessions: verification context ended"
			return result, safeRPCError(err, result.Detail)
		}
		record, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			result.Detail = "agentsessions: replay stream failed"
			return result, safeRPCError(err, result.Detail)
		}
		size := proto.Size(record)
		if len(records) >= maxVerifyRecords || size > maxVerifyBytes-total {
			return fail(codes.ResourceExhausted, "agentsessions: journal exceeds verification bounds")
		}
		total += size
		if record == nil || len(record.ProtoReflect().GetUnknown()) != 0 || !validEvent(record.GetEvent()) {
			return evidence("unsupported", "agentsessions: journal wire content unavailable")
		}
		if record.Seq != head.Seq+1 || record.Seq > request.Head.Seq || record.PrevHash != head.Hash {
			return evidence("mismatch", "agentsessions: journal prefix integrity mismatch")
		}
		// The received protobuf is owned by this call. Clone before the domain
		// conversion because opaque buffers/config are otherwise aliased, and
		// NewFrom copies only the outer record slice.
		isolated := proto.Clone(record).(*v1.LogRecord)
		domain := eventlog.RecordFromProto(isolated)
		hash, err := canon.HashRecord(domain.PrevHash, domain.Seq, domain.Event)
		if err != nil || hash != domain.Hash {
			return evidence("mismatch", "agentsessions: journal prefix integrity mismatch")
		}
		records = append(records, domain)
		result.Records = len(records)
		head = JournalHead{Seq: domain.Seq, Hash: hash}
	}
	if err := ctx.Err(); err != nil {
		result.Detail = "agentsessions: verification context ended"
		return result, safeRPCError(err, result.Detail)
	}
	if head != request.Head {
		return evidence("mismatch", "agentsessions: receipt prefix head mismatch")
	}
	if !referenceChatInvocation(records) {
		return evidence("unsupported", "agentsessions: reference invocation shape unavailable")
	}
	result.ConfigSHA256 = verifySHA256(records[0].Event.ExecutionStart.Config)
	if records[2].Event.ModelCall.Model != request.Model {
		return evidence("mismatch", "agentsessions: recorded model mismatch")
	}

	// Bound concatenation before calling upstream Message.Text, which otherwise
	// creates an unbounded string. Text-only shape validation preserves every part.
	var answer strings.Builder
	for _, part := range records[3].Event.Message.Parts {
		if len(part.Text.Text) > maxAnswerBytes-answer.Len() {
			return fail(codes.ResourceExhausted, "agentsessions: committed answer exceeds 1 MiB")
		}
		answer.WriteString(part.Text.Text)
	}
	recorded := answer.String()
	calls := 0
	failOnCall := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{}, errors.New("agentsessions: live model forbidden")
	}
	// The controller mints a fence only on this isolated memory copy. No provider
	// endpoint/client, tool executor, live store or live fence is available.
	replay, err := controller.New(eventlog.AsStore(eventlog.NewFrom(records)), failOnCall)
	if err != nil {
		return fail(codes.Internal, "agentsessions: reference reconstruction unavailable")
	}
	outputs, err := replay.Replay(ctx, chatagent.Harness{Model: request.Model})
	result.ModelCalls = calls + replay.ModelInvocations()
	if ctx.Err() != nil {
		result.Detail = "agentsessions: verification context ended"
		return result, safeRPCError(ctx.Err(), result.Detail)
	}
	if result.ModelCalls != 0 {
		return fail(codes.Internal, "agentsessions: reference replay reached a live model")
	}
	if err != nil {
		return evidence("mismatch", "agentsessions: reference replay diverged")
	}
	// Replay checks fingerprints and complete consumption itself. Compare output
	// count and exact text independently; receipt digests never normalize text.
	if len(outputs) != 1 || outputs[0] != recorded {
		return evidence("mismatch", "agentsessions: reconstructed outputs mismatch")
	}
	result.AnswerSHA256 = verifySHA256([]byte(outputs[0]))
	if result.AnswerSHA256 != request.AnswerSHA256 {
		return evidence("mismatch", "agentsessions: reconstructed answer digest mismatch")
	}
	return evidence("equivalent", "")
}

// Only the fresh, counted, single-turn, text-only chat shape is supported. This
// closes controller Replay's intentional skipping of unfinished turns and its
// legacy markerless defaults without reimplementing its replay state machine.
func referenceChatInvocation(records []eventlog.Record) bool {
	if len(records) != 5 {
		return false
	}
	kinds := []api.EventKind{api.EventExecutionStart, api.EventInput, api.EventModelCall, api.EventOutput, api.EventEnd}
	executionID := records[0].Event.ExecutionID
	if !safeIdentity(executionID) {
		return false
	}
	for i, record := range records {
		if record.Event.Kind != kinds[i] || record.Event.ExecutionID != executionID || record.Event.SchemaVersion < 0 || record.Event.SchemaVersion > 1 {
			return false
		}
	}
	start := records[0].Event.ExecutionStart
	model := records[2].Event.ModelCall
	end := records[4].Event.End
	return start.ResumeFromSeq == 0 && start.InputCount != nil && *start.InputCount == 1 &&
		referenceTextMessage(records[1].Event.Message, "user") && referenceTextMessage(records[3].Event.Message, "assistant") &&
		safeIdentity(model.Model) && verifyDigest(model.InputHash) && len(model.Params) == 0 &&
		end.State == "COMPLETED" && end.Error == nil
}
func referenceTextMessage(message *api.Message, role string) bool {
	if message == nil || message.Role != role || len(message.Parts) == 0 {
		return false
	}
	for _, part := range message.Parts {
		if part.Text == nil {
			return false
		}
	}
	return true
}
func verifyDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func verifySHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
