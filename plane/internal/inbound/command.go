package inbound

// Approval commands from Slack. A human mentions the bot with
// `approve <id> [uses=N] [ttl=D] [amount=N]` or `deny <id>` and the
// plane decides the request — the second verb on the boundary that
// already exists (the slack-events hook), recognised AFTER Slack's
// signature and the channel allowlist have been checked and BEFORE the
// grant gate: a command needs no inbound grant (or approving would need
// an approval), invokes no agent, and spends nothing. Who may decide is
// the approver file, not the room.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// command is a parsed approval command. err is set when the text is
// recognisably a command with a bad argument: still a command (answered
// as "invalid …"), never handed to the agent as a question.
type command struct {
	verb   string // "approve" | "deny"
	prefix string // request id or a prefix of it, lowercase
	uses   *int32
	ttl    *time.Duration
	amount *int64
	err    error
}

const (
	// minPrefix is the shortest id prefix accepted: the first block of
	// the uuid, which is what a human copies from the notification or
	// `make approvals`. Uniqueness is checked in the store regardless.
	minPrefix = 8
	// Same ranges the admin surface applies.
	maxUses   = 1_000_000
	maxAmount = 1_000_000_000_000
)

var (
	prefixRe = regexp.MustCompile(`^[0-9a-f-]{1,36}$`)
	// approverIDRe bounds a Slack user id in the approver file: U… or
	// (Enterprise Grid) W…, plain uppercase alphanumerics.
	approverIDRe = regexp.MustCompile(`^[UW][A-Z0-9]{1,63}$`)
)

// parseCommand recognises a command in a mention's text (mention tokens
// already stripped). ok=false means "not a command" — the mention keeps
// today's behaviour and goes to the agent.
func parseCommand(text string) (c command, ok bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return command{}, false
	}
	verb := strings.ToLower(fields[0])
	if verb != "approve" && verb != "deny" {
		return command{}, false
	}
	c.verb = verb
	if len(fields) < 2 {
		c.err = fmt.Errorf("usage: %s <request id> [uses=N] [ttl=15m] [amount=N]", verb)
		return c, true
	}
	c.prefix = strings.ToLower(strings.Trim(fields[1], "`<>"))
	if !prefixRe.MatchString(c.prefix) || len(strings.ReplaceAll(c.prefix, "-", "")) < minPrefix {
		c.err = fmt.Errorf("request id must be at least the first %d characters of the id shown in the notification or `make approvals`", minPrefix)
		return c, true
	}
	for _, f := range fields[2:] {
		k, v, found := strings.Cut(strings.ToLower(f), "=")
		if !found || v == "" {
			c.err = fmt.Errorf("unrecognised argument %q (want uses=N, ttl=D, amount=N)", f)
			return c, true
		}
		switch k {
		case "uses":
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil || n < 1 || n > maxUses {
				c.err = fmt.Errorf("uses must be an integer in [1, %d]", maxUses)
				return c, true
			}
			u := int32(n)
			c.uses = &u
		case "ttl":
			d, err := config.ParseTTL(v)
			if err != nil {
				c.err = fmt.Errorf("ttl: %v", err)
				return c, true
			}
			c.ttl = &d
		case "amount":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 || n > maxAmount {
				c.err = fmt.Errorf("amount must be an integer in [1, %d]", maxAmount)
				return c, true
			}
			c.amount = &n
		default:
			c.err = fmt.Errorf("unrecognised argument %q (want uses=N, ttl=D, amount=N)", f)
			return c, true
		}
	}
	if verb == "deny" && (c.uses != nil || c.ttl != nil || c.amount != nil) {
		c.err = errors.New("deny takes no bounds")
	}
	return c, true
}

var errApproversUnusable = errors.New("approver list unusable")

// readSlackApprovers reads a hook's approver list per request from plane
// custody: Slack user ids separated by commas, spaces or newlines. A
// malformed entry fails the whole list closed rather than admitting
// whatever parsed — a list that is half garbage is not a list of
// approvers. Empty fails closed too: nobody may approve.
func readSlackApprovers(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(string(raw), func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' }) {
		if !approverIDRe.MatchString(f) {
			return nil, errApproversUnusable
		}
		out[f] = true
	}
	if len(out) == 0 {
		return nil, errApproversUnusable
	}
	return out, nil
}

// handleCommand handles a recognised command. Its record is an inbound
// row with decision 'command' (the outcome in detail); the decision
// itself is in the approvals trail with the approver's identity; the
// reply goes to the thread through the governed posting path.
func (b *Bridge) handleCommand(w http.ResponseWriter, r *http.Request, name string, h config.InboundHook,
	delivery string, cred store.Credential, ev event, c command) {
	// Who may decide: the approver file, read per request; unreadable or
	// empty refuses the command (503) and nothing else — a question
	// asked a moment later is still answered.
	approvers, err := readSlackApprovers(h.SlackApproversFile)
	if err != nil {
		slog.Error("inbound: slack approver list unavailable", "hook", name, "file", h.SlackApproversFile, "err", err)
		b.deny(w, r, name, h, delivery, http.StatusServiceUnavailable, "hook approver list unavailable")
		return
	}
	if ev.slack.user == "" || !approvers[ev.slack.user] {
		// Channel membership is not authority. Audited, not
		// replied: the trail names who tried; the room is not told.
		b.deny(w, r, name, h, delivery, http.StatusForbidden,
			"user "+ev.slack.user+" is not an approver on this hook (command: "+c.verb+")")
		return
	}
	who := "slack:" + ev.slack.user

	// Cancel-free: Slack hangs up fast, and a decision must not be half
	// made because the request context went away mid-transaction.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	outcome := b.decide(ctx, h, c, who)
	detail := c.verb + " " + c.prefix + " by " + who + ": " + outcome

	// The row first, the acknowledgement second (the ignored/challenge
	// rule): an outcome the trail cannot record is not acknowledged, so
	// Slack redelivers and the retried command answers "already
	// decided" once the trail is back.
	if !b.audit(r.Context(), store.InboundAuditEntry{Hook: name, CredentialName: cred.Name,
		DeliveryID: delivery, Decision: "command", Status: http.StatusOK, Detail: detail, Agent: agentRef(h)}) {
		http.Error(w, "inbound audit unavailable", http.StatusServiceUnavailable)
		return
	}
	if b.d.Replier != nil {
		b.d.Replier.Reply(outcome, ev.slack.threadTS)
	} else {
		slog.Warn("inbound: no replier configured; command outcome not posted back", "outcome", outcome)
	}
	writeJSON(w, map[string]string{"status": "command", "outcome": outcome})
}

// decide resolves the request and applies the verb, returning the
// human-readable outcome (what is replied and audited).
func (b *Bridge) decide(ctx context.Context, h config.InboundHook, c command, who string) string {
	if c.err != nil {
		return "invalid: " + c.err.Error()
	}
	req, err := b.d.Store.RequestByPrefix(ctx, c.prefix)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "no approval request starts with " + c.prefix
	case errors.Is(err, store.ErrAmbiguous):
		return "more than one request starts with " + c.prefix + "; use a longer prefix"
	case err != nil:
		slog.Error("inbound: request lookup failed", "prefix", c.prefix, "err", err)
		return "the plane could not look up the request; try again"
	}
	if req.Status != "pending" {
		// Immutable once decided: report, never re-decide.
		return alreadyDecided(req)
	}
	if c.verb == "deny" {
		err := b.d.Store.DenyApprovalRequest(ctx, req.ID, who)
		if errors.Is(err, store.ErrNotPending) {
			return b.raced(ctx, req)
		}
		if err != nil {
			slog.Error("inbound: deny failed", "request", req.ID, "err", err)
			return "the plane could not record the denial; try again"
		}
		return "denied request " + req.ID + " (" + req.CredentialName + " " + req.Kind + "/" + req.Subject + ")"
	}
	// Bounds: explicit wins; otherwise the hook's defaults (uses and ttl
	// both — a chat-typed approval gets the tightest useful grant).
	uses, ttl := c.uses, c.ttl
	if uses == nil && ttl == nil {
		u := int32(h.SlackDefaultUses)
		d, err := config.ParseTTL(h.SlackDefaultTTL)
		if err != nil { // config.Parse validated it; belt and braces
			return "the hook's default ttl is invalid; give ttl= explicitly"
		}
		uses, ttl = &u, &d
	}
	var expiresAt *time.Time
	if ttl != nil {
		t := b.d.Now().Add(*ttl)
		expiresAt = &t
	}
	if req.Kind == "budget" && c.amount == nil {
		return "a budget request needs amount=<" + req.Subject + "> (how much to raise the cap by)"
	}
	if req.Kind != "budget" && c.amount != nil {
		return "amount= is for budget requests only"
	}
	g, err := b.d.Store.ApproveRequest(ctx, req.ID, expiresAt, uses, c.amount, who)
	switch {
	case errors.Is(err, store.ErrNotPending):
		return b.raced(ctx, req)
	case errors.Is(err, store.ErrBounds):
		return "invalid bounds: " + err.Error()
	case err != nil:
		slog.Error("inbound: approve failed", "request", req.ID, "err", err)
		return "the plane could not record the approval; try again"
	}
	bounds := ""
	if g.MaxUses != nil {
		bounds += fmt.Sprintf(" uses=%d", *g.MaxUses)
	}
	if g.ExpiresAt != nil {
		bounds += " expires=" + g.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if g.Amount != nil {
		bounds += fmt.Sprintf(" amount=%d", *g.Amount)
	}
	return "approved request " + req.ID + " (" + req.CredentialName + " " + req.Kind + "/" + req.Subject +
		"): grant " + g.ID + bounds
}

// raced re-reads a request another decision beat this one to.
func (b *Bridge) raced(ctx context.Context, req store.ApprovalRequest) string {
	if fresh, err := b.d.Store.RequestByPrefix(ctx, req.ID); err == nil {
		return alreadyDecided(fresh)
	}
	return "request " + req.ID + " was already decided"
}

func alreadyDecided(req store.ApprovalRequest) string {
	when := ""
	if req.DecidedAt != nil {
		when = " at " + req.DecidedAt.UTC().Format(time.RFC3339)
	}
	by := req.DecidedBy
	if by == "" {
		by = "unknown"
	}
	return "request " + req.ID + " was already " + req.Status + " by " + by + when + "; a decided request is immutable"
}
