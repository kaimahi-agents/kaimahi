package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

func renderTable(out io.Writer, headers []string, rows [][]string, format string) {
	ui := cliui.New(out)
	if ui.Rich() {
		title := "Records"
		roles := make([]cliui.ColumnRole, len(headers))
		state, number := cliui.ColumnState, cliui.ColumnNumber
		switch format {
		case ledgerFmt:
			title = "Ledger"
			roles[4], roles[5], roles[6], roles[7], roles[8] = number, number, number, state, state
		case grantsFmt:
			title = "Grants"
			roles[4], roles[6], roles[7] = state, number, number
		case approvalFmt:
			title = "Approval audit"
			roles[4] = state
		case pendingFmt:
			title = "Pending approvals"
		case credentialsFmt:
			title = "Credentials"
			roles[1], roles[2], roles[4] = number, number, state
		case flowFmt:
			title = "Flow"
			roles[4], roles[5] = state, number
		}
		fmt.Fprintln(out, ui.Report(title, headers, rows, roles...))
		return
	}
	values := make([]any, len(headers))
	for i := range headers {
		values[i] = headers[i]
	}
	fmt.Fprintf(out, format, values...)
	for _, row := range rows {
		values = make([]any, len(row))
		for i := range row {
			values[i] = row[i]
		}
		fmt.Fprintf(out, format, values...)
	}
}

// The read views. Every format string, column width, header and empty-case
// line below is a compatibility contract because these are not decorative.
// CI greps them (`hello-world +ollama +qwen2\.5:3b +[0-9]+ +[0-9]+ +0 +free
// +200`), the docs quote them, and an operator reading a ledger from `kmx`
// and one reading it from `make` must see the same table.

const (
	// The trailing column on the two enforcement trails is "acted for":
	// who the call was made for. It is LAST on purpose — every existing
	// CI grep and doc example anchors on the columns before it, and a
	// widened column mid-table is a broken pipeline. The two caller
	// columns went in immediately BEFORE it for the same reason: they
	// sit after every column an existing grep names, and "acted for"
	// stays where the end-anchored ones ('none *$') expect it.
	ledgerFmt      = "%-19s %-12s %-9s %-16s %6s %6s %6s %-8s %-6s %-28s %-16s %s\n"
	grantsFmt      = "%-36s %-12s %-8s %-18s %-6s %-22s %-9s %-8s %-19s %-18s %-20s %s\n"
	credentialsFmt = "%-16s %-10s %-12s %-22s %-9s %s\n"
	approvalFmt    = "%-19s %-12s %-8s %-18s %-10s %-18s %-40s %s\n"
	pendingFmt     = "%-36s %-19s %-12s %-8s %-18s %-34s %s\n"
)

// Ledger prints the spend ledger, newest first, plus month-to-date totals.
func (c *Client) Ledger(out io.Writer, credential string) error {
	doc, err := c.Get("ledger", "/admin/ledger?credential="+url.QueryEscape(credential)+"&limit=50")
	if err != nil {
		return err
	}
	var viewRows [][]string
	rich := cliui.New(out).Rich()
	for _, row := range rows(doc, "entries") {
		model := str(row["model"])
		if !rich {
			model = trunc(model, 16)
		}
		viewRows = append(viewRows, []string{
			trunc(str(row["created_at"]), 19), str(row["credential"]), str(row["upstream"]),
			model,
			str(row["input_tokens"]), str(row["output_tokens"]), str(row["cost_cents"]),
			str(row["cost_source"]), str(row["status"]),
			callerClaim(row, rich), callerAddr(row, rich), actedFor(row)})
	}
	renderTable(out, []string{"created (UTC)", "credential", "upstream", "model", "in", "out", "cents", "source", "status", "caller (claimed)", "from (observed)", "acted for"}, viewRows, ledgerFmt)
	if _, ok := doc["month_cents"]; ok {
		fmt.Fprintf(out, "-- month to date: %s cents, %s tokens\n",
			str(doc["month_cents"]), str(doc["month_tokens"]))
	}
	return nil
}

// Grants lists grants with liveness — an expired grant is not a grant.
func (c *Client) Grants(out io.Writer, credential string) error {
	doc, err := c.Get("grants", "/admin/grants?credential="+url.QueryEscape(credential)+"&limit=50")
	if err != nil {
		return err
	}
	list := rows(doc, "grants")
	if len(list) == 0 && !cliui.New(out).Rich() {
		fmt.Fprintln(out, "no grants")
		return nil
	}
	var viewRows [][]string
	for _, g := range list {
		uses := str(g["uses"])
		if max, ok := g["max_uses"]; ok && max != nil {
			uses += "/" + str(max)
		}
		viewRows = append(viewRows, []string{
			str(g["id"]), str(g["credential"]), str(g["kind"]), str(g["subject"]),
			yesno(g["live"]),
			// [:19] inside a 22-wide column: an expiry is printed to the
			// second, and the extra width
			// is the gap before the next column.
			trunc(dash(g["expires_at"]), 19), uses, dash(g["amount"]),
			trunc(str(g["created_at"]), 19), dash(g["decided_by"]), binds(g, cliui.New(out).Rich()),
			// A grant that outlives the credential it was given on is a
			// promise the plane cannot keep, so the two deadlines are
			// read side by side.
			trunc(dash(g["credential_expires_at"]), 19)})
	}
	renderTable(out, []string{"id", "credential", "kind", "subject", "live", "expires (UTC)", "uses", "amount", "created (UTC)", "decided by", "binds", "cred expires (UTC)"}, viewRows, grantsFmt)
	return nil
}

// Approvals lists the requests waiting for a human decision.
//
// The CALL column is not decoration: a tool grant is welded to one
// call, so an approver who can see only the verb is being asked to approve
// something they cannot see. That is the whole problem restated.
//
// The columns are stable to the character. CI does not grep this
// table, it AWKs it — `$1` is the id an approval is then issued against —
// so a widened column is a broken pipeline, not a cosmetic change.
func (c *Client) Approvals(out io.Writer) error {
	doc, err := c.Get("approvals", "/admin/approvals")
	if err != nil {
		return err
	}
	list := rows(doc, "pending")
	if len(list) == 0 && !cliui.New(out).Rich() {
		fmt.Fprintln(out, "no pending approval requests")
		return nil
	}
	var viewRows [][]string
	for _, r := range list {
		viewRows = append(viewRows, []string{
			str(r["id"]), trunc(str(r["created_at"]), 19), str(r["credential"]),
			str(r["kind"]), str(r["subject"]), str(r["detail"]), dash(r["arg_summary"])})
	}
	renderTable(out, []string{"id", "created (UTC)", "credential", "kind", "subject", "detail", "call"}, viewRows, pendingFmt)
	return nil
}

// ApprovalAudit prints the approvals' own trail: filed, approved, denied.
func (c *Client) ApprovalAudit(out io.Writer, credential string) error {
	doc, err := c.Get("approval-audit", "/admin/approval-audit?credential="+url.QueryEscape(credential)+"&limit=50")
	if err != nil {
		return err
	}
	var viewRows [][]string
	for _, e := range rows(doc, "entries") {
		viewRows = append(viewRows, []string{
			trunc(str(e["created_at"]), 19), str(e["credential"]), str(e["kind"]),
			str(e["subject"]), str(e["action"]), dash(e["decided_by"]), str(e["bounds"]),
			dash(e["arg_summary"])})
	}
	renderTable(out, []string{"created (UTC)", "credential", "kind", "subject", "action", "decided by", "bounds", "call"}, viewRows, approvalFmt)
	return nil
}

// Credentials lists the governed credentials and, first of all, when
// each one dies. A credential that expires silently at 3am is an outage
// nobody diagnosed; this is the view where an operator sees it a week
// out, and the reason the table is sorted soonest-first.
func (c *Client) Credentials(out io.Writer) error {
	doc, err := c.Get("credentials", "/admin/credentials")
	if err != nil {
		return err
	}
	list := rows(doc, "credentials")
	if len(list) == 0 && !cliui.New(out).Rich() {
		fmt.Fprintln(out, "no credentials")
		return nil
	}
	var viewRows [][]string
	for _, c := range list {
		viewRows = append(viewRows, []string{
			str(c["credential"]), dash(c["cap_cents"]), dash(c["cap_tokens"]),
			trunc(dash(c["expires_at"]), 19), expiryState(c),
			trunc(str(c["created_at"]), 19)})
	}
	renderTable(out, []string{"credential", "cap cents", "cap tokens", "expires (UTC)", "state", "created (UTC)"}, viewRows, credentialsFmt)
	return nil
}

// expiryState is the word an operator scans the column for. "no expiry"
// is the legacy class: issued before credentials expired, still valid,
// and a class that can only shrink — never a blank that reads as a bug.
func expiryState(c map[string]any) string {
	switch {
	case c["expires_at"] == nil:
		return "no expiry"
	case yesno(c["expired"]) == "yes":
		return "EXPIRED"
	case yesno(c["expiring_soon"]) == "yes":
		return "EXPIRING"
	}
	return "ok"
}

// actedFor renders who a call was made for. The three words that are not
// a person are different answers and stay different: 'none' (there is no
// person), 'unknown' (the plane cannot say) and 'legacy' (the row
// predates attribution).
func actedFor(e map[string]any) string {
	if v := str(e["acted_for"]); v != "" {
		return v
	}
	return "unknown"
}

// callerClaim renders what the caller said it was. The value carries its
// own 'ua:' prefix from the plane, which is the point: this column is the
// caller's word for itself and must never read as something the plane
// checked. A row from a plane too old to serve the field renders
// "unrecorded"; a row from this plane that predates the column renders
// its own word, "legacy". Two different histories, neither collapsed
// into the other, and both meaning no record of who called.
func callerClaim(e map[string]any, rich ...bool) string {
	claim := orUnrecorded(str(e["caller_claim"]))
	if len(rich) > 0 && rich[0] {
		return claim
	}
	return clipCell(claim, 28)
}

// callerAddr renders the peer address the plane observed. Not a claim by
// the thing being governed — but still only an address: in a cluster it
// names a pod, and pod addresses are reused.
func callerAddr(e map[string]any, rich ...bool) string {
	addr := orUnrecorded(str(e["caller_addr"]))
	if len(rich) > 0 && rich[0] {
		return addr
	}
	return clipCell(addr, 16)
}

// clipCell shortens a cell and SAYS it shortened it. The plain trunc is
// right for a timestamp, where everyone knows the seconds were cut; it is
// wrong for these two, where a silently shortened value still looks like
// a whole one. An IPv6 address cut to sixteen characters is a different,
// perfectly plausible address, and every caller in the same prefix
// renders identically — which would quietly destroy the one half of the
// pair the caller cannot choose.
func clipCell(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func orUnrecorded(v string) string {
	if v == "" {
		return "unrecorded"
	}
	return v
}

// binds says what a tool grant admits: one CALL, named by the
// digest of its policy-relevant arguments. A tool grant with no digest is
// the closed legacy class — minted before argument binding, so it admits
// any arguments and says so; other kinds have no arguments at all.
func binds(g map[string]any, rich ...bool) string {
	if str(g["kind"]) != "tool" {
		return "-"
	}
	if d := str(g["arg_digest"]); d != "" {
		if len(rich) > 0 && rich[0] {
			return "call " + d
		}
		return "call " + trunc(d, 12)
	}
	return "verb-level (legacy)"
}

// rows returns a document's list of records, tolerating a null or absent
// key because an empty result is a valid response.
func rows(doc map[string]any, key string) []map[string]any {
	list, _ := doc[key].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// str renders a JSON value for a table cell. Numbers keep the digits the
// plane sent (the decoder is in UseNumber mode), and a null prints empty —
// the API's textual behaviour, since these tables are read by eye and by grep.
// It also keeps a cell to ONE LINE. The plane bounds what it writes into
// an audit column, but this renderer prints rows it did not write today:
// an older plane's, a restored dump's, a fixture's. A newline in a cell
// renders as a second line, which reads as a governed row nobody wrote —
// so the defence is at the write AND here, and neither is trusted to be
// the only one.
func str(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return oneLine(value)
	case json.Number:
		return value.String()
	case bool:
		if value {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(value)
	}
}

// dash renders an absent optional as "-", which is what the columns for
// expiry, amount and the approver mean by "not set".
func dash(v any) string {
	if s := str(v); s != "" {
		return s
	}
	return "-"
}

func yesno(v any) string {
	if b, ok := v.(bool); ok && b {
		return "yes"
	}
	return "no"
}

// oneLine is the read-side twin of the store's audit-text rule: a value that
// is already one printable line
// with no padding is printed exactly as it is; anything else is QUOTED.
//
// Quoted rather than stripped, for the reason the store gives at length
// (plane/internal/store/audittext.go): stripping a control character to
// a space and letting the column pad it makes a forged value render
// exactly like a legitimate one, which is worse than the mess it tidies.
// The store stops such a value being written today — this is for the
// rows it did not write: an older plane's, a restored dump's.
//
// The escaping is spelled out here so its quote character and escape forms
// remain an explicit output contract rather than inheriting strconv changes.
func oneLine(s string) string {
	if isCleanCell(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if unicode.IsPrint(r) {
				b.WriteRune(r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// isCleanCell reports whether a cell can be printed unaltered: printable
// throughout, and not padded at either end.
func isCleanCell(s string) bool {
	if s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// trunc cuts a cell to n characters: a timestamp is printed to the second,
// not to the microsecond.
//
// CHARACTERS, not bytes. Go's format widths count runes, so a byte slice here
// would both cut a multibyte rune
// in half — printing replacement junk into an audit table — and make the
// corrupt exactly the values most worth reading carefully, since a caller
// chooses its own name. Identical for the ASCII this truncates most of the
// time.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
