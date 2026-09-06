package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func argsOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	_, msg, err := canonicalize([]byte(`{"method":"tools/call","params":{"name":"x","arguments":` + raw + `}}`))
	require.NoError(t, err)
	args, ok := argumentsOf(msg)
	require.True(t, ok)
	return args
}

var payFields = []string{"invoice_id", "amount_cents", "payee_id"}

// The declared fields are what an approval binds: a call that differs in
// one of them is a DIFFERENT call, and a call that differs only outside
// them is the same one. That second half is what keeps the binding from
// being brittle — an LLM re-emitting a semantically identical call
// is not byte-stable.
func TestDigestBindsDeclaredFieldsOnly(t *testing.T) {
	approved := Bind("pay", argsOf(t, `{"invoice_id":"INV-1","amount_cents":3255000,"payee_id":"MER-4471"}`), payFields, true)

	same := Bind("pay", argsOf(t, `{"payee_id":"MER-4471","amount_cents":3255000,"invoice_id":"INV-1","memo":"re-emitted"}`), payFields, true)
	assert.Equal(t, approved.Digest, same.Digest, "argument order and undeclared fields must not change the binding")

	for name, other := range map[string]string{
		"a different amount": `{"invoice_id":"INV-1","amount_cents":4800000,"payee_id":"MER-4471"}`,
		"a different payee":  `{"invoice_id":"INV-1","amount_cents":3255000,"payee_id":"EVIL-1"}`,
		"a dropped field":    `{"invoice_id":"INV-1","amount_cents":3255000}`,
		"a nulled field":     `{"invoice_id":"INV-1","amount_cents":3255000,"payee_id":null}`,
		"a string amount":    `{"invoice_id":"INV-1","amount_cents":"3255000","payee_id":"MER-4471"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.NotEqual(t, approved.Digest, Bind("pay", argsOf(t, other), payFields, true).Digest)
		})
	}

	// The tool is inside the digest: the same arguments to another tool
	// are another call.
	assert.NotEqual(t, approved.Digest,
		Bind("dispute_open", argsOf(t, `{"invoice_id":"INV-1","amount_cents":3255000,"payee_id":"MER-4471"}`), payFields, true).Digest)
}

func TestDigestWithoutADeclarationBindsTheWholeCall(t *testing.T) {
	a := Bind("echo", argsOf(t, `{"text":"hi","n":1}`), nil, false)
	b := Bind("echo", argsOf(t, `{"n":1,"text":"hi"}`), nil, false)
	assert.Equal(t, a.Digest, b.Digest, "canonical form, so key order is not a difference")

	// Brittle by construction: any argument at all changes the call.
	c := Bind("echo", argsOf(t, `{"text":"hi","n":1,"trace":"x"}`), nil, false)
	assert.NotEqual(t, a.Digest, c.Digest)

	// A whole-object binding and a declared-field binding of the same
	// arguments are different bindings, never a collision.
	assert.NotEqual(t, a.Digest, Bind("echo", argsOf(t, `{"text":"hi","n":1}`), []string{"text", "n"}, true).Digest)
}

// An explicitly empty declaration is verb-level: the arguments do not
// enter the digest at all.
func TestDigestWithAnEmptyDeclarationIsVerbLevel(t *testing.T) {
	a := Bind("invoice_get", argsOf(t, `{"invoice_id":"INV-1"}`), []string{}, true)
	b := Bind("invoice_get", argsOf(t, `{"invoice_id":"INV-2"}`), []string{}, true)
	assert.Equal(t, a.Digest, b.Digest)
	assert.Equal(t, "invoice_get: (no policy-relevant arguments)", a.Summary)
}

// The summary is the transaction a human approves — and it carries
// declared fields ONLY: the audit is in every pg_dump.
func TestSummaryCarriesDeclaredFieldsOnly(t *testing.T) {
	b := Bind("pay", argsOf(t,
		`{"invoice_id":"INV-88134","amount_cents":3255000,"payee_id":"MER-4471","bank_account":"12-3456-7890123-00"}`),
		payFields, true)
	assert.Equal(t, "pay: invoice_id INV-88134, amount_cents 3255000, payee_id MER-4471", b.Summary)
	assert.NotContains(t, b.Summary, "12-3456")
	assert.NotContains(t, b.Summary, "bank_account")

	// An undeclared tool names itself and nothing else.
	u := Bind("echo", argsOf(t, `{"secret_note":"do not persist me"}`), nil, false)
	assert.NotContains(t, u.Summary, "do not persist me")
	assert.NotContains(t, u.Summary, "secret_note")
	assert.Contains(t, u.Summary, "echo")
}

func TestSummaryIsOneBoundedPrintableLine(t *testing.T) {
	long := `"` + strings.Repeat("A", 500) + `"`
	b := Bind("pay", argsOf(t, `{"invoice_id":`+long+`,"amount_cents":1,"payee_id":"x\ny\u0007z"}`), payFields, true)
	assert.LessOrEqual(t, len(b.Summary), maxSummary)
	assert.NotContains(t, b.Summary, "\n")
	assert.NotContains(t, b.Summary, "\a")

	// Nested values are named, never inlined.
	n := Bind("pay", argsOf(t, `{"invoice_id":{"id":"INV-1"},"amount_cents":[1],"payee_id":true}`), payFields, true)
	assert.Equal(t, "pay: invoice_id (object), amount_cents (list), payee_id true", n.Summary)

	// Declared-but-absent fields are simply not there.
	assert.Equal(t, "pay: amount_cents 1", Bind("pay", argsOf(t, `{"amount_cents":1}`), payFields, true).Summary)
	assert.Equal(t, "pay: (no declared argument present)", Bind("pay", argsOf(t, `{"memo":"x"}`), payFields, true).Summary)
}

// Truncation must not cut a rune in half: the summary lands in an
// approval request, an audit row and a Slack message.
func TestSummaryTruncationIsRuneSafe(t *testing.T) {
	long := `"` + strings.Repeat("kōrero", 200) + `"`
	b := Bind("pay", argsOf(t, `{"invoice_id":`+long+`,"amount_cents":1,"payee_id":`+long+`}`), payFields, true)
	assert.True(t, utf8.ValidString(b.Summary), "summary must be valid UTF-8")
	assert.LessOrEqual(t, len(b.Summary), maxSummary)
	assert.True(t, strings.HasSuffix(b.Summary, "…"))

	// One value's own bound holds too, ellipsis included.
	v := clip(strings.Repeat("ā", 100))
	assert.True(t, utf8.ValidString(v))
	assert.LessOrEqual(t, len(v), maxSummaryValue)
}

// BindArguments is the admin surface's path: the operator names the call
// instead of the gateway seeing it, and gets the SAME digest.
func TestBindArgumentsMatchesTheGatewaysOwn(t *testing.T) {
	fromWire := Bind("pay", argsOf(t, `{"invoice_id":"INV-1","amount_cents":10,"payee_id":"MER-4471"}`), payFields, true)
	fromAdmin, err := BindArguments("pay", []byte(`{"payee_id":"MER-4471","invoice_id":"INV-1","amount_cents":10}`), payFields, true)
	require.NoError(t, err)
	assert.Equal(t, fromWire.Digest, fromAdmin.Digest)

	// No arguments at all is the argument-less call, not "any call".
	empty, err := BindArguments("pay", nil, payFields, true)
	require.NoError(t, err)
	assert.Equal(t, Bind("pay", map[string]any{}, payFields, true).Digest, empty.Digest)
	assert.NotEqual(t, fromWire.Digest, empty.Digest)

	// Malformed or non-object arguments are refused, never bound blindly.
	_, err = BindArguments("pay", []byte(`{"a":1,"a":2}`), payFields, true)
	require.ErrorIs(t, err, errDuplicateKey)
	_, err = BindArguments("pay", []byte(`[1]`), payFields, true)
	require.Error(t, err)
}
