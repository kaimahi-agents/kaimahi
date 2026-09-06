package notify

import (
	"context"
	"log/slog"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// Filing is one freshly filed approval request, as the notifier names
// it to a human. Summary is the transaction line a tool request
// carries — "payment_schedule: amount_cents 3255000, payee_id MER-4471"
// — so an approver sees WHAT they are approving, not just which verb.
type Filing struct {
	ID, Credential, Kind, Subject, Detail string
	Summary                               string
}

// Notifier is told about fresh filings. *Poster satisfies it via
// Notify; Store below depends on the interface, not the Poster.
type Notifier interface {
	Notify(f Filing)
}

// Notify posts the "a request is waiting" message: the request id (what
// the approver types), whose credential, what kind and subject, and the
// command. Once per filing — the store dedupes pending requests, and
// Store below only calls this for a filing that was actually fresh.
func (p *Poster) Notify(f Filing) {
	p.Enqueue(Post{Kind: "approval-request", Text: Message(f)})
}

// Reply posts a command outcome in-thread (inbound.Replier).
func (p *Poster) Reply(text, threadTS string) {
	p.Enqueue(Post{Kind: "command-reply", Text: text, ThreadTS: threadTS})
}

// Message is the notification text. The bot is named in plain text, not
// as a mention token: the plane does not know the bot's user id, and
// the human types the mention. Were the text to arrive back as a
// bot-authored app_mention it would be ignored by the loop guard
// anyway (proved in the inbound tests).
func Message(f Filing) string {
	amount := ""
	if f.Kind == "budget" {
		amount = " amount=<" + f.Subject + ">"
	}
	// The call itself, where there is one: an approver who cannot see the
	// transaction is the problem this restates.
	what := "`" + f.Subject + "`"
	if f.Summary != "" {
		what = "`" + f.Summary + "`"
	}
	return "Kaimahi approval request `" + f.ID + "`: credential `" + f.Credential +
		"` was denied " + f.Kind + " " + what + " (" + f.Detail + ").\n" +
		"To decide, mention the bot: `@kaimahi approve " + f.ID + " [uses=N] [ttl=15m]" + amount +
		"` or `@kaimahi deny " + f.ID + "`. Or run `make approvals`."
}

// Filer is the one filing function with the id (store.Store.FileRequest).
type Filer interface {
	FileRequest(ctx context.Context, f store.Filing) (id string, filed bool, err error)
}

// Store wraps the plane's store so that the ONE filing function every
// filing site reaches (the gateway's tool denial, the meter's budget
// denial, the inbound door, the admin's explicit filing) notifies once
// per fresh filing. Deduped refilings (filed=false) notify nothing; a
// failed filing notifies nothing and is the caller's denial regardless.
// Every other method is the embedded store's own; Filer is where
// filings go (the same store in main; a fake without a database in
// tests). With N nil it is the store.
type Store struct {
	*store.Store
	Filer Filer
	N     Notifier
}

func (s Store) FileApprovalRequest(ctx context.Context, f store.Filing) (bool, error) {
	id, filed, err := s.Filer.FileRequest(ctx, f)
	if err != nil || !filed {
		return filed, err
	}
	if s.N != nil {
		slog.Info("notify: approval request filed; notifying", "request", id,
			"credential", f.Credential, "kind", f.Kind, "subject", f.Subject)
		s.N.Notify(Filing{ID: id, Credential: f.Credential, Kind: f.Kind,
			Subject: f.Subject, Detail: f.Detail, Summary: f.ArgSummary})
	}
	return true, nil
}
