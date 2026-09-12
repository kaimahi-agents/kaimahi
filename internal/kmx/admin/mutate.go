package admin

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var issuedTokenRe = regexp.MustCompile(`^kmh_[0-9a-f]{64}$`)

// The admin plane's MUTATIONS, and the argument validation in front of them.
//
// Every name and cap below is part of the command contract,
// as is every well-formed-positive status check. The plane validates all of it
// again — these checks exist because the values enter JSON and paths,
// and because a typo should fail before an admin port-forward is opened,
// not after.
//
// Custody is the package's, unchanged: the admin bearer never leaves this
// process, and no mutation follows a redirect.

// ValidCredentialName checks the public credential-name shape.
func ValidCredentialName(name string) error {
	if name == "" {
		return fmt.Errorf("a credential name is required (want [a-z0-9-]+)")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("invalid credential name %q (want [a-z0-9-]+)", name)
		}
	}
	return nil
}

// ParseCap reads a cap-shaped argument: "-" or "" means "no cap" (JSON
// null), anything else must be a non-negative integer.
func ParseCap(what, value string) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return nil, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || strings.ContainsAny(value, "+-") {
		return nil, fmt.Errorf("invalid %s %q (want a non-negative integer or -)", what, value)
	}
	if err := CheckCap(what, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// CheckCap preserves zero budgets and rejects negative caps.
func CheckCap(what string, value *int64) error {
	if value == nil {
		return nil
	}
	min, max := int64(0), int64(math.MaxInt64)
	if *value < min || *value > max {
		return fmt.Errorf("%s must be between %d and %d", what, min, max)
	}
	return nil
}

// ParseTTL reads a credential's TTL with the command's suffixes — a bare
// number is seconds, s/m/h/d scale it — and returns seconds.
func ParseTTL(value string) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return nil, nil
	}
	bad := fmt.Errorf("invalid TTL %q (want e.g. 90, 90s, 5m, 2h, 1d)", value)
	digits, unit := value, ""
	if last := value[len(value)-1]; last == 's' || last == 'm' || last == 'h' || last == 'd' {
		digits, unit = value[:len(value)-1], string(last)
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || strings.ContainsAny(digits, "+-") {
		return nil, bad
	}
	multiplier := int64(1)
	switch unit {
	case "", "s":
	case "m":
		multiplier = 60
	case "h":
		multiplier = 3600
	case "d":
		multiplier = 86400
	default:
		return nil, bad
	}
	if n > math.MaxInt64/multiplier {
		return nil, bad
	}
	n *= multiplier
	return &n, nil
}

// CheckCredentialTTL matches the issuance and renewal API's lifetime range.
func CheckCredentialTTL(ttl *int64) error {
	if ttl != nil && (*ttl < 60 || *ttl > 31536000) {
		return fmt.Errorf("ttl_seconds must be between 60 and 31536000 (a credential with no expiry cannot be issued)")
	}
	return nil
}

// SetBudget replaces a credential's monthly caps. A nil cap is "no cap".
func (c *Client) SetBudget(credential string, capCents, capTokens *int64) error {
	if err := ValidCredentialName(credential); err != nil {
		return err
	}
	if err := CheckCap("cap_cents", capCents); err != nil {
		return err
	}
	if err := CheckCap("cap_tokens", capTokens); err != nil {
		return err
	}
	body := map[string]any{"credential": credential, "cap_cents": capCents, "cap_tokens": capTokens}
	return c.expect(http.MethodPut, "/admin/budgets", body, http.StatusNoContent, "budget set")
}

// RenewCredential extends a credential's deadline and returns the new
// one. No token is minted, sent or read: renewal moves a date, and the
// credential bytes stay where they are.
func (c *Client) RenewCredential(credential string, ttlSeconds *int64) (string, error) {
	if err := ValidCredentialName(credential); err != nil {
		return "", err
	}
	if err := CheckCredentialTTL(ttlSeconds); err != nil {
		return "", err
	}
	body := map[string]any{}
	if ttlSeconds != nil {
		body["ttl_seconds"] = *ttlSeconds
	}
	status, out, err := c.Do(http.MethodPost, "/admin/credentials/"+credential+"/renew", body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("credential renew failed (HTTP %d): %s", status, strings.TrimSpace(string(out)))
	}
	doc, err := decode(out)
	if err != nil {
		return "", err
	}
	return str(doc["expires_at"]), nil
}

// IssueIdentityCredential mints an identity-only credential. A 409 is
// idempotent success. On 201 the one-time bearer is checked and immediately
// discarded; it is never returned to a caller that could print or store it.
func (c *Client) IssueIdentityCredential(credential string, ttlSeconds *int64) (bool, error) {
	if err := ValidCredentialName(credential); err != nil {
		return false, err
	}
	body := map[string]any{"name": credential}
	if ttlSeconds != nil {
		body["ttl_seconds"] = *ttlSeconds
	}
	status, out, err := c.Do(http.MethodPost, "/admin/credentials", body)
	if err != nil {
		return false, err
	}
	if status == http.StatusConflict {
		return false, nil
	}
	if status != http.StatusCreated {
		// Unlike ordinary admin errors, do not quote this response. This is
		// the one endpoint that can contain a bearer, and custody wins even
		// if a broken plane returns it with the wrong status.
		return false, fmt.Errorf("credential issue failed (HTTP %d)", status)
	}
	token, err := TokenFrom(out)
	if err != nil {
		return false, err
	}
	if !issuedTokenRe.MatchString(token) {
		return false, fmt.Errorf("the plane issued the credential but returned an invalid Kaimahi token")
	}
	return true, nil
}

// expect performs a mutation and refuses anything but the well-formed
// positive, quoting the body.
func (c *Client) expect(method, path string, body any, want int, what string) error {
	status, out, err := c.Do(method, path, body)
	if err != nil {
		return err
	}
	if status != want {
		return fmt.Errorf("%s failed (HTTP %d): %s", what, status, strings.TrimSpace(string(out)))
	}
	return nil
}
