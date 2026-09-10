package admin

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// The two trails say who called: the client's own word for itself, and
// the address the plane observed. A reader meeting "acted for: none"
// needs both to read the word in context.

const callerToolReply = `{"entries": [
  {"created_at": "2026-09-08T13:58:28Z", "credential": "ap-agent", "upstream": "erp",
   "method": "tools/call", "tool": "invoice_get", "decision": "allowed", "status": 200,
   "detail": "", "arg_digest": "ebb1d47dba1eabc0000000000000000000000000000000000000000000000000",
   "arg_summary": "invoice_get: invoice_id INV-88134", "acted_for": "none",
   "caller_claim": "ua:curl/8.5.0", "caller_addr": "127.0.0.1"},
  {"created_at": "2026-09-08T13:57:11Z", "credential": "ap-agent", "upstream": "erp",
   "method": "tools/call", "tool": "invoice_get", "decision": "allowed", "status": 200,
   "detail": "", "arg_digest": "", "arg_summary": "", "acted_for": "none",
   "caller_claim": "ua:kagent/0.9.12", "caller_addr": "10.244.1.7"},
  {"created_at": "2026-09-08T09:00:00Z", "credential": "ap-agent", "upstream": "erp",
   "method": "tools/list", "tool": "", "decision": "allowed", "status": 200,
   "detail": "", "arg_digest": "", "arg_summary": "", "acted_for": "none",
   "caller_claim": "legacy", "caller_addr": "legacy"}
]}`

func TestTheToolAuditSaysWhoCalled(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(callerToolReply))
	}))
	var out bytes.Buffer
	if err := c.ToolAudit(&out, ""); err != nil {
		t.Fatalf("ToolAudit: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"caller (claimed)", "from (observed)",
		"ua:curl/8.5.0", "127.0.0.1",
		"ua:kagent/0.9.12", "10.244.1.7",
		// A row written before the columns existed says so, and it is not
		// the same word as "the caller offered no name".
		"legacy",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tool audit lacks %q:\n%s", want, got)
		}
	}

	// "acted for" is still LAST: ci.yml anchors on it at end of line
	// (`'none *$'`), and the caller columns went in immediately before it
	// for exactly that reason.
	if !regexp.MustCompile(`(?m)ua:curl/8\.5\.0 +127\.0\.0\.1 +none *$`).MatchString(got) {
		t.Errorf("the caller columns are not immediately before a trailing 'acted for':\n%s", got)
	}
	// And every pattern CI already greps still matches.
	if !regexp.MustCompile(`ap-agent +erp +tools/call +invoice_get +allowed +200`).MatchString(got) {
		t.Errorf("an existing CI pattern stopped matching:\n%s", got)
	}
}

func TestTheLedgerSaysWhoCalled(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entries": [{"created_at": "2026-09-08T14:02:01Z",
		  "credential": "foreign-runtime", "upstream": "ollama", "model": "qwen2.5:3b",
		  "input_tokens": 0, "output_tokens": 0, "cost_cents": 0, "cost_source": "denied",
		  "status": 429, "acted_for": "none",
		  "caller_claim": "ua:curl/8.5.0", "caller_addr": "10.244.4.2"}]}`))
	}))
	var out bytes.Buffer
	if err := c.Ledger(&out, ""); err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	got := out.String()
	if !regexp.MustCompile(`(?m)ua:curl/8\.5\.0 +10\.244\.4\.2 +none *$`).MatchString(got) {
		t.Errorf("the ledger does not name its caller before 'acted for':\n%s", got)
	}
	if !regexp.MustCompile(`foreign-runtime +ollama +qwen2\.5:3b +0 +0 +0 +denied +429`).MatchString(got) {
		t.Errorf("an existing CI pattern stopped matching:\n%s", got)
	}
}

func TestAPlaneTooOldToRecordACallerSaysSoRatherThanNothing(t *testing.T) {
	// kmx talks to planes older than itself on purpose (contract.go). A
	// row with no caller field at all must read as "no record", not as an
	// empty answer about who called.
	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entries": [{"created_at": "2026-09-08T13:58:28Z",
		  "credential": "ap-agent", "upstream": "erp", "method": "tools/list",
		  "decision": "allowed", "status": 200, "detail": "", "acted_for": "none"}]}`))
	}))
	var out bytes.Buffer
	if err := c.ToolAudit(&out, ""); err != nil {
		t.Fatalf("ToolAudit: %v", err)
	}
	if !regexp.MustCompile(`(?m)unrecorded +unrecorded +none *$`).MatchString(out.String()) {
		t.Errorf("an absent caller field did not render as 'unrecorded':\n%s", out.String())
	}
}

func TestAHostileCallerNameCannotBreakTheRenderedTable(t *testing.T) {
	// The claim is the one field in a governed row an attacker writes. The
	// plane bounds it at the write; the renderer must not be the place the
	// bound is missing, because a row read from an older plane, a restored
	// dump or a hand-edited fixture is not bound by today's writer.
	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entries": [{"created_at": "2026-09-08T13:58:28Z",
		  "credential": "ap-agent", "upstream": "erp", "method": "tools/call",
		  "tool": "invoice_get", "decision": "allowed", "status": 200, "detail": "",
		  "acted_for": "none",
		  "caller_claim": "ua:evil\" ,\n2026-09-08T00:00:00 ap-agent erp tools/call payment_schedule allowed 200",
		  "caller_addr": "10.244.1.7"}]}`))
	}))
	var out bytes.Buffer
	if err := c.ToolAudit(&out, ""); err != nil {
		t.Fatalf("ToolAudit: %v", err)
	}
	got := out.String()
	if lines := strings.Count(strings.TrimRight(got, "\n"), "\n"); lines != 1 {
		t.Errorf("one header and one row expected, got %d newlines — a forged row got through:\n%s", lines+1, got)
	}
	if strings.Contains(got, "payment_schedule") {
		t.Errorf("the forged tail of the caller name reached the table:\n%s", got)
	}
}

// The renderer must keep every record on one physical line, visibly escape
// hostile cells, and clip caller fields without making them look complete.
func TestToolAuditEscapesAndClipsHostileCells(t *testing.T) {
	doc := `{"entries": [
	  {"created_at": "2026-09-08T13:58:28Z", "credential": "ap-agent", "upstream": "erp",
	   "method": "tools/call", "tool": "invoice_get", "decision": "allowed", "status": 200,
	   "detail": "", "arg_digest": "ebb1d47dba1eabc0", "arg_summary": "invoice_get: id INV-1",
	   "acted_for": "none", "caller_claim": "ua:curl/8.5.0", "caller_addr": "127.0.0.1"},
	  {"created_at": "2026-09-08T13:57:11Z", "credential": "ap-agent", "upstream": "erp",
	   "method": "tools/call", "tool": "invoice_get\ttrailing", "decision": "allowed", "status": 200,
	   "detail": "quote \" and backslash \\ and newline \n here", "arg_digest": "", "arg_summary": "",
	   "acted_for": "none", "caller_claim": "ua:naïve/1.0 \u200b", "caller_addr": "fe80::1ff:fe23:4567:890a%eth0"},
	  {"created_at": "2026-09-08T09:00:00Z", "credential": "ap-agent", "upstream": " openai",
	   "method": "tools/list", "tool": "", "decision": "allowed", "status": 200,
	   "detail": "", "arg_digest": "", "arg_summary": "", "acted_for": "none",
	   "caller_claim": "legacy", "caller_addr": "legacy"}
	]}`

	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(doc))
	}))
	var goOut bytes.Buffer
	if err := c.ToolAudit(&goOut, ""); err != nil {
		t.Fatalf("ToolAudit: %v", err)
	}

	got := goOut.String()
	if lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n"); len(lines) != 4 {
		t.Fatalf("header plus three one-line records expected, got %d lines:\n%s", len(lines), got)
	}
	for _, want := range []string{
		`"invoice_get\ttrailing"`, // a tab, which would have padded into a lookalike
		"\\u200b\"",               // a zero-width space, invisible if it were passed through
		`" openai"`,               // a leading space, which would render as the real name
		`"quote \" and backslash \\ and newline \n here"`,
		"fe80::1ff:fe23:…",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered audit lacks %q:\n%s", want, got)
		}
	}
	if !regexp.MustCompile(`(?m)legacy +legacy +none *$`).MatchString(got) {
		t.Errorf("caller columns no longer precede the trailing acted-for column:\n%s", got)
	}
}
