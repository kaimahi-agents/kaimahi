// Package meter enforces budget caps at the proxy, before every upstream
// call. Adapted from tomte-old's meter: the fail-closed contract (no spend
// visibility, no spend), the calendar-month UTC window, and the 403/429
// split survive; identity moved from tenant/run to the Kaimahi-issued
// credential, and token caps joined cents caps (the free ollama tier costs
// $0 by classification, so only a token cap can ever exhaust there).
//
// The decision itself lives in the store. Reserve is one
// transaction under the credential's row lock that counts the ledger
// plus the calls already in flight and leaves a
// reservation the ledger write consumes — exact across replicas. This
// package keeps the policy edges: what a call holds, the month window,
// and how a verdict maps onto the wire.
package meter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// Denial is a typed refusal the proxy maps onto the HTTP response.
// 429 = budget reached; 403 = metering unavailable (fail closed).
type Denial struct {
	Status int
	Msg    string
}

func (d Denial) Error() string { return d.Msg }

// Store is what the meter needs from Postgres. *store.Store satisfies it.
type Store interface {
	// AdmitSpend is the locked, exact decision (store/spend.go).
	AdmitSpend(ctx context.Context, credential string, hold store.SpendHold, monthStart time.Time, ttl time.Duration) (store.Admission, error)
}

// Reservation is what an admitted call carries to its ledger write. ID
// is empty when the credential has no caps (nothing was held).
type Reservation struct {
	ID string
}

// DefaultHoldTTL bounds a reservation a crashed replica never consumed.
// Longer than any call the proxy allows (the upstream client's 5-minute
// timeout plus the ledger write's), so a live call never loses its hold;
// after it, the hold stops counting and the next admission sweeps it.
const DefaultHoldTTL = 10 * time.Minute

type Meter struct {
	Store   Store
	Now     func() time.Time // nil = time.Now
	HoldTTL time.Duration    // zero = DefaultHoldTTL
}

func (m *Meter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Meter) holdTTL() time.Duration {
	if m.HoldTTL > 0 {
		return m.HoldTTL
	}
	return DefaultHoldTTL
}

// MonthStartUTC is ported verbatim from tomte-old.
func MonthStartUTC(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Hold is what one admitted call commits until its ledger row lands:
// the LEAST it can spend, never an estimate. Every call that reaches an
// upstream spends at least one token; it spends at least one cent only
// when the model is priced (cents cost on a free or unpriced upstream
// is zero by classification, and a zero hold is honest there).
func Hold(priced bool) store.SpendHold {
	h := store.SpendHold{Tokens: 1}
	if priced {
		h.Cents = 1
	}
	return h
}

// Reserve admits or denies one call, exactly, and — when admitted under
// a cap — reserves its hold until RecordLedger consumes it. Fail closed:
// a store error, or a credential the lock cannot find, denies. The
// credential passed in only names the row; caps are read from the
// locked row itself, so a `make budget` lands on the very next call.
func (m *Meter) Reserve(ctx context.Context, cred store.Credential, priced bool) (Reservation, error) {
	a, err := m.Store.AdmitSpend(ctx, cred.Name, Hold(priced), MonthStartUTC(m.now()), m.holdTTL())
	if errors.Is(err, store.ErrNotFound) {
		slog.Error("meter: credential vanished before admission, denying", "credential", cred.Name)
		return Reservation{}, Denial{Status: http.StatusForbidden, Msg: "metering unavailable"}
	}
	if err != nil {
		slog.Error("meter: spend admission failed, denying request", "credential", cred.Name, "err", err)
		return Reservation{}, Denial{Status: http.StatusForbidden, Msg: "metering unavailable"}
	}
	if a.Denied {
		return Reservation{}, capDenial(a.Subject)
	}
	return Reservation{ID: a.ReservationID}, nil
}

func capDenial(subject string) Denial {
	msg := "monthly budget reached"
	if subject == "tokens" {
		msg = "monthly token budget reached"
	}
	return Denial{Status: http.StatusTooManyRequests, Msg: msg}
}
