package runtime

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestJSONKeysUnique(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want bool
	}{
		{"object", `{"a":1,"b":{"a":2},"c":[{"a":3},{"a":4}]}`, true},
		{"scalar", `"x"`, true},
		{"top-level duplicate", `{"a":1,"a":2}`, false},
		{"nested duplicate", `{"choices":[{"message":{"tool_calls":[{"id":"c"}],"tool_calls":null}}]}`, false},
		{"case alias", `{"finish_reason":"length","Finish_Reason":"stop"}`, false},
		{"kelvin alias", "{\"k\":1,\"K\":2}", false},
		{"long s alias", "{\"s\":1,\"ſ\":2}", false},
		{"escaped alias", `{"role":"assistant","Role":"tool"}`, false},
		{"distinct after fold", `{"ab":1,"a b":2}`, true},
		{"trailing data", `{"a":1}{"a":2}`, false},
		{"truncated", `{"a":1`, false},
		{"invalid utf8", "{\"a\":\"\xff\"}", false},
		{"too deep", strings.Repeat("[", maxJSONUniqueDepth+2) + strings.Repeat("]", maxJSONUniqueDepth+2), false},
		{"deepest allowed", strings.Repeat("[", maxJSONUniqueDepth+1) + strings.Repeat("]", maxJSONUniqueDepth+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := JSONKeysUnique([]byte(tc.data)); got != tc.want {
				t.Fatalf("JSONKeysUnique(%q) = %t, want %t", tc.data, got, tc.want)
			}
		})
	}
}

func TestFoldJSONKeyMatchesEqualFold(t *testing.T) {
	runes := []rune{'a', 'A', 'k', 'K', 'K', 's', 'S', 'ſ', 'ß', 'ẞ', 'µ', 'Μ', 'μ', 'ς', 'σ', 'Σ', 'İ', 'ı', 'i', 'I', '_', '1', '�'}
	for _, a := range runes {
		for _, b := range runes {
			x, y := "k"+string(a), "K"+string(b)
			if got, want := foldJSONKey(x) == foldJSONKey(y), strings.EqualFold(x, y); got != want {
				t.Errorf("%q vs %q: fold equal %t, EqualFold %t", x, y, got, want)
			}
		}
	}
}

func TestJSONKeysUniqueManyKeysIsLinear(t *testing.T) {
	var data strings.Builder
	data.WriteString("{")
	for i := range 120000 {
		fmt.Fprintf(&data, "%q:0,", fmt.Sprintf("k%d", i))
	}
	data.WriteString(`"end":0}`)
	start := time.Now()
	if !JSONKeysUnique([]byte(data.String())) {
		t.Fatal("distinct keys refused")
	}
	// Pairwise comparison of 120000 keys takes minutes; a set lookup takes
	// well under a second even under -race.
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("checking %d bytes took %s", data.Len(), elapsed)
	}
}
