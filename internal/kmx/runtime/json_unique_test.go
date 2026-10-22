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

func manyDistinctKeys(n int) []byte {
	var data strings.Builder
	data.WriteString("{")
	for i := range n {
		fmt.Fprintf(&data, "%q:0,", fmt.Sprintf("k%d", i))
	}
	data.WriteString(`"end":0}`)
	return []byte(data.String())
}

func TestJSONKeysUniqueManyKeysIsLinear(t *testing.T) {
	data := manyDistinctKeys(120000)
	start := time.Now()
	if !JSONKeysUnique(data) {
		t.Fatal("distinct keys refused")
	}
	// Each key costs one set lookup, so 120000 keys finish well under a
	// second even under -race; the generous bound only catches a regression
	// to per-key work proportional to the object size.
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("checking %d bytes took %s", len(data), elapsed)
	}
}

func BenchmarkJSONKeysUnique(b *testing.B) {
	data := manyDistinctKeys(10000)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if !JSONKeysUnique(data) {
			b.Fatal("distinct keys refused")
		}
	}
}
