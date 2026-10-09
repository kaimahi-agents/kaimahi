package agentsessions

import (
	"strings"
	"testing"
)

// Catch address comparison that resolves aliases or compares raw spellings.
func TestNormalizeAddress(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"HOST.Example.:08080", "host.example:8080"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"[0:0:0:0:0:0:0:1]:08080", "[::1]:8080"},
		{"[::ffff:127.0.0.1]:8080", "127.0.0.1:8080"},
		{"localhost:8080", "localhost:8080"},
	} {
		got, err := NormalizeAddress(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"", "host", "host:0", "host:65536", "http://host:8080", " host:8080", "host:8x", "[fe80::1%eth0]:8080", "bad_host:8080", strings.Repeat("a", 64) + ":8080"} {
		if _, err := NormalizeAddress(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
