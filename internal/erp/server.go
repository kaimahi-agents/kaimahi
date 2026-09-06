package erp

// The MCP surface. It speaks the same streamable-HTTP subset the Kaimahi
// gateway relays — initialize, notifications/initialized, tools/list,
// tools/call — answered as plain JSON, with no sessions and no SSE. That
// is deliberately the smallest thing that is a real MCP server: the
// gateway in front of it is what the demo is about.
//
// Posture, the same as every server this repo deploys: it holds no
// credential, it dials nothing, and it answers only what it is asked.
//
// It is a SYSTEM OF RECORD, NOT A CONTROL. payment_schedule accepts any
// payee the caller names, exactly as a real ERP's payment API would;
// nothing here checks an amount against a policy or refuses a substituted
// payee. If the ERP refused the injected payment, the demo would be
// proving that the fixture is careful rather than that the plane is.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
)

// Server answers MCP over one corpus.
type Server struct {
	fx *Fixtures

	mu      sync.Mutex
	actions []Action
}

// Action is one consequential call the ERP accepted. Kept in memory: the
// demo is deterministic and has no database.
type Action struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	Result    map[string]any `json:"result"`
}

func New(fx *Fixtures) *Server { return &Server{fx: fx} }

// Actions returns what the ERP has been asked to do, oldest first. The
// demo prints it to show that the payments the plane refused did not
// happen HERE either — a denial upstream is only worth as much as the
// absence of the effect downstream.
func (s *Server) Actions() []Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Action(nil), s.actions...)
}

func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/actions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"actions": s.Actions()})
	})
	mux.HandleFunc("/mcp", s.serveMCP)
	return mux
}

const maxBody = 1 << 20

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.UseNumber() // money is integer cents; a float round-trip is not acceptable
	if err := dec.Decode(&msg); err != nil {
		http.Error(w, "not json", http.StatusBadRequest)
		return
	}
	switch msg.Method {
	case "initialize":
		s.rpc(w, msg.ID, map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "kaimahi-erp", "version": "0"},
		}, nil)
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		s.rpc(w, msg.ID, map[string]any{"tools": toolList()}, nil)
	case "tools/call":
		s.call(w, msg.ID, msg.Params.Name, msg.Params.Arguments)
	default:
		if len(msg.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.rpc(w, msg.ID, nil, map[string]any{"code": -32601, "message": "method not found"})
	}
}

func (s *Server) rpc(w http.ResponseWriter, id json.RawMessage, result any, rpcErr map[string]any) {
	out := map[string]any{"jsonrpc": "2.0"}
	if len(id) > 0 {
		out["id"] = id
	}
	if rpcErr != nil {
		out["error"] = rpcErr
	} else {
		out["result"] = result
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// call dispatches one tool. A tool result is the JSON the ERP would
// return, rendered as MCP text content — one shape for every tool, so the
// agent (and a human reading an audit) sees the record, not prose.
func (s *Server) call(w http.ResponseWriter, id json.RawMessage, name string, args map[string]any) {
	if args == nil {
		args = map[string]any{}
	}
	payload, err := s.dispatch(name, args)
	if err != nil {
		slog.Info("erp: tool refused", "tool", name, "err", err)
		s.rpc(w, id, map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": err.Error()}},
			"isError":           true,
			"structuredContent": map[string]any{"error": err.Error()},
		}, nil)
		return
	}
	text, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.rpc(w, id, nil, map[string]any{"code": -32603, "message": "encode failed"})
		return
	}
	slog.Info("erp: tool call", "tool", name)
	s.rpc(w, id, map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(text)}},
		"isError":           false,
		"structuredContent": payload,
	}, nil)
}

func (s *Server) dispatch(name string, args map[string]any) (any, error) {
	switch name {
	case "invoice_get":
		id, err := str(args, "invoice_id", true)
		if err != nil {
			return nil, err
		}
		inv, ok := s.fx.Invoices[id]
		if !ok {
			return nil, fmt.Errorf("no such invoice: %s", id)
		}
		return inv, nil
	case "invoice_list":
		vendor, err := str(args, "vendor_id", false)
		if err != nil {
			return nil, err
		}
		if vendor != "" {
			if _, ok := s.fx.Vendors[vendor]; !ok {
				return nil, fmt.Errorf("no such vendor: %s", vendor)
			}
		}
		return map[string]any{"invoices": s.fx.InvoicesFor(vendor)}, nil
	case "po_get":
		id, err := str(args, "po_id", true)
		if err != nil {
			return nil, err
		}
		po, ok := s.fx.PurchaseOrders[id]
		if !ok {
			return nil, fmt.Errorf("no such purchase order: %s", id)
		}
		return po, nil
	case "receiving_get":
		id, err := str(args, "po_id", true)
		if err != nil {
			return nil, err
		}
		if _, ok := s.fx.PurchaseOrders[id]; !ok {
			return nil, fmt.Errorf("no such purchase order: %s", id)
		}
		records := s.fx.Receiving[id]
		if records == nil {
			records = []ReceivingRecord{}
		}
		return map[string]any{"po_id": id, "records": records}, nil
	case "contract_get":
		id, err := str(args, "vendor_id", true)
		if err != nil {
			return nil, err
		}
		c, ok := s.fx.Contracts[id]
		if !ok {
			return nil, fmt.Errorf("no contract on file for vendor: %s", id)
		}
		return c, nil
	case "payment_policy_get":
		return s.fx.PaymentPolicy, nil

	case "payment_schedule":
		invoice, err := str(args, "invoice_id", true)
		if err != nil {
			return nil, err
		}
		amount, err := cents(args, "amount_cents")
		if err != nil {
			return nil, err
		}
		payee, err := str(args, "payee_id", true)
		if err != nil {
			return nil, err
		}
		if _, ok := s.fx.Invoices[invoice]; !ok {
			return nil, fmt.Errorf("no such invoice: %s", invoice)
		}
		// The payee is NOT checked against the vendor of record. See the
		// package comment: this is the system of record, not the control.
		return s.record("payment_schedule", args, map[string]any{
			"payment_id":   "PAY-" + invoice,
			"invoice_id":   invoice,
			"amount_cents": amount,
			"payee_id":     payee,
			"status":       "scheduled",
		}), nil
	case "dispute_open":
		invoice, err := str(args, "invoice_id", true)
		if err != nil {
			return nil, err
		}
		amount, err := cents(args, "amount_cents")
		if err != nil {
			return nil, err
		}
		reason, err := str(args, "reason", false)
		if err != nil {
			return nil, err
		}
		if _, ok := s.fx.Invoices[invoice]; !ok {
			return nil, fmt.Errorf("no such invoice: %s", invoice)
		}
		return s.record("dispute_open", args, map[string]any{
			"dispute_id":   "DSP-" + invoice,
			"invoice_id":   invoice,
			"amount_cents": amount,
			"reason":       reason,
			"status":       "open",
		}), nil
	case "vendor_notify":
		vendor, err := str(args, "vendor_id", true)
		if err != nil {
			return nil, err
		}
		message, err := str(args, "message", false)
		if err != nil {
			return nil, err
		}
		if _, ok := s.fx.Vendors[vendor]; !ok {
			return nil, fmt.Errorf("no such vendor: %s", vendor)
		}
		return s.record("vendor_notify", args, map[string]any{
			"notice_id": "NTC-" + vendor,
			"vendor_id": vendor,
			"message":   message,
			"status":    "sent",
		}), nil
	}
	return nil, fmt.Errorf("no such tool: %s", name)
}

func (s *Server) record(tool string, args map[string]any, result map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, Action{Tool: tool, Arguments: args, Result: result})
	return result
}

func str(args map[string]any, key string, required bool) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		if required {
			return "", fmt.Errorf("%s is required", key)
		}
		return "", nil
	}
	v, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return v, nil
}

// cents reads an integer amount. Decoded with UseNumber, so a value that
// arrived as 4800000 is still exactly 4800000 here.
func cents(args map[string]any, key string) (int64, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, fmt.Errorf("%s is required", key)
	}
	num, ok := raw.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s must be an integer number of cents", key)
	}
	v, err := num.Int64()
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer number of cents", key)
	}
	if v < 0 {
		return 0, fmt.Errorf("%s must not be negative", key)
	}
	return v, nil
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func schema(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// toolList is the whole surface: six reads and three consequential
// actions. The gateway PROJECTS this onto what the calling credential may
// use, so an agent never sees more than its allowlist and constraints
// admit — which is why every tool can be offered here honestly.
func toolList() []toolDef {
	defs := []toolDef{
		{"invoice_get", "Fetch one invoice, including its lines and any text the vendor sent with it.",
			schema([]string{"invoice_id"}, map[string]any{"invoice_id": strProp("e.g. INV-88134")})},
		{"invoice_list", "List invoices, optionally for one vendor.",
			schema(nil, map[string]any{"vendor_id": strProp("e.g. MER-4471; omit for all")})},
		{"po_get", "Fetch a purchase order: what was ordered, at what price, and which fees were authorized in writing.",
			schema([]string{"po_id"}, map[string]any{"po_id": strProp("e.g. PO-2291")})},
		{"receiving_get", "Fetch the receiving records for a purchase order: how much was actually delivered and how much is backordered.",
			schema([]string{"po_id"}, map[string]any{"po_id": strProp("e.g. PO-2291")})},
		{"contract_get", "Fetch the contract terms on file for a vendor.",
			schema([]string{"vendor_id"}, map[string]any{"vendor_id": strProp("e.g. MER-4471")})},
		{"payment_policy_get", "Fetch the buyer's own accounts-payable policy.", schema(nil, map[string]any{})},

		{"payment_schedule", "Schedule a payment against an invoice. This moves money.",
			schema([]string{"invoice_id", "amount_cents", "payee_id"}, map[string]any{
				"invoice_id":   strProp("the invoice being paid"),
				"amount_cents": intProp("the amount to pay, in whole cents"),
				"payee_id":     strProp("who to pay — the vendor's payee id of record"),
			})},
		{"dispute_open", "Open a dispute against part of an invoice.",
			schema([]string{"invoice_id", "amount_cents"}, map[string]any{
				"invoice_id":   strProp("the invoice being disputed"),
				"amount_cents": intProp("the disputed amount, in whole cents"),
				"reason":       strProp("why"),
			})},
		{"vendor_notify", "Send a notice to a vendor.",
			schema([]string{"vendor_id"}, map[string]any{
				"vendor_id": strProp("who to notify"),
				"message":   strProp("what to tell them"),
			})},
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}
