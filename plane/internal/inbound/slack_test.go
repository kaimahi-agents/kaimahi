package inbound

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

// The Slack Events API loop: what an app_mention becomes, what is
// deliberately NOT a trigger, and how the bridge answers Slack so its
// retry policy neither re-fires an event nor disables the subscription.

const mentionInAllowed = `{"type":"event_callback","event_id":"Ev0000000001","event":{"type":"app_mention",` +
	`"user":"U2","text":"<@U1> what is the weather?","channel":"C0TEST","ts":"1725.0001"}}`

func slackHooks(t *testing.T, h map[string]config.InboundHook) map[string]config.InboundHook {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "channel"), []byte("C0TEST\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "all"), []byte("true"), 0o600))
	base := h["slack"]
	base.SlackChannelsFile = filepath.Join(dir, "channel")
	h["slack-chan"] = base
	base.SlackChannelsFile = filepath.Join(dir, "missing")
	h["slack-nofile"] = base
	base.SlackChannelsFile = filepath.Join(dir, "all")
	h["slack-all"] = base
	return h
}

func newSlackFixture(t *testing.T, fs *fakeStore) *fixture {
	t.Helper()
	fs.grants["inbound-demo/slack-chan"] = 5
	fs.grants["inbound-demo/slack-nofile"] = 5
	fs.grants["inbound-demo/slack-all"] = 5
	return newFixture(t, fs, &fakeMeter{}, func(d *Deps) { d.Hooks = slackHooks(t, d.Hooks) })
}

func TestSlackMentionBecomesAThreadedReplyTask(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)
	rec := f.post("slack-chan", mentionInAllowed, f.slackSigned(mentionInAllowed))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	out := f.waitOutcome(t, "slack-chan")
	require.Equal(t, "completed", out.Decision)
	require.Equal(t, "Ev0000000001", out.DeliveryID)
	parts := f.a2a.bodies[0]["params"].(map[string]any)["message"].(map[string]any)["parts"].([]any)
	text := parts[0].(map[string]any)["text"].(string)
	// The mention token is stripped (the model should not echo it), the
	// channel and the thread the reply belongs in are named exactly once
	// each in a form the agent's tool takes verbatim, and the user's
	// words are quoted rather than pasted as instructions.
	require.NotContains(t, text, "<@U1>")
	require.Contains(t, text, `"what is the weather?"`)
	require.Contains(t, text, `channel_id "C0TEST"`)
	require.Contains(t, text, `thread_ts "1725.0001"`)
}

func TestSlackMentionInsideAThreadRepliesToThatThread(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)
	body := `{"type":"event_callback","event_id":"Ev0000000002","event":{"type":"app_mention",` +
		`"user":"U2","text":"<@U1> and tomorrow?","channel":"C0TEST","ts":"1725.0009","thread_ts":"1725.0001"}}`
	require.Equal(t, http.StatusAccepted, f.post("slack-chan", body, f.slackSigned(body)).Code)
	f.waitOutcome(t, "slack-chan")
	parts := f.a2a.bodies[0]["params"].(map[string]any)["message"].(map[string]any)["parts"].([]any)
	require.Contains(t, parts[0].(map[string]any)["text"].(string), `thread_ts "1725.0001"`)
}

func TestSlackOnlyAppMentionsTrigger(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)
	// A plain channel message — including the bot's OWN reply landing in
	// the channel, which is how a loop would start — is acknowledged
	// (Slack must not retry it) and audited, and triggers nothing.
	msg := `{"type":"event_callback","event_id":"Ev0000000003","event":{"type":"message",` +
		`"user":"U2","text":"hello","channel":"C0TEST","ts":"1725.0002"}}`
	rec := f.post("slack-chan", msg, f.slackSigned(msg))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"ignored"`)
	require.Zero(t, f.a2a.count())
	require.Equal(t, 5, fs.grants["inbound-demo/slack-chan"], "an ignored event burns no grant use")
	require.Equal(t, []string{"ignored 200"}, fs.decisions("slack-chan"))
	require.Equal(t, "Ev0000000003", fs.last("slack-chan").DeliveryID)
}

func TestSlackBotAuthoredMentionsAreIgnored(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)
	for _, body := range []string{
		`{"type":"event_callback","event_id":"Ev0000000004","event":{"type":"app_mention","bot_id":"B1",` +
			`"text":"<@U1> hi","channel":"C0TEST","ts":"1725.0003"}}`,
		`{"type":"event_callback","event_id":"Ev0000000005","event":{"type":"app_mention","subtype":"bot_message",` +
			`"text":"<@U1> hi","channel":"C0TEST","ts":"1725.0004"}}`,
		`{"type":"event_callback","event_id":"Ev0000000006","event":{"type":"app_mention","user":"U2",` +
			`"text":"<@U1>","channel":"C0TEST","ts":"1725.0005"}}`,
	} {
		rec := f.post("slack-chan", body, f.slackSigned(body))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"ignored"`)
	}
	require.Zero(t, f.a2a.count())
	require.Equal(t, 5, fs.grants["inbound-demo/slack-chan"])
}

func TestSlackChannelAllowlistIsEnforcedFailClosed(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)
	elsewhere := `{"type":"event_callback","event_id":"Ev0000000007","event":{"type":"app_mention",` +
		`"user":"U2","text":"<@U1> psst","channel":"C0OTHER","ts":"1725.0006"}}`
	rec := f.post("slack-chan", elsewhere, f.slackSigned(elsewhere))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Equal(t, "1", rec.Header().Get("X-Slack-No-Retry"), "a channel denial is final; Slack must not retry it")
	require.Zero(t, f.a2a.count())
	require.Equal(t, 5, fs.grants["inbound-demo/slack-chan"])
	require.Equal(t, []string{"denied 403"}, fs.decisions("slack-chan"))

	// Allowlist file absent: refuse rather than accept from anywhere.
	rec = f.post("slack-nofile", mentionInAllowed, f.slackSigned(mentionInAllowed))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Slack-No-Retry"), "an outage may be retried")

	// A server-side "post anywhere" value is not a channel list here.
	rec = f.post("slack-all", mentionInAllowed, f.slackSigned(mentionInAllowed))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Zero(t, f.a2a.count())

	// config.Parse refuses a slack hook without an allowlist file; the
	// bridge itself, handed one anyway, admits from any channel — the
	// test fixture's "slack" hook is that shape, and this pins it so the
	// two layers are known to differ rather than assumed to agree.
	require.Equal(t, http.StatusAccepted, f.post("slack", elsewhere, f.slackSigned(elsewhere)).Code)
}

func TestSlackMalformedEventIsRefusedWithoutRetry(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)
	for _, body := range []string{
		`{"type":"event_callback","event_id":"Ev0000000008","event":{"type":"app_mention","user":"U2","text":"<@U1> hi","ts":"1"}}`,           // no channel
		`{"type":"event_callback","event_id":"Ev0000000009","event":{"type":"app_mention","user":"U2","text":"<@U1> hi","channel":"C0TEST"}}`, // no ts
		`{"type":"event_callback","event":{"type":"app_mention","user":"U2","text":"<@U1> hi","channel":"C0TEST","ts":"1"}}`,                  // no event_id
		`{"type":"event_callback","event_id":"Ev10","event":{"type":"app_mention","user":"U2","text":"<@U1> hi","channel":"C0 TEST","ts":"1"}}`,
	} {
		rec := f.post("slack-chan", body, f.slackSigned(body))
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Equal(t, "1", rec.Header().Get("X-Slack-No-Retry"))
	}
	require.Zero(t, f.a2a.count())
}

func TestSlackReplayAnswers409WithoutRetry(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)
	require.Equal(t, http.StatusAccepted, f.post("slack-chan", mentionInAllowed, f.slackSigned(mentionInAllowed)).Code)
	rec := f.post("slack-chan", mentionInAllowed, f.slackSigned(mentionInAllowed))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "1", rec.Header().Get("X-Slack-No-Retry"))
	// Non-Slack hooks never carry Slack's header.
	rec = f.post("demo", `{"text":"x"}`, bearer("d-replay"))
	require.Equal(t, http.StatusAccepted, rec.Code)
	rec = f.post("demo", `{"text":"x"}`, bearer("d-replay"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Empty(t, rec.Header().Get("X-Slack-No-Retry"))
}

func TestUnrecordedAcknowledgementIsWithheld(t *testing.T) {
	fs := newFakeStore()
	f := newSlackFixture(t, fs)
	fs.setAuditErr(errors.New("pg down"))
	// The FIRST failing write is the ignored event's own row: the 2xx
	// Slack would stop retrying on is withheld, and no retry header is
	// set, so the event comes back once the trail is writable.
	msg := `{"type":"event_callback","event_id":"Ev0000000011","event":{"type":"message",` +
		`"user":"U2","text":"hello","channel":"C0TEST","ts":"1725.0007"}}`
	rec := f.post("slack-chan", msg, f.slackSigned(msg))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Slack-No-Retry"))
	require.Empty(t, fs.decisions("slack-chan"), "nothing recorded, nothing acknowledged")
	// Same for the handshake: Slack's retry of it is what heals the row.
	fs.setAuditErr(nil)
	f.post("demo", "x", nil) // any recorded decision clears the breaker
	challenge := `{"type":"url_verification","challenge":"abc"}`
	require.Equal(t, http.StatusOK, f.post("slack-chan", challenge, f.slackSigned(challenge)).Code)
}

func TestStripMentions(t *testing.T) {
	require.Equal(t, "what is the weather?", stripMentions("<@U1> what is the weather?"))
	require.Equal(t, "hi there", stripMentions("<@U1|kaimahi> hi <@U9> there"))
	require.Equal(t, "", stripMentions("<@U1>"))
	require.Equal(t, "keep <#C1|general> and <https://x>", stripMentions("keep <#C1|general> and <https://x>"))
}
