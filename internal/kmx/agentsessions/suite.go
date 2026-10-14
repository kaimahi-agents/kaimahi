package agentsessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/wire"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	suiteHarnessVersion = "1" // pinned built-in chat replay/config contract
	maxSuiteFrames      = 256
	maxSuiteBytes       = 4 << 20
)

// SuiteCaseRequest identifies one packaged tool-free chat invocation. SuiteDigest
// is sha256-prefixed; CaseDigest is raw lowercase SHA-256. These and the member
// label correlate the Session; they are not server-enforced ownership.
type SuiteCaseRequest struct {
	SuiteDigest, Member, CaseDigest, Instructions, Model, Input string
	// UsedSessionUIDs is caller-owned and read-only during invocation. A reused
	// created UID refuses before Exec. Nil is normal for the first case.
	UsedSessionUIDs map[string]bool
}

// ConfigDigest binds the exact UTF-8 JSON system_prompt encoding in the chat
// configuration domain. Model selection remains separately host-owned.
func ConfigDigest(instructions string) (string, error) {
	config, err := chatConfig(instructions)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("kmx.agentsessions.chat-config/v1\x00"), config...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func chatConfig(instructions string) ([]byte, error) {
	if !utf8.ValidString(instructions) {
		return nil, status.Error(codes.InvalidArgument, "agentsessions: invalid case config")
	}
	config, err := json.Marshal(struct {
		SystemPrompt string `json:"system_prompt"`
	}{instructions})
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "agentsessions: invalid case config")
	}
	return config, nil
}

// RunSuiteCase executes one fresh Session for one packaged chat case. The caller
// owns host qualification, ordered coverage and cross-case Session uniqueness.
func (c *Client) RunSuiteCase(ctx context.Context, request SuiteCaseRequest) (CaseResult, error) {
	if !strings.HasPrefix(request.SuiteDigest, "sha256:") || !verifyDigest(strings.TrimPrefix(request.SuiteDigest, "sha256:")) || !verifyDigest(request.CaseDigest) || !safeIdentity(request.Member) {
		return CaseResult{}, status.Error(codes.InvalidArgument, "agentsessions: valid suite case identity required")
	}
	return c.runCase(ctx, CaseRequest{Instructions: request.Instructions, Model: request.Model, Input: request.Input}, map[string]string{
		"kmx.suite-digest": request.SuiteDigest,
		"kmx.member":       request.Member,
		"kmx.case-digest":  request.CaseDigest,
	}, true, request.UsedSessionUIDs)
}

// Only this pinned, single-model text chat shape can supply suite proof. The
// legacy runner intentionally keeps its broader event/output compatibility.
func suiteRecordShape(record *v1.LogRecord) bool {
	event := record.GetEvent()
	if event.GetSchemaVersion() < 0 || event.GetSchemaVersion() > 1 {
		return false
	}
	switch record.GetSeq() {
	case 1:
		start := event.GetExecutionStart()
		return event.Kind == v1.EventKind_EVENT_EXECUTION_START && start.GetHarness() == "chat" && start.GetHarnessVersion() == suiteHarnessVersion
	case 2:
		return event.Kind == v1.EventKind_EVENT_INPUT && suiteTextMessage(event.GetMessage(), "user")
	case 3:
		model := event.GetModel()
		return event.Kind == v1.EventKind_EVENT_MODEL_CALL && safeIdentity(model.GetId()) && verifyDigest(model.GetInputHash()) && len(model.GetParams()) == 0
	case 4:
		return event.Kind == v1.EventKind_EVENT_OUTPUT && suiteTextMessage(event.GetMessage(), "assistant")
	case 5:
		return event.Kind == v1.EventKind_EVENT_END
	default:
		return false
	}
}

func suiteTextMessage(message *v1.Message, role string) bool {
	domain := wire.MessageFromProto(message)
	if !referenceTextMessage(domain, role) {
		return false
	}
	for _, part := range domain.Parts {
		if !utf8.ValidString(part.Text.Text) {
			return false
		}
	}
	return true
}
