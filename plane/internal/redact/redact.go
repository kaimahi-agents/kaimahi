// Package redact scrubs known secret values from strings before logging.
package redact

import (
	"slices"
	"strings"
)

const placeholder = "[REDACTED]"

// Redactor replaces known secret values with a fixed placeholder.
//
// Callers must funnel any string through Redact before it reaches a log sink;
// the redactor cannot intercept logs it never sees. The proxy builds one at
// boot from every secret it holds — the Postgres password, the admin token,
// each tool upstream's credential file and each inbound hook's signing
// secret — and installs it as the process-wide slog handler: one Redactor
// per run, one log-formatting choke point.
type Redactor struct {
	values []string
}

// New returns a Redactor that scrubs the given secret values. Empty strings
// are ignored. Values are sorted longest-first so that overlapping secrets
// redact to their most specific match.
func New(secrets []string) *Redactor {
	filtered := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	slices.SortFunc(filtered, func(a, b string) int {
		return len(b) - len(a)
	})
	return &Redactor{values: filtered}
}

// Redact returns s with every known secret value replaced by "[REDACTED]".
func (r *Redactor) Redact(s string) string {
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, placeholder)
	}
	return s
}
