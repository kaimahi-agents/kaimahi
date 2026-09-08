package erp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	f, err := os.Open(shipped)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fx, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return New(fx)
}

func post(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	s.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, out
}

func callTool(t *testing.T, s *Server, name, args string) (result map[string]any, isError bool) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	code, out := post(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("tools/call %s: HTTP %d", name, code)
	}
	if _, bad := out["error"]; bad {
		t.Fatalf("tools/call %s: JSON-RPC error %v", name, out["error"])
	}
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s: no result in %v", name, out)
	}
	return res, res["isError"] == true
}

func TestTheNineToolsAreOffered(t *testing.T) {
	s := newTestServer(t)
	_, out := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	res := out["result"].(map[string]any)
	var names []string
	for _, raw := range res["tools"].([]any) {
		names = append(names, raw.(map[string]any)["name"].(string))
	}
	want := []string{"contract_get", "dispute_open", "invoice_get", "invoice_list",
		"payment_policy_get", "payment_schedule", "po_get", "receiving_get", "vendor_notify"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools offered: %v, want %v", names, want)
	}
}

func TestReadsAnswerFromTheCorpus(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct{ tool, args, want string }{
		{"invoice_get", `{"invoice_id": "INV-88134"}`, `"total_cents": 4800000`},
		{"invoice_list", `{"vendor_id": "MER-4471"}`, `"INV-88134"`},
		{"po_get", `{"po_id": "PO-2291"}`, `"unit_price_cents": 10500`},
		{"receiving_get", `{"po_id": "PO-2291"}`, `"backordered": 90`},
		{"contract_get", `{"vendor_id": "MER-4471"}`, `WRITTEN authorization`},
		{"payment_policy_get", `{}`, `"approval_threshold_cents": 1000000`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			res, isErr := callTool(t, s, tc.tool, tc.args)
			if isErr {
				t.Fatalf("%s failed: %v", tc.tool, res)
			}
			text := res["content"].([]any)[0].(map[string]any)["text"].(string)
			if !strings.Contains(text, tc.want) {
				t.Fatalf("%s payload does not contain %q:\n%s", tc.tool, tc.want, text)
			}
		})
	}
	// A read the corpus cannot answer fails as a tool error, not a crash.
	res, isErr := callTool(t, s, "invoice_get", `{"invoice_id": "INV-00000"}`)
	if !isErr {
		t.Fatalf("an unknown invoice was answered: %v", res)
	}
}

// The ERP is the system of record, not a control: it accepts the injected
// payment. Everything that stops it is in front of it. If this test ever
// starts failing because the ERP got careful, the demo has quietly moved
// its proof into the fixture.
func TestTheERPIsNotTheControl(t *testing.T) {
	s := newTestServer(t)
	res, isErr := callTool(t, s, "payment_schedule",
		`{"invoice_id": "INV-88140", "amount_cents": 4800000, "payee_id": "MER-9911"}`)
	if isErr {
		t.Fatalf("the ERP refused the substituted payee — the demo must not depend on that: %v", res)
	}
	sc := res["structuredContent"].(map[string]any)
	if sc["payee_id"] != "MER-9911" || sc["status"] != "scheduled" {
		t.Fatalf("unexpected payment record: %v", sc)
	}
	acts := s.Actions()
	if len(acts) != 1 || acts[0].Tool != "payment_schedule" {
		t.Fatalf("the action was not recorded: %v", acts)
	}
}

func TestConsequentialToolsValidateTheirArguments(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct{ name, tool, args, want string }{
		{"amount must be an integer", "payment_schedule",
			`{"invoice_id": "INV-88134", "amount_cents": "3255000", "payee_id": "MER-4471"}`, "integer number of cents"},
		{"amount must not be fractional", "payment_schedule",
			`{"invoice_id": "INV-88134", "amount_cents": 3255000.5, "payee_id": "MER-4471"}`, "integer number of cents"},
		{"payee is required", "payment_schedule",
			`{"invoice_id": "INV-88134", "amount_cents": 3255000}`, "payee_id is required"},
		{"invoice must exist", "dispute_open",
			`{"invoice_id": "INV-00000", "amount_cents": 600000}`, "no such invoice"},
		{"vendor must exist", "vendor_notify", `{"vendor_id": "MER-9911"}`, "no such vendor"},
		{"unknown tool", "settle_everything", `{}`, "no such tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, isErr := callTool(t, s, tc.tool, tc.args)
			if !isErr {
				t.Fatalf("accepted: %v", res)
			}
			text := res["content"].([]any)[0].(map[string]any)["text"].(string)
			if !strings.Contains(text, tc.want) {
				t.Fatalf("refused for the wrong reason: %s", text)
			}
		})
	}
	if acts := s.Actions(); len(acts) != 0 {
		t.Fatalf("a refused call was recorded as an action: %v", acts)
	}
}

func TestLifecycleAndUnknownMethods(t *testing.T) {
	s := newTestServer(t)
	code, out := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if code != http.StatusOK || out["result"] == nil {
		t.Fatalf("initialize: %d %v", code, out)
	}
	rec := httptest.NewRecorder()
	s.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notifications/initialized: %d", rec.Code)
	}
	_, out = post(t, s, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	if out["error"] == nil {
		t.Fatalf("an unrelayed method was answered: %v", out)
	}
	rec = httptest.NewRecorder()
	s.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
}
