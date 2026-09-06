package inbound

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/notify"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// Approval commands typed in Slack. What is a command, who may
// give one, what it does to the request, what it records, and what it
// never does (run the agent, spend, burn a grant, re-decide).

// Synthetic ids (the tree's identifier scanner exempts low-entropy
// GUIDs). reqA and reqB share their first block, so the 8-character
// prefix "0000000a" is ambiguous between them; reqBudget's is unique.
const (
	reqA      = "0000000a-0000-0000-0000-00000000000a"
	reqB      = "0000000a-0000-0000-0000-00000000000b"
	reqBudget = "000000cc-0000-0000-0000-000000000000"
)

type fakeReplier struct {
	mu      sync.Mutex
	replies []string // "thread: text"
}

func (r *fakeReplier) Reply(text, thread string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = append(r.replies, thread+": "+text)
}

func (r *fakeReplier) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.replies...)
}

// commandHooks adds approver files to the Slack hooks: "slack-chan" has
// approvers U2 and U9, "slack-noapprovers" names a missing file, and
// "slack-empty" an empty one.
func commandHooks(t *testing.T, h map[string]config.InboundHook) map[string]config.InboundHook {
	t.Helper()
	h = slackHooks(t, h)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "approvers"), []byte("U2, U9\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty"), []byte("\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "garbled"), []byte("U2,not an id\n"), 0o600))
	base := h["slack-chan"]
	base.SlackApproversFile = filepath.Join(dir, "approvers")
	h["slack-chan"] = base
	base.SlackApproversFile = filepath.Join(dir, "missing")
	h["slack-noapprovers"] = base
	base.SlackApproversFile = filepath.Join(dir, "empty")
	h["slack-empty"] = base
	base.SlackApproversFile = filepath.Join(dir, "garbled")
	h["slack-garbled"] = base
	wide := h["slack-chan"]
	wide.SlackDefaultUses, wide.SlackDefaultTTL = 5, "1h"
	h["slack-wide"] = wide
	return h
}

func newCommandFixture(t *testing.T, fs *fakeStore) (*fixture, *fakeReplier) {
	t.Helper()
	rep := &fakeReplier{}
	fs.requests = []*store.ApprovalRequest{
		{ID: reqA, CredentialName: "hello-tools", Kind: "tool", Subject: "k8s_get_events", Status: "pending"},
		{ID: reqB, CredentialName: "hello-slack", Kind: "tool", Subject: "conversations_add_message", Status: "pending"},
		{ID: reqBudget, CredentialName: "hello-world", Kind: "budget", Subject: "tokens", Status: "pending"},
	}
	for _, name := range []string{"slack-chan", "slack-noapprovers", "slack-empty", "slack-garbled", "slack-wide"} {
		fs.grants["inbound-demo/"+name] = 5
	}
	f := newFixture(t, fs, &fakeMeter{}, func(d *Deps) {
		d.Hooks = commandHooks(t, d.Hooks)
		d.Replier = rep
	})
	return f, rep
}

func mention(id, user, text string) string {
	return `{"type":"event_callback","event_id":"` + id + `","event":{"type":"app_mention","user":"` + user +
		`","text":"<@U1> ` + text + `","channel":"C0TEST","ts":"1725.0100","thread_ts":"1725.0050"}}`
}

func (f *fixture) mention(hook, id, user, text string) (int, string) {
	body := mention(id, user, text)
	rec := f.post(hook, body, f.slackSigned(body))
	return rec.Code, rec.Body.String()
}

func TestParseCommand(t *testing.T) {
	c, ok := parseCommand("approve " + reqA + " uses=2 ttl=30m")
	require.True(t, ok)
	require.NoError(t, c.err)
	require.Equal(t, "approve", c.verb)
	require.Equal(t, reqA, c.prefix)
	require.Equal(t, int32(2), *c.uses)
	require.Equal(t, 30*time.Minute, *c.ttl)
	require.Nil(t, c.amount)

	c, ok = parseCommand("APPROVE `00000000-0000` amount=5000")
	require.True(t, ok)
	require.NoError(t, c.err)
	require.Equal(t, "00000000-0000", c.prefix, "backticks are stripped, the verb is case-insensitive")
	require.Equal(t, int64(5000), *c.amount)

	c, ok = parseCommand("deny 00000000")
	require.True(t, ok)
	require.NoError(t, c.err)
	require.Equal(t, "deny", c.verb)

	// Not commands: the mention goes to the agent as before.
	for _, text := range []string{"what is the weather?", "please approve my leave", "approved!", "", "denying it"} {
		_, ok := parseCommand(text)
		require.False(t, ok, text)
	}
	// Commands with bad arguments are still commands — answered as such,
	// never handed to the agent as a question.
	for _, text := range []string{"approve", "approve 0000", "approve " + reqA + " uses=0", "approve " + reqA + " ttl=31d",
		"approve " + reqA + " amount=-1", "approve " + reqA + " bogus=1", "approve " + reqA + " uses", "deny " + reqA + " uses=1",
		"approve not-an-id"} {
		c, ok := parseCommand(text)
		require.True(t, ok, text)
		require.Error(t, c.err, text)
	}
}

func TestReadSlackApprovers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a")
	require.NoError(t, os.WriteFile(p, []byte("U2,U9\nW0ENT\n"), 0o600))
	got, err := readSlackApprovers(p)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"U2": true, "U9": true, "W0ENT": true}, got)
	for _, content := range []string{"", "\n", "U2,c0lower", "U2 <@U9>", "true"} {
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		_, err := readSlackApprovers(p)
		require.Error(t, err, content)
	}
	_, err = readSlackApprovers(filepath.Join(dir, "missing"))
	require.Error(t, err)
}

func TestApproverCommandMintsAGrantWithTheirIdentity(t *testing.T) {
	fs := newFakeStore()
	f, rep := newCommandFixture(t, fs)
	code, body := f.mention("slack-chan", "Ev0000000101", "U2", "approve "+reqA+" uses=2 ttl=30m")
	require.Equal(t, http.StatusOK, code, body)
	require.Contains(t, body, `"command"`)
	// The grant, with the approver's identity and the typed bounds.
	require.Len(t, fs.grantsMade, 1)
	g := fs.grantsMade[0]
	require.Equal(t, "slack:U2", g.DecidedBy)
	require.Equal(t, int32(2), *g.MaxUses)
	require.Equal(t, f.now.Add(30*time.Minute), *g.ExpiresAt)
	require.Equal(t, reqA, g.RequestID)
	require.Equal(t, "slack:U2", fs.find(reqA).DecidedBy)
	// No agent ran, no grant use was burned, nothing was filed.
	require.Zero(t, f.a2a.count())
	require.Equal(t, 5, fs.grants["inbound-demo/slack-chan"])
	require.Empty(t, fs.filed)
	// The record: an inbound 'command' row naming who and what, and the
	// reply in the mention's thread.
	require.Equal(t, []string{"command 200"}, fs.decisions("slack-chan"))
	last := fs.last("slack-chan")
	require.Equal(t, "Ev0000000101", last.DeliveryID)
	require.Contains(t, last.Detail, "approve "+reqA+" by slack:U2: approved request "+reqA)
	require.Contains(t, last.Detail, "grant g-0000000a uses=2 expires=2026-09-01T21:30:00Z")
	replies := rep.all()
	require.Len(t, replies, 1)
	require.True(t, strings.HasPrefix(replies[0], "1725.0050: approved request "+reqA), replies[0])
}

func TestDefaultsBoundAnUnboundedApproval(t *testing.T) {
	fs := newFakeStore()
	f, _ := newCommandFixture(t, fs)
	code, _ := f.mention("slack-chan", "Ev0000000102", "U2", "approve "+reqA)
	require.Equal(t, http.StatusOK, code)
	g := fs.grantsMade[0]
	require.Equal(t, int32(config.DefaultSlackUses), *g.MaxUses)
	require.Equal(t, f.now.Add(15*time.Minute), *g.ExpiresAt)
	// Per hook: a table can widen the default.
	code, _ = f.mention("slack-wide", "Ev0000000103", "U2", "approve "+reqB)
	require.Equal(t, http.StatusOK, code)
	g = fs.grantsMade[1]
	require.Equal(t, int32(5), *g.MaxUses)
	require.Equal(t, f.now.Add(time.Hour), *g.ExpiresAt)
}

func TestExplicitBoundAloneIsEnough(t *testing.T) {
	// uses= alone: no default ttl is added (an explicit bound is the
	// human's decision, and the store accepts either bound alone).
	fs := newFakeStore()
	f, _ := newCommandFixture(t, fs)
	code, _ := f.mention("slack-chan", "Ev0000000104", "U2", "approve "+reqA+" uses=3")
	require.Equal(t, http.StatusOK, code)
	g := fs.grantsMade[0]
	require.Equal(t, int32(3), *g.MaxUses)
	require.Nil(t, g.ExpiresAt)
}

func TestNonApproverIsRefusedAndAudited(t *testing.T) {
	fs := newFakeStore()
	f, rep := newCommandFixture(t, fs)
	// Same channel, a real human, not on the list: channel membership is
	// not authority.
	code, body := f.mention("slack-chan", "Ev0000000105", "U7", "approve "+reqA)
	require.Equal(t, http.StatusForbidden, code, body)
	require.Empty(t, fs.grantsMade)
	require.Equal(t, "pending", fs.find(reqA).Status)
	require.Equal(t, []string{"denied 403"}, fs.decisions("slack-chan"))
	require.Contains(t, fs.last("slack-chan").Detail, "user U7 is not an approver")
	require.Empty(t, rep.all(), "a refusal is audited, not announced in the room")
	require.Zero(t, f.a2a.count())
	// Approver list unreadable, empty or garbled: the command fails
	// closed (503, retryable), and nothing is decided.
	for _, hook := range []string{"slack-noapprovers", "slack-empty", "slack-garbled"} {
		code, body := f.mention(hook, "Ev0000000106", "U2", "approve "+reqA)
		require.Equal(t, http.StatusServiceUnavailable, code, hook+": "+body)
		require.Empty(t, rep.all(), hook) // no reply, no decision
		require.Empty(t, fs.grantsMade)
		require.Equal(t, "pending", fs.find(reqA).Status)
	}
	// …while a QUESTION on the same hook is unaffected by the list's
	// state: the agent still runs.
	code, _ = f.mention("slack-noapprovers", "Ev0000000107", "U7", "what is the weather?")
	require.Equal(t, http.StatusAccepted, code)
	f.waitOutcome(t, "slack-noapprovers")
}

func TestDecidedRequestIsImmutable(t *testing.T) {
	fs := newFakeStore()
	f, rep := newCommandFixture(t, fs)
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000108", "U2", "deny "+reqB)))
	require.Equal(t, "denied", fs.find(reqB).Status)
	require.Equal(t, "slack:U2", fs.find(reqB).DecidedBy)
	// The same command again, and an approve by another approver: both
	// answered "already decided", nothing re-decided.
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000109", "U2", "deny "+reqB)))
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000110", "U9", "approve "+reqB+" uses=9")))
	require.Empty(t, fs.grantsMade)
	replies := rep.all()
	require.Len(t, replies, 3)
	require.Contains(t, replies[0], "denied request "+reqB)
	require.Contains(t, replies[1], "already denied by slack:U2 at 2026-09-02T12:00:00Z; a decided request is immutable")
	require.Contains(t, replies[2], "already denied by slack:U2")
}

func TestPrefixResolution(t *testing.T) {
	fs := newFakeStore()
	f, rep := newCommandFixture(t, fs)
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000111", "U2", "approve 0000000a-0000")))
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000112", "U2", "approve 0000ffff")))
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000113", "U2", "approve "+reqBudget[:8])))
	replies := rep.all()
	require.Contains(t, replies[0], "more than one request starts with 0000000a-0000")
	require.Contains(t, replies[1], "no approval request starts with 0000ffff")
	require.Contains(t, replies[2], "a budget request needs amount=<tokens>")
	require.Empty(t, fs.grantsMade)
	// With the amount, the budget grant carries it.
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000114", "U2", "approve "+reqBudget+" amount=100000")))
	require.Len(t, fs.grantsMade, 1)
	require.Equal(t, int64(100000), *fs.grantsMade[0].Amount)
	// amount on a tool request is refused before the store sees it.
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000115", "U2", "approve "+reqA+" amount=5")))
	require.Contains(t, rep.all()[4], "amount= is for budget requests only")
	require.Len(t, fs.grantsMade, 1)
}

func TestInvalidCommandIsAnsweredNotAskedOfTheAgent(t *testing.T) {
	fs := newFakeStore()
	f, rep := newCommandFixture(t, fs)
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000116", "U2", "approve "+reqA+" ttl=soon")))
	require.Contains(t, rep.all()[0], "invalid: ttl:")
	require.Zero(t, f.a2a.count())
	require.Empty(t, fs.grantsMade)
}

func TestBotAuthoredCommandsAreIgnored(t *testing.T) {
	// The notification the plane itself posts carries the command text.
	// Were Slack to deliver it back as a bot-authored app_mention — or as
	// the plain `message` it actually is — the loop guard ignores it
	// before any command is parsed: nothing decided, nothing replied.
	fs := newFakeStore()
	f, rep := newCommandFixture(t, fs)
	text := strings.ReplaceAll(notify.Message(notify.Filing{ID: reqA, Credential: "hello-tools", Kind: "tool",
		Subject: "k8s_get_events", Detail: "denied"}), "\n", " ")
	text = strings.ReplaceAll(text, `"`, `\"`)
	for i, body := range []string{
		`{"type":"event_callback","event_id":"Ev0000000117","event":{"type":"app_mention","bot_id":"B1",` +
			`"text":"<@U1> approve ` + reqA + `","channel":"C0TEST","ts":"1725.0200"}}`,
		`{"type":"event_callback","event_id":"Ev0000000118","event":{"type":"message","bot_id":"B1",` +
			`"text":"` + text + `","channel":"C0TEST","ts":"1725.0201"}}`,
		`{"type":"event_callback","event_id":"Ev0000000119","event":{"type":"app_mention","subtype":"bot_message",` +
			`"text":"<@U1> ` + text + `","channel":"C0TEST","ts":"1725.0202"}}`,
	} {
		rec := f.post("slack-chan", body, f.slackSigned(body))
		require.Equal(t, http.StatusOK, rec.Code, "%d: %s", i, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"ignored"`)
	}
	require.Empty(t, fs.grantsMade)
	require.Equal(t, "pending", fs.find(reqA).Status)
	require.Empty(t, rep.all())
	require.Zero(t, f.a2a.count())
}

func TestCommandFromAnotherChannelIsRefused(t *testing.T) {
	// The channel allowlist runs BEFORE the command is recognised.
	fs := newFakeStore()
	f, _ := newCommandFixture(t, fs)
	body := `{"type":"event_callback","event_id":"Ev0000000120","event":{"type":"app_mention","user":"U2",` +
		`"text":"<@U1> approve ` + reqA + `","channel":"C0OTHER","ts":"1725.0300"}}`
	rec := f.post("slack-chan", body, f.slackSigned(body))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, fs.grantsMade)
}

func TestUnrecordedCommandIsNotAcknowledged(t *testing.T) {
	fs := newFakeStore()
	f, rep := newCommandFixture(t, fs)
	fs.setAuditErr(errors.New("pg down"))
	code, _ := f.mention("slack-chan", "Ev0000000121", "U2", "approve "+reqA)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Empty(t, rep.all(), "no row, no reply: Slack redelivers and the retry answers 'already decided'")
	// The decision itself stood (its own transaction); the retry says so.
	fs.setAuditErr(nil)
	f.post("demo", "x", nil) // clears the breaker
	require.Equal(t, http.StatusOK, must(f.mention("slack-chan", "Ev0000000121", "U2", "approve "+reqA)))
	require.Contains(t, rep.all()[0], "already approved by slack:U2")
}

func TestNonSlackHooksNeverParseCommands(t *testing.T) {
	fs := newFakeStore()
	f, _ := newCommandFixture(t, fs)
	rec := f.post("demo", `{"text":"approve `+reqA+`"}`, bearer("d-cmd"))
	require.Equal(t, http.StatusAccepted, rec.Code, "a generic webhook's text is a prompt, never a command")
	f.waitOutcome(t, "demo")
	require.Empty(t, fs.grantsMade)
}

func must(code int, _ string) int { return code }
