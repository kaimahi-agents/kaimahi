package runtime

import (
	"strings"
	"testing"
)

func mustReplace(t *testing.T, doc, old, replacement string) string {
	t.Helper()
	if strings.Count(doc, old) != 1 {
		t.Fatalf("fixture does not contain %q exactly once", old)
	}
	return strings.Replace(doc, old, replacement, 1)
}

// A baseURL is the one reference that could carry a credential inside it, so
// the refusal must never quote it.
func TestParseOrkaBindingsRejectsMalformedBaseURLsWithoutEchoingThem(t *testing.T) {
	for _, tc := range []struct{ name, baseURL string }{
		{"not a URL", "models.example.invalid"},
		{"not http", "ftp://models.example.invalid"},
		{"no host", "https://"},
		{"embedded credentials", "https://someone:a-passphrase@models.example.invalid"},
		{"query string", "https://models.example.invalid/?token=opaque"},
		{"fragment", "https://models.example.invalid/#frag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, validOrkaBindingsYAML, "  baseURL: https://models.example.invalid\n",
				"  baseURL: \""+tc.baseURL+"\"\n")
			_, err := ParseOrkaBindings([]byte(doc))
			if err == nil || !strings.Contains(err.Error(), "baseURL") {
				t.Fatalf("invalid URL accepted or wrong error: %v", err)
			}
			if strings.Contains(err.Error(), tc.baseURL) {
				t.Errorf("the refusal echoed the URL: %v", err)
			}
		})
	}
}
