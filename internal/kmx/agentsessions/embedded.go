package agentsessions

import (
	"context"
	"net"
	"unicode/utf8"

	"github.com/agent-primitives/sessions/api"
	sdk "github.com/agent-primitives/sessions/client"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// PreviewLabel marks Sessions created by kmx local preview. It is correlation
// only: a preview Session is never suite, receipt or deployment evidence.
const PreviewLabel = "kmx.local-preview"

// DialInProcess connects to a Sessions server that the caller embeds in the
// same process. dialer supplies the connection (for example an in-memory
// listener); no address is resolved and no socket or port is opened here.
// Plaintext is used because bytes never leave the process.
func DialInProcess(dialer func(context.Context, string) (net.Conn, error)) (*Client, error) {
	if dialer == nil {
		return nil, status.Error(codes.InvalidArgument, "agentsessions: in-process dialer required")
	}
	client, err := sdk.Dial("passthrough:///kmx-local-preview", sdk.WithDialOptions(
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableRetry(),
		grpc.WithDisableServiceConfig(),
		grpc.WithContextDialer(dialer),
	))
	if err != nil {
		return nil, safeRPCError(err, "agentsessions: transport initialization failed")
	}
	return &Client{sdk: client}, nil
}

// PreviewCaseRequest identifies one local preview turn of a single-file agent.
// Revision is the agent file's sha256-prefixed byte revision; Agent and
// Example are the authored identities, or Example is empty for ad-hoc input.
type PreviewCaseRequest struct {
	Revision, Agent, Example, Instructions, Model, Input string
	// UsedSessionUIDs is caller-owned and read-only during invocation, as for
	// RunSuiteCase.
	UsedSessionUIDs map[string]bool
	ToolDefinitions []api.ToolDefinition
}

// RunPreviewCase executes one fresh Session with the same strict five-record
// chat shape, bounds and zero-model replay proof as RunSuiteCase. Only its
// labels differ: they mark local preview and never carry suite identity.
func (c *Client) RunPreviewCase(ctx context.Context, request PreviewCaseRequest) (CaseResult, error) {
	revision := request.Revision
	if len(revision) != len("sha256:")+64 || revision[:7] != "sha256:" || !verifyDigest(revision[7:]) || !safeIdentity(request.Agent) || (request.Example != "" && !safeIdentity(request.Example)) {
		return CaseResult{}, status.Error(codes.InvalidArgument, "agentsessions: valid preview identity required")
	}
	labels := map[string]string{
		PreviewLabel:         "true",
		"kmx.agent":          request.Agent,
		"kmx.agent-revision": revision,
	}
	if request.Example != "" {
		labels["kmx.example"] = request.Example
	}
	return c.runCase(ctx, CaseRequest{Instructions: request.Instructions, Model: request.Model, Input: request.Input}, labels, true, request.UsedSessionUIDs)
}

// PreviewSession is a read-only reconstruction of one journaled preview
// Session. Input and Answer are retained content, not safe log projections.
type PreviewSession struct {
	SessionUID               string
	Agent, Revision, Example string
	Model                    string
	// Outcome is "completed" only for the complete verified chat shape whose
	// zero-model replay reproduced the recorded answer; otherwise "incomplete".
	Outcome       string
	Input, Answer string
	// ModelCalls counts live model invocations during reconstruction. A
	// verified reconstruction always reports zero.
	ModelCalls int
	ToolCalls  int
	Records    int
}

// InspectPreviewSession reads one Session's journal without Exec, Resume or a
// fence and, for a complete chat turn, replays it against the pinned chat
// harness with every live model call refused.
func (c *Client) InspectPreviewSession(ctx context.Context, uid string) (PreviewSession, error) {
	out := PreviewSession{SessionUID: uid, Outcome: "incomplete"}
	if _, bounded := ctx.Deadline(); !bounded || !safeIdentity(uid) {
		return out, status.Error(codes.InvalidArgument, "agentsessions: deadline and valid session identity required")
	}
	session, err := c.sdk.GetSession(ctx, uid)
	if err != nil {
		return out, safeRPCError(err, "agentsessions: session read failed")
	}
	labels := session.GetLabels()
	if labels[PreviewLabel] != "true" {
		return out, status.Error(codes.FailedPrecondition, "agentsessions: not a local preview session")
	}
	for _, field := range []struct {
		key  string
		into *string
	}{{"kmx.agent", &out.Agent}, {"kmx.agent-revision", &out.Revision}, {"kmx.example", &out.Example}} {
		if value := labels[field.key]; safeIdentity(value) {
			*field.into = value
		}
	}
	records, _, result, err := c.replayVerifiedJournal(ctx, uid, maxVerifyRecords)
	if err != nil {
		return out, err
	}
	if result.Status != "" {
		return out, status.Error(codes.DataLoss, result.Detail)
	}
	out.Records = len(records)
	for _, record := range records {
		event := record.Event
		switch {
		case event.Kind == api.EventInput && out.Input == "" && referenceTextMessage(event.Message, "user"):
			if text, err := boundedReferenceText(event.Message); err == nil && utf8.ValidString(text) {
				out.Input = text
			}
		case event.Kind == api.EventModelCall && event.ModelCall != nil && safeIdentity(event.ModelCall.Model):
			out.Model = event.ModelCall.Model
		}
	}
	if session.GetHarness() == localtools.HarnessName {
		return inspectToolPreview(ctx, out, records)
	}
	if !referenceChatInvocation(records) {
		return out, nil
	}
	answer, err := boundedReferenceText(records[3].Event.Message)
	if err != nil {
		return out, err
	}
	proof, err := referenceChatProof(ctx, records, out.Model, verifySHA256([]byte(answer)))
	out.ModelCalls = proof.ModelCalls
	if err != nil {
		return out, err
	}
	if proof.Status == "equivalent" && proof.ModelCalls == 0 {
		out.Outcome, out.Answer = "completed", answer
	}
	return out, nil
}

// ListPreviewSessions returns the UIDs of local preview Sessions in the
// connected journal, newest first.
func (c *Client) ListPreviewSessions(ctx context.Context) ([]string, error) {
	sessions, err := c.sdk.ListSessions(ctx, "")
	if err != nil {
		return nil, safeRPCError(err, "agentsessions: session listing failed")
	}
	var uids []string
	for _, session := range sessions {
		if uid := session.GetMetadata().GetUid(); session.GetLabels()[PreviewLabel] == "true" && safeIdentity(uid) {
			uids = append(uids, uid)
		}
	}
	return uids, nil
}
