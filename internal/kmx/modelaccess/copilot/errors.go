// Package copilot provides explicit sign-in and direct Chat Completions access.
// Credentials never form part of a modelaccess.Route or a returned error.
package copilot

import (
	"context"
	"errors"
	"fmt"
	"net"
)

var (
	ErrAuthRequired     = errors.New("copilot_auth_required: run kmx model login")
	ErrAuthRevoked      = errors.New("copilot_auth_revoked: set a fresh COPILOT_GITHUB_TOKEN; with a usable OS keychain, renew gh using gh auth login --hostname github.com or renew kmx using kmx model login")
	ErrSubscription     = errors.New("copilot_no_subscription: an active Copilot subscription is required")
	ErrPolicy           = errors.New("copilot_policy_denied: access refused by policy")
	ErrQuota            = errors.New("copilot_quota: quota or rate limit reached")
	ErrModelUnavailable = errors.New("copilot_model_unavailable: select an available Copilot Chat model explicitly")
	ErrMalformed        = errors.New("copilot_malformed: malformed or oversized data")
	ErrKeychain         = errors.New("copilot_keychain_unavailable: use an existing gh login or COPILOT_GITHUB_TOKEN at runtime; no token file fallback")
	ErrUnavailable      = errors.New("copilot_unavailable: request could not be completed")
)

// Preserve cancellation identity, but never retain an untrusted underlying error.
func safeError(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("copilot_cancelled: %w", context.Canceled)
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return fmt.Errorf("copilot_timeout: %w", context.DeadlineExceeded)
	}
	return ErrUnavailable
}
