package admin

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// TestTheTwoRenderersAgreeByteForByte runs the shell renderer's own
// python block and this package's Go one over the same document and
// requires identical bytes.
//
// The format-string test below catches a width that drifts. This catches
// the harder half: the two now ESCAPE a hostile cell rather than
// stripping it, and Go's strconv.Quote and Python's repr() disagree on
// the quote character and the escape forms — so the scheme is spelled
// out twice and only running both proves they agree. The fixture is
// deliberately nasty: a newline, a tab, a quote, a backslash, a
// zero-width space, a multibyte name and a value long enough to clip.
func TestTheTwoRenderersAgreeByteForByte(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; the shell renderer cannot be run")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "plane-admin.sh"))
	if err != nil {
		t.Fatalf("read plane-admin.sh: %v", err)
	}
	// Lift the tool-audit view's python block out of its heredoc.
	block := string(script)
	start := strings.Index(block, "  tool-audit)")
	if start < 0 {
		t.Fatal("plane-admin.sh has no tool-audit block")
	}
	block = block[start:]
	from := strings.Index(block, "import json, sys")
	to := strings.Index(block, "\nEOF\n")
	if from < 0 || to < 0 || to < from {
		t.Fatal("cannot delimit the tool-audit python block")
	}
	py := block[from:to]

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

	dir := t.TempDir()
	docPath := filepath.Join(dir, "doc.json")
	if err := os.WriteFile(docPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	pyPath := filepath.Join(dir, "render.py")
	if err := os.WriteFile(pyPath, []byte(py), 0o600); err != nil {
		t.Fatal(err)
	}
	shellOut, err := exec.Command("python3", pyPath, docPath).CombinedOutput()
	if err != nil {
		t.Fatalf("shell renderer failed: %v\n%s", err, shellOut)
	}

	c, _ := open(t, health(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(doc))
	}))
	var goOut bytes.Buffer
	if err := c.ToolAudit(&goOut, ""); err != nil {
		t.Fatalf("ToolAudit: %v", err)
	}

	if goOut.String() != string(shellOut) {
		t.Errorf("the two renderers disagree.\n--- go ---\n%s\n--- shell ---\n%s", goOut.String(), shellOut)
	}
	// And the hostile cells really were escaped, so this is not two
	// renderers agreeing on having done nothing.
	for _, want := range []string{
		`"invoice_get\ttrailing"`, // a tab, which would have padded into a lookalike
		"\\u200b\"",               // a zero-width space, invisible if it were passed through
		`" openai"`,               // a leading space, which would render as the real name
	} {
		if !strings.Contains(goOut.String(), want) {
			t.Errorf("the fixture's hostile cell %s was not escaped:\n%s", want, goOut.String())
		}
	}
}

// The two renderers are one specification in two languages, and the file
// comment on views.go says so. This holds them to it: every format string
// here is the shell's, character for character. A width that drifts in one
// and not the other is a broken pipeline in whichever half CI does not run.
func TestTheFormatStringsAreTheScriptsVerbatim(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "plane-admin.sh"))
	if err != nil {
		t.Fatalf("read plane-admin.sh: %v", err)
	}
	script := string(raw)
	for name, format := range map[string]string{
		"ledger":     ledgerFmt,
		"tool-audit": toolFmt,
		"grants":     grantsFmt,
		"approvals":  approvalFmt,
		"pending":    pendingFmt,
	} {
		want := `fmt = "` + strings.TrimSuffix(format, "\n") + `"`
		if !strings.Contains(script, want) {
			t.Errorf("%s: plane-admin.sh does not carry the Go renderer's format\n  want line: %s", name, want)
		}
	}
	// And the headers, which are what an operator and a grep both read.
	for _, header := range []string{
		`"status", "caller (claimed)", "from (observed)", "acted for"`,
		`"call", "caller (claimed)", "from (observed)", "acted for"`,
	} {
		if !strings.Contains(script, header) {
			t.Errorf("plane-admin.sh does not carry the header %s", header)
		}
	}
}
