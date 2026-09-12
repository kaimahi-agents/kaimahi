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
// Every name, cap, UUID, and tool shape below is part of the command contract,
// as is every well-formed-positive status check. The plane validates all of it
// again — these
// checks exist because these values are interpolated into JSON and paths,
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

// namePart is the shape for a tool name and for a request subject:
// [A-Za-z0-9._-]+.
func namePart(what, value string) error {
	if value == "" {
		return fmt.Errorf("invalid %s '' (want [A-Za-z0-9._-]+)", what)
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("invalid %s %q (want [A-Za-z0-9._-]+)", what, value)
		}
	}
	return nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidRequestID checks the UUID shape; the id comes
// off the approvals table, so the error says where to find one.
func ValidRequestID(id string) error {
	if !uuidRe.MatchString(id) {
		return fmt.Errorf("invalid request id %q (want a UUID from `kmx approvals`)", id)
	}
	return nil
}

// ParseCap reads a cap-shaped argument: "-" or "" means "no cap" (JSON
// null), anything else must be a non-negative integer. Approval `uses` and
// `amount` additionally require positive values within the plane's limits.
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

// CheckCap preserves zero budgets, but approval uses and amounts must be
// positive and within the plane's limits.
func CheckCap(what string, value *int64) error {
	if value == nil {
		return nil
	}
	min, max := int64(0), int64(math.MaxInt64)
	switch what {
	case "uses":
		min, max = 1, 1_000_000
	case "amount":
		min, max = 1, 1_000_000_000_000
	}
	if *value < min || *value > max {
		return fmt.Errorf("%s must be between %d and %d", what, min, max)
	}
	return nil
}

// ParseTTL reads an approval's TTL with the command's suffixes — a bare
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

// ParseToolList reads a comma-separated allowlist. "-" is the EMPTY
// allowlist — a valid answer meaning nothing is callable without a live
// grant — and is deliberately not an error; it returns an empty, non-nil
// slice so it marshals as [] rather than null.
func ParseToolList(list string) ([]string, error) {
	tools := []string{}
	if strings.TrimSpace(list) == "" || strings.TrimSpace(list) == "-" {
		return tools, nil
	}
	for _, t := range strings.Split(list, ",") {
		if err := namePart("tool name", t); err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	return tools, nil
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

// SetToolAllowlist replaces a credential's tool allowlist.
func (c *Client) SetToolAllowlist(credential string, tools []string) error {
	if err := ValidCredentialName(credential); err != nil {
		return err
	}
	if tools == nil {
		tools = []string{}
	}
	body := map[string]any{"credential": credential, "tools": tools}
	return c.expect(http.MethodPut, "/admin/tool-allowlist", body, http.StatusNoContent, "tool-allow")
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

// Approve mints the bounded grant a pending request asked for.
//
// The at-least-one-bound rule is checked HERE as well as by the plane, and
// the plane's own sentence is the one reported: an operator who typed
// `kmx approve <id>` should be told why before a port-forward is opened, and
// should be told the same thing either way.
func (c *Client) Approve(id string, ttlSeconds, maxUses, amount *int64) (map[string]any, error) {
	if err := ValidRequestID(id); err != nil {
		return nil, err
	}
	if err := CheckBounds(ttlSeconds, maxUses); err != nil {
		return nil, err
	}
	if err := CheckCap("amount", amount); err != nil {
		return nil, err
	}
	// Only the bounds that were SET are sent.
	body := map[string]any{}
	if ttlSeconds != nil {
		body["ttl_seconds"] = *ttlSeconds
	}
	if maxUses != nil {
		body["max_uses"] = *maxUses
	}
	if amount != nil {
		body["amount"] = *amount
	}
	status, out, err := c.Do(http.MethodPost, "/admin/approvals/"+id+"/approve", body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated {
		return nil, fmt.Errorf("approve failed (HTTP %d): %s", status, strings.TrimSpace(string(out)))
	}
	return decode(out)
}

// CheckBounds is the plane's at-least-one-bound rule, with the plane's own
// wording (plane/internal/proxy/admin_approvals.go).
func CheckBounds(ttlSeconds, maxUses *int64) error {
	if ttlSeconds == nil && maxUses == nil {
		return fmt.Errorf("an unbounded grant is a config change, not an approval — set --ttl and/or --uses")
	}
	if ttlSeconds != nil && (*ttlSeconds < 1 || *ttlSeconds > 30*24*60*60) {
		return fmt.Errorf("approval ttl_seconds must be between 1 and 2592000")
	}
	return CheckCap("uses", maxUses)
}

// Deny refuses a pending request.
func (c *Client) Deny(id string) error {
	if err := ValidRequestID(id); err != nil {
		return err
	}
	return c.expect(http.MethodPost, "/admin/approvals/"+id+"/deny", nil, http.StatusNoContent, "deny")
}

// Request files an approval request explicitly.
//
// args names the CALL a tool request is about. It is meaningful only
// on a tool request, and omitting it means the ARGUMENT-LESS call — never
// "any call". The plane computes the digest with the gateway's own code, so
// the request and the agent's retry are provably the same call.
func (c *Client) Request(credential, kind, subject string, args map[string]any) (bool, error) {
	if err := ValidRequest(credential, kind, subject, args); err != nil {
		return false, err
	}
	body := map[string]any{"credential": credential, "kind": kind, "subject": subject}
	if args != nil {
		body["arguments"] = args
	}
	status, out, err := c.Do(http.MethodPost, "/admin/requests", body)
	if err != nil {
		return false, err
	}
	if status != http.StatusCreated {
		return false, fmt.Errorf("request failed (HTTP %d): %s", status, strings.TrimSpace(string(out)))
	}
	doc, err := decode(out)
	if err != nil {
		return false, err
	}
	deduped, _ := doc["deduped"].(bool)
	return deduped, nil
}

// ValidRequest is the shape check in front of a filing. It is exported so
// the command layer can run it BEFORE the guard and the port-forward: a
// mistyped subject should fail on the spot, not after an operator has
// confirmed a context for it.
func ValidRequest(credential, kind, subject string, args map[string]any) error {
	if err := ValidCredentialName(credential); err != nil {
		return err
	}
	switch kind {
	case "tool", "budget":
	default:
		return fmt.Errorf("kind must be tool or budget")
	}
	if err := namePart("subject", subject); err != nil {
		return err
	}
	// The arguments name the CALL a TOOL request is about. On a budget
	// request there is no call to name, so accepting them would be
	// accepting something the plane cannot act on.
	if args != nil && kind != "tool" {
		return fmt.Errorf("--args is meaningful only on tool requests")
	}
	return nil
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

// GrantSummary renders an approval's reply for the operator:
// "Granted: <credential> <kind>/<subject> — <bounds> (grant <id>)".
func GrantSummary(g map[string]any) string {
	var bounds []string
	if s := str(g["expires_at"]); s != "" {
		bounds = append(bounds, "expires "+s)
	}
	if v, ok := g["max_uses"]; ok && v != nil {
		bounds = append(bounds, str(v)+" use(s)")
	}
	if v, ok := g["amount"]; ok && v != nil {
		bounds = append(bounds, "amount "+str(v))
	}
	return fmt.Sprintf("Granted: %s %s/%s — %s (grant %s)",
		str(g["credential"]), str(g["kind"]), str(g["subject"]),
		strings.Join(bounds, ", "), str(g["id"]))
}
