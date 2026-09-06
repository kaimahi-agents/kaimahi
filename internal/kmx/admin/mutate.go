package admin

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// The admin plane's MUTATIONS, and the argument validation in front of them.
//
// scripts/plane-admin.sh is the specification here as it is for the reads:
// every check_name, check_cap, check_uuid and tool-name shape below is the
// script's, and every well-formed-positive status check is the script's
// `[ "$status" = 204 ] || …`. The plane validates all of it again — these
// checks exist because these values are interpolated into JSON and paths,
// and because a typo should fail before an admin port-forward is opened,
// not after.
//
// Custody is the package's, unchanged: the admin bearer never leaves this
// process, and no mutation follows a redirect.

// ValidCredentialName is the script's check_name.
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

// namePart is the script's shape for a tool name and for a request subject:
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

// ValidRequestID is the script's check_uuid, message included: the id comes
// off the approvals table, so the error says where to find one.
func ValidRequestID(id string) error {
	if !uuidRe.MatchString(id) {
		return fmt.Errorf("invalid request id %q (want a UUID from `kmx approvals`)", id)
	}
	return nil
}

// ParseCap reads a cap-shaped argument: "-" or "" means "no cap" (JSON
// null), anything else must be a non-negative integer. The script's
// check_cap, which also governs `uses` and `amount` on an approval.
func ParseCap(what, value string) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return nil, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || strings.ContainsAny(value, "+-") {
		return nil, fmt.Errorf("invalid %s %q (want a non-negative integer or -)", what, value)
	}
	return &n, nil
}

// ParseTTL reads an approval's TTL with the script's suffixes — a bare
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
	if err != nil || n < 0 || strings.ContainsAny(digits, "+-") {
		return nil, bad
	}
	switch unit {
	case "", "s":
	case "m":
		n *= 60
	case "h":
		n *= 3600
	case "d":
		n *= 86400
	default:
		return nil, bad
	}
	return &n, nil
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
// one. No token is minted, sent or read: renewal moves a date, which is
// the only reason a CLI that accepts no credential material can offer it
// at all.
func (c *Client) RenewCredential(credential string, ttlSeconds *int64) (string, error) {
	if err := ValidCredentialName(credential); err != nil {
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
	// Only the bounds that were SET are sent, as the script builds its body.
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
	return nil
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
	case "tool", "budget", "inbound":
	default:
		return fmt.Errorf("kind must be tool, budget or inbound")
	}
	if err := namePart("subject", subject); err != nil {
		return err
	}
	// The arguments name the CALL a TOOL request is about. On a budget or
	// inbound request there is no call to name, so accepting them would be
	// accepting something the plane cannot act on.
	if args != nil && kind != "tool" {
		return fmt.Errorf("--args is meaningful only on tool requests")
	}
	return nil
}

// expect performs a mutation and refuses anything but the well-formed
// positive, quoting the body — the script's status check, unchanged.
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

// GrantSummary renders an approval's reply the way the script does:
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
