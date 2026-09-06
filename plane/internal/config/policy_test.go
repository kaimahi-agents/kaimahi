package config

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// base is a minimal valid config; each test bolts its own tool policy
// and constraints onto it.
func base(toolUpstreams, extra string) []byte {
	return []byte(`{
	  "upstreams": {"ollama": {"base_url": "http://ollama.ollama:11434", "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {` + toolUpstreams + `}` + extra + `}`)
}

const erp = `"erp": {"url": "http://erp.kaimahi:8080/mcp", "tools": {
	"payment_schedule": {"policy_fields": ["invoice_id", "amount_cents", "payee_id"]},
	"invoice_get": {"policy_fields": []}}}`

func TestPolicyDeclarationsAndConstraintsLoad(t *testing.T) {
	c, err := Parse(base(erp, `,
	  "standing_constraints": {"ap-agent": {"payment_schedule": [
	    {"field": "amount_cents", "op": "lte", "value": 1000000},
	    {"field": "payee_id", "op": "in", "values": ["MER-4471", "ACME-1042"]}]}}`))
	require.NoError(t, err)

	fields, ok := c.Policy().Declared("payment_schedule")
	require.True(t, ok)
	assert.Equal(t, []string{"invoice_id", "amount_cents", "payee_id"}, fields)

	// An explicit empty declaration is a declaration: no argument is
	// policy-relevant, so the binding is verb-level.
	fields, ok = c.Policy().Declared("invoice_get")
	assert.True(t, ok)
	assert.Empty(t, fields)

	// An undeclared tool declares nothing at all.
	_, ok = c.Policy().Declared("dispute_open")
	assert.False(t, ok)

	cs, ok := c.Policy().Constraints("ap-agent", "payment_schedule")
	require.True(t, ok)
	assert.Len(t, cs, 2)
	_, ok = c.Policy().Constraints("other-agent", "payment_schedule")
	assert.False(t, ok)
}

// Everything malformed is refused at LOAD — never ignored at call time.
func TestPolicyMalformedDeclarationsAreRefusedAtLoad(t *testing.T) {
	cases := map[string]string{
		"policy_fields missing":  `"erp": {"url": "http://erp.kaimahi:8080/mcp", "tools": {"pay": {}}}`,
		"invalid field name":     `"erp": {"url": "http://erp.kaimahi:8080/mcp", "tools": {"pay": {"policy_fields": ["amount cents"]}}}`,
		"nested field path":      `"erp": {"url": "http://erp.kaimahi:8080/mcp", "tools": {"pay": {"policy_fields": ["invoice.amount"]}}}`,
		"duplicate field":        `"erp": {"url": "http://erp.kaimahi:8080/mcp", "tools": {"pay": {"policy_fields": ["a", "a"]}}}`,
		"invalid tool name":      `"erp": {"url": "http://erp.kaimahi:8080/mcp", "tools": {"pay me": {"policy_fields": []}}}`,
		"conflicting across two": `"erp": {"url": "http://erp.kaimahi:8080/mcp", "tools": {"pay": {"policy_fields": ["a"]}}}, "erp2": {"url": "http://erp2.kaimahi:8080/mcp", "tools": {"pay": {"policy_fields": ["b"]}}}`,
	}
	for name, tu := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(base(tu, ""))
			require.Error(t, err)
		})
	}
}

func TestPolicyMalformedConstraintsAreRefusedAtLoad(t *testing.T) {
	cases := map[string]string{
		// The load-time error a standing constraint must produce: a
		// constraint on a field the tool does not declare, which must never
		// be a silently-ignored rule.
		"undeclared field":  `{"ap-agent": {"payment_schedule": [{"field": "memo", "op": "eq", "value": "x"}]}}`,
		"undeclared tool":   `{"ap-agent": {"dispute_open": [{"field": "amount_cents", "op": "lte", "value": 1}]}}`,
		"empty rule list":   `{"ap-agent": {"payment_schedule": []}}`,
		"unknown op":        `{"ap-agent": {"payment_schedule": [{"field": "amount_cents", "op": "under", "value": 1}]}}`,
		"in without values": `{"ap-agent": {"payment_schedule": [{"field": "payee_id", "op": "in"}]}}`,
		"in with a value":   `{"ap-agent": {"payment_schedule": [{"field": "payee_id", "op": "in", "value": "MER-4471"}]}}`,
		"eq with values":    `{"ap-agent": {"payment_schedule": [{"field": "payee_id", "op": "eq", "values": ["a"]}]}}`,
		"lte on a string":   `{"ap-agent": {"payment_schedule": [{"field": "amount_cents", "op": "lte", "value": "1000000"}]}}`,
		"non-integer bound": `{"ap-agent": {"payment_schedule": [{"field": "amount_cents", "op": "lte", "value": 1000.5}]}}`,
		"bad credential":    `{"AP Agent": {"payment_schedule": [{"field": "amount_cents", "op": "lte", "value": 1}]}}`,
		"unknown field key": `{"ap-agent": {"payment_schedule": [{"field": "amount_cents", "op": "lte", "value": 1, "unless": "x"}]}}`,
	}
	for name, sc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(base(erp, `, "standing_constraints": `+sc))
			require.Error(t, err)
		})
	}
}

// The boundary itself is the test: at, just under, and just over.
func TestSatisfiedBoundary(t *testing.T) {
	cs := rules(t, `[{"field": "amount_cents", "op": "lte", "value": 1000000}]`)
	for _, tc := range []struct {
		amount string
		want   bool
	}{
		{"999999", true},  // just under
		{"1000000", true}, // at the bound
		{"1000001", false},
	} {
		ok, why := Satisfied(cs, args(t, `{"amount_cents": `+tc.amount+`}`))
		assert.Equal(t, tc.want, ok, "amount %s", tc.amount)
		if !tc.want {
			assert.Equal(t, "amount_cents lte 1000000", why)
		}
	}
}

func TestSatisfiedFailsClosed(t *testing.T) {
	cs := rules(t, `[{"field": "amount_cents", "op": "lte", "value": 1000000}]`)
	for name, a := range map[string]string{
		"field missing":   `{"payee_id": "MER-4471"}`,
		"a string amount": `{"amount_cents": "1"}`,
		"a non-integer":   `{"amount_cents": 1.5}`,
		"an object":       `{"amount_cents": {"value": 1}}`,
		"null":            `{"amount_cents": null}`,
		"a boolean":       `{"amount_cents": true}`,
		"an empty call":   `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			ok, _ := Satisfied(cs, args(t, a))
			assert.False(t, ok)
		})
	}
}

func TestSatisfiedVocabulary(t *testing.T) {
	all := rules(t, `[
	  {"field": "amount_cents", "op": "gt", "value": 0},
	  {"field": "payee_id", "op": "in", "values": ["MER-4471", "ACME-1042"]},
	  {"field": "invoice_id", "op": "ne", "value": "INV-BLOCKED"}]`)

	ok, _ := Satisfied(all, args(t, `{"amount_cents": 1, "payee_id": "MER-4471", "invoice_id": "INV-1"}`))
	assert.True(t, ok)

	// Every rule must hold: one failure denies.
	ok, why := Satisfied(all, args(t, `{"amount_cents": 1, "payee_id": "EVIL-1", "invoice_id": "INV-1"}`))
	assert.False(t, ok)
	assert.Contains(t, why, "payee_id in")

	ok, _ = Satisfied(all, args(t, `{"amount_cents": 0, "payee_id": "MER-4471", "invoice_id": "INV-1"}`))
	assert.False(t, ok)

	ok, _ = Satisfied(all, args(t, `{"amount_cents": 1, "payee_id": "MER-4471", "invoice_id": "INV-BLOCKED"}`))
	assert.False(t, ok)

	notIn := rules(t, `[{"field": "payee_id", "op": "not_in", "values": ["EVIL-1"]}]`)
	ok, _ = Satisfied(notIn, args(t, `{"payee_id": "MER-4471"}`))
	assert.True(t, ok)
	ok, _ = Satisfied(notIn, args(t, `{"payee_id": "EVIL-1"}`))
	assert.False(t, ok)
	// not_in still requires the field to be present: an absent field is
	// not "not in the set", it is a call whose policy input is missing.
	ok, _ = Satisfied(notIn, args(t, `{}`))
	assert.False(t, ok)
}

func rules(t *testing.T, raw string) []Constraint {
	t.Helper()
	var cs []Constraint
	require.NoError(t, json.Unmarshal([]byte(raw), &cs))
	return cs
}

// args parses a call's arguments the way canon.go hands them over:
// numbers as json.Number.
func args(t *testing.T, raw string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var out map[string]any
	require.NoError(t, dec.Decode(&out))
	return out
}
