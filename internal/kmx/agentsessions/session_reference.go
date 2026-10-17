package agentsessions

import (
	"context"

	"github.com/agent-primitives/sessions/eventlog"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExtractSessionReference supports only a fresh completed text chat. Asking for
// record six detects an already appended second turn without buffering a whole
// journal. Store is deliberately empty: the caller binds its explicitly selected
// normalized source address before constructing the runtime reference case.
func (c *Client) ExtractSessionReference(ctx context.Context, uid string) (runtime.SessionReference, error) {
	if _, bounded := ctx.Deadline(); !bounded || !safeIdentity(uid) {
		return runtime.SessionReference{}, status.Error(codes.InvalidArgument, "agentsessions: deadline and valid source identity required")
	}
	if err := ctx.Err(); err != nil {
		return runtime.SessionReference{}, safeRPCError(err, "agentsessions: source context ended")
	}
	records, head, proof, err := c.replayVerifiedJournal(ctx, uid, 6)
	if err != nil {
		return runtime.SessionReference{}, err
	}
	if proof.Status != "" || !referenceChatInvocation(records) {
		return runtime.SessionReference{}, status.Error(codes.DataLoss, "agentsessions: source invocation unavailable")
	}
	input, answer, err := referenceSourceText(records)
	if err != nil {
		return runtime.SessionReference{}, err
	}
	model := records[2].Event.ModelCall.Model
	proof, err = referenceChatProof(ctx, records, model, verifySHA256([]byte(answer)))
	if err != nil {
		return runtime.SessionReference{}, err
	}
	if proof.Status != "equivalent" {
		return runtime.SessionReference{}, status.Error(codes.DataLoss, "agentsessions: source reference equivalence unavailable")
	}
	return runtime.SessionReference{SessionUID: uid, Harness: "chat", HarnessVersion: ReferenceRevision, Model: model, InputSeq: 2, PrefixSeq: head.Seq, PrefixHash: head.Hash, InputDigest: verifySHA256([]byte(input)), AnswerDigest: proof.AnswerSHA256, ConfigDigest: proof.ConfigSHA256}, nil
}

// ResolveSessionReference returns input only after checking the pinned prefix,
// metadata, content digests and original request/output equivalence. It makes
// exactly one bounded read on this already selected connection, never dials Store.
func (c *Client) ResolveSessionReference(ctx context.Context, reference runtime.SessionReference) (string, error) {
	if _, bounded := ctx.Deadline(); !bounded || !validSourceReference(reference) {
		return "", status.Error(codes.InvalidArgument, "agentsessions: deadline and valid source reference required")
	}
	if err := ctx.Err(); err != nil {
		return "", safeRPCError(err, "agentsessions: source context ended")
	}
	records, head, proof, err := c.replayVerifiedJournal(ctx, reference.SessionUID, reference.PrefixSeq)
	if err != nil {
		return "", err
	}
	if proof.Status != "" || head.Seq != reference.PrefixSeq || head.Hash != reference.PrefixHash || !referenceChatInvocation(records) {
		return "", status.Error(codes.DataLoss, "agentsessions: source prefix unavailable")
	}
	input, answer, err := referenceSourceText(records)
	if err != nil {
		return "", err
	}
	if verifySHA256([]byte(input)) != reference.InputDigest || verifySHA256([]byte(answer)) != reference.AnswerDigest || verifySHA256(records[0].Event.ExecutionStart.Config) != reference.ConfigDigest {
		return "", status.Error(codes.DataLoss, "agentsessions: source content identity mismatch")
	}
	proof, err = referenceChatProof(ctx, records, reference.Model, reference.AnswerDigest)
	if err != nil {
		return "", err
	}
	if proof.Status != "equivalent" {
		return "", status.Error(codes.DataLoss, "agentsessions: source reference equivalence unavailable")
	}
	return input, nil
}

func referenceSourceText(records []eventlog.Record) (string, string, error) {
	input, err := boundedReferenceText(records[1].Event.Message)
	if err != nil {
		return "", "", err
	}
	answer, err := boundedReferenceText(records[3].Event.Message)
	if err != nil {
		return "", "", err
	}
	return input, answer, nil
}

func validSourceReference(r runtime.SessionReference) bool {
	for _, value := range []string{r.Store, r.SessionUID, r.Harness, r.HarnessVersion, r.Model} {
		if !safeIdentity(value) {
			return false
		}
	}
	for _, value := range []string{r.PrefixHash, r.InputDigest, r.AnswerDigest, r.ConfigDigest} {
		if !verifyDigest(value) {
			return false
		}
	}
	return r.Harness == "chat" && r.HarnessVersion == ReferenceRevision && r.InputSeq == 2 && r.PrefixSeq == 5
}
