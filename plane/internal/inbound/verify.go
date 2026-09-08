package inbound

// Inbound authentication: how a caller proves it is the hook's bound
// credential. Three modes (config.Auth*), two of them signatures.
// Signature verification is preferred over a bearer wherever the source
// can sign: the secret never travels, the body is bound to the proof,
// and the signed timestamp (plus the signed delivery id in Kaimahi's own
// scheme) is what makes replay protection more than a database index.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

const (
	// Kaimahi's generic signed-webhook scheme (config.AuthKaimahiHMAC):
	//   X-Kaimahi-Delivery:  the source's unique delivery id
	//   X-Kaimahi-Timestamp: unix seconds at signing time
	//   X-Kaimahi-Signature: v1=<hex hmac-sha256 over "v1:<ts>:<delivery>:<body>">
	// A bearer hook (config.AuthBearer) sends only the delivery header.
	HeaderDelivery  = "X-Kaimahi-Delivery"
	HeaderTimestamp = "X-Kaimahi-Timestamp"
	HeaderSignature = "X-Kaimahi-Signature"
	kaimahiVersion  = "v1"

	// Slack request signing (config.AuthSlack): v0=<hex hmac-sha256 over
	// "v0:<ts>:<body>">; the delivery id is the signed body's event_id.
	slackTimestamp = "X-Slack-Request-Timestamp"
	slackSignature = "X-Slack-Signature"
	slackVersion   = "v0"

	// replayWindow bounds how old a signed timestamp may be (Slack's own
	// recommendation is five minutes). Beyond it the signature is stale
	// and refused even if it verifies.
	replayWindow = 5 * time.Minute
)

// deliveryRe bounds a delivery id: it lands in an audit row and a unique
// index, so keep it a plain identifier of sane length.
var deliveryRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

var errSecretUnavailable = errors.New("signing secret unavailable")

// readSecret reads a hook's signing secret per request from plane-side
// custody (a Secret-mounted file), so rotation needs no restart. Missing
// or empty fails closed — the caller answers 503, never "unsigned".
func readSecret(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errSecretUnavailable
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return nil, errSecretUnavailable
	}
	return []byte(secret), nil
}

var errChannelsUnusable = errors.New("channel allowlist unusable")

// readSlackChannels reads a hook's channel allowlist per request from
// plane-side custody: channel IDs separated by commas or newlines. The
// file is the same Secret key that restricts the Slack MCP server's
// posting (SLACK_MCP_ADD_MESSAGE_TOOL), whose grammar also admits "true"
// (post anywhere) and "!C…" (everywhere but) — neither is a list of
// rooms this hook may be triggered from, so both fail closed here rather
// than widen an ingress to the whole workspace.
func readSlackChannels(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(string(raw), func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' }) {
		if !slackIDRe.MatchString(f) {
			return nil, errChannelsUnusable
		}
		out[f] = true
	}
	if len(out) == 0 {
		return nil, errChannelsUnusable
	}
	return out, nil
}

// verifySignature checks "<version>=<hex>" against HMAC-SHA256(secret,
// base) in constant time.
func verifySignature(secret []byte, header, version string, base []byte) bool {
	sigHex, ok := strings.CutPrefix(header, version+"=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(base)
	return hmac.Equal(got, mac.Sum(nil))
}

// freshTimestamp parses a signed unix-seconds timestamp and requires it
// to be within replayWindow of now (either direction — clock skew is
// symmetric).
func freshTimestamp(ts string, now time.Time) bool {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	d := now.Sub(time.Unix(n, 0))
	return d <= replayWindow && d >= -replayWindow
}

func kaimahiBase(ts, delivery string, body []byte) []byte {
	return []byte(kaimahiVersion + ":" + ts + ":" + delivery + ":" + string(body))
}

func slackBase(ts string, body []byte) []byte {
	return []byte(slackVersion + ":" + ts + ":" + string(body))
}

// Sign produces Kaimahi's v1 signature for a request, over the same base
// string verify uses, so a Go caller and the plane cannot disagree about
// what was signed.
//
// Its callers today are this package's tests. scripts/inbound-probe.sh does
// NOT use it: the probe recomputes the HMAC in Python, so the scheme has a
// second implementation, and docs/inbound.md states it a third time in
// prose. Nothing holds those three to each other. That is worth knowing
// before anyone changes the base string.
func Sign(secret []byte, ts, delivery string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(kaimahiBase(ts, delivery, body))
	return kaimahiVersion + "=" + hex.EncodeToString(mac.Sum(nil))
}

// event is what the bridge extracts from a verified payload: the text
// the agent is asked, and (for sources that carry it in the body) the
// delivery id. challenge is Slack's URL-verification handshake — a
// payload that must be echoed, never acted on. ignored names why a
// well-formed event is acknowledged but deliberately not a trigger.
type event struct {
	text      string
	delivery  string
	challenge string
	ignored   string
	slack     slackMention
}

// slackMention is the part of an app_mention the reply needs: where it
// was said, who said it, which thread the answer belongs in, and the
// words themselves (mention tokens stripped) for the command parser.
type slackMention struct {
	channel  string
	user     string
	threadTS string
	text     string
}

// genericEvent takes the text from a JSON object's "text" field when
// there is one, otherwise the body itself as text. Anything that is not
// UTF-8 text is refused: a webhook payload becomes a prompt, and a
// prompt is text.
func genericEvent(body []byte) (event, bool) {
	if !utf8.Valid(body) {
		return event{}, false
	}
	var m struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(body, &m) == nil && m.Text != nil {
		return event{text: *m.Text}, *m.Text != ""
	}
	text := strings.TrimSpace(string(body))
	return event{text: text}, text != ""
}

// slackIDRe bounds a Slack channel/user id and a message ts: each lands
// in the prompt and in an audit row, so keep them plain identifiers.
var (
	slackIDRe = regexp.MustCompile(`^[A-Z0-9]{1,64}$`)
	slackTSRe = regexp.MustCompile(`^[0-9]{1,20}\.[0-9]{1,10}$`)
	mentionRe = regexp.MustCompile(`<@[A-Z0-9]+(\|[^>]*)?>`)
)

// slackEvent understands the two Events API envelope shapes that matter:
// url_verification (answer the challenge) and event_callback (keyed by
// the envelope's event_id). Anything else is refused: Slack sends
// nothing else to an events URL, and an unknown shape must not become a
// prompt.
//
// Within event_callback, ONLY an app_mention by a human triggers. Every
// other inner event a subscription might deliver — a plain message
// (which is what the bot's own reply is, once it lands in the channel),
// anything authored by a bot, an empty mention — is acknowledged as
// ignored: Slack gets its 2xx, the audit trail gets a row, and no agent
// runs. That is the loop guard and the "exactly one trigger" rule in
// one place. A malformed app_mention (no channel, ts or event_id) is a
// refusal (ok=false), not an ignore: the plane cannot even say what it
// is declining.
func slackEvent(body []byte) (event, bool) {
	var env struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		EventID   string `json:"event_id"`
		Event     struct {
			Type     string `json:"type"`
			Subtype  string `json:"subtype"`
			BotID    string `json:"bot_id"`
			User     string `json:"user"`
			Text     string `json:"text"`
			Channel  string `json:"channel"`
			TS       string `json:"ts"`
			ThreadTS string `json:"thread_ts"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return event{}, false
	}
	switch env.Type {
	case "url_verification":
		return event{challenge: env.Challenge}, env.Challenge != ""
	case "event_callback":
		if !deliveryRe.MatchString(env.EventID) {
			return event{}, false
		}
		ev := event{delivery: env.EventID}
		e := env.Event
		switch {
		case e.Type != "app_mention":
			ev.ignored = "event type " + e.Type + " is not a trigger (app_mention only)"
			return ev, true
		case e.BotID != "" || e.Subtype == "bot_message":
			ev.ignored = "bot-authored mention (loop guard)"
			return ev, true
		}
		if !slackIDRe.MatchString(e.Channel) || !slackTSRe.MatchString(e.TS) ||
			(e.ThreadTS != "" && !slackTSRe.MatchString(e.ThreadTS)) ||
			(e.User != "" && !slackIDRe.MatchString(e.User)) {
			return event{}, false
		}
		text := stripMentions(e.Text)
		if text == "" {
			ev.ignored = "mention carries no text"
			return ev, true
		}
		thread := e.ThreadTS
		if thread == "" {
			// A top-level mention starts its own thread: the reply goes
			// under the message that asked, not loose in the channel.
			thread = e.TS
		}
		ev.slack = slackMention{channel: e.Channel, user: e.User, threadTS: thread, text: text}
		ev.text = slackTask(text, ev.slack)
		return ev, true
	}
	return event{}, false
}

// stripMentions removes <@Uxxx> and <@Uxxx|name> tokens — the bot's own
// mention, first of all — and collapses the whitespace they leave.
func stripMentions(text string) string {
	return strings.Join(strings.Fields(mentionRe.ReplaceAllString(text, " ")), " ")
}

// slackTask is the event → agent-task mapping for a mention: the user's
// words are QUOTED as data (a Slack message is untrusted input, not an
// instruction to the plane), and the reply's destination is named once
// in exactly the shape the posting tool takes, so the agent has nothing
// to guess and nothing to rewrite. Which tools the agent may call is
// not decided here: posting is admitted by the gateway's allowlist plus
// a live grant, or it is not.
func slackTask(text string, m slackMention) string {
	who := "someone"
	if m.user != "" {
		who = "user " + m.user
	}
	return "A Slack message from " + who + " in channel " + m.channel + " mentioned you. Their message was: " +
		strconv.Quote(text) + "\n\nAnswer it in one short paragraph, then post that answer to Slack by calling " +
		"conversations_add_message with channel_id " + strconv.Quote(m.channel) +
		", thread_ts " + strconv.Quote(m.threadTS) + ", and your answer as the payload."
}

// actorOf names the PERSON an authenticated event was sent by, in the
// approvals vocabulary (migration 00006's `decided_by`: free text
// prefixed by the path that vouched for it) — reused rather than
// reinvented, so an approver and a requester are named the same way.
//
// A Slack app_mention names the user who typed it, and the signature
// the bridge just verified is what vouches for that claim. Every other
// hook shape — a bearer webhook, a Kaimahi-signed webhook — names a
// sender the plane authenticated but no HUMAN, so it is 'none': a
// complete answer, not a gap. The user id and nothing else is taken; a
// name, an email or a profile would be PII this plane has no reason to
// hold and every backup would carry.
func actorOf(h config.InboundHook, ev event) string {
	if h.Auth == config.AuthSlack && ev.slack.user != "" {
		return "slack:" + ev.slack.user
	}
	return store.ActedForNone
}
