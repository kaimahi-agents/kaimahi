package kaimahi

import "testing"

func TestFileCheckFailure(t *testing.T) {
	for _, tc := range []struct{ kind, value, want string }{
		{"contains", "Hello", `Missing "Hello"`},
		{"notContains", "Bye", `Found "Bye"`},
		{"regex", "a+b", "No match for a+b"},
		{"toolCalled", "search", "Not met: Calls search"},
		{"toolNotCalled", "search", "Not met: Doesn't call search"},
		{"contains", "$1 {value}", `Missing "$1 {value}"`},
		{"llmJudge", "x", ""},
		{"unknown", "x", ""},
	} {
		if got := FileCheckFailure(tc.kind, tc.value); got != tc.want {
			t.Errorf("FileCheckFailure(%q, %q) = %q, want %q", tc.kind, tc.value, got, tc.want)
		}
	}
}
