// Package copilot provides runtime gh credentials, optional explicit sign-in,
// and direct Chat Completions or Responses access.
// Credentials never form part of a modelaccess.Route or a returned error.
package copilot

import (
	"context"
	"errors"
	"fmt"
	"net"
)

var (
	ErrAuthRequired      = errors.New("copilot_auth_required: sign in with gh or run kmx model login")
	ErrAuthRevoked       = errors.New("copilot_auth_revoked: renew gh using gh auth login --hostname github.com or set a fresh COPILOT_GITHUB_TOKEN; without gh, run kmx model login with a usable OS keychain")
	ErrSubscription      = errors.New("copilot_no_subscription: an active Copilot subscription is required")
	ErrPolicy            = errors.New("copilot_policy_denied: access refused by policy")
	ErrQuota             = errors.New("copilot_quota: quota or rate limit reached")
	ErrModelUnavailable  = errors.New("copilot_model_unavailable: select an available Copilot model explicitly")
	ErrToolsUnsupported  = errors.New("copilot_tools_unsupported: selected model does not support tool calls")
	ErrMalformed         = errors.New("copilot_malformed: malformed or oversized data")
	ErrKeychain          = errors.New("copilot_keychain_unavailable: use an existing gh login or COPILOT_GITHUB_TOKEN at runtime; no token file fallback")
	ErrUnavailable       = errors.New("copilot_unavailable: request could not be completed")
	ErrCredentialContent = errors.New("copilot_credential_content: credential-bearing content refused")
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
