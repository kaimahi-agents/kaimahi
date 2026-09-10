package convert

import (
	"strings"
	"testing"
)

// A generated route must not get past conversion only to be refused by adapter.New.
func TestRefusesAdapterReservedHeadersAndPathKeys(t *testing.T) {
	input := string(fixture(t, "literal.yaml"))
	schemas := fixture(t, "schemas.json")
	for _, header := range []string{"Accept-Encoding", "Keep-Alive", "Proxy-Authorization", "Proxy-Connection", "Idempotency-Key", "X-Idempotency-Key", "Mcp-Extension"} {
		t.Run(header, func(t *testing.T) {
			out, err := Convert([]byte(strings.Replace(input, "name: Authorization", "name: "+header, 1)), schemas)
			if err == nil || len(out) != 0 {
				t.Fatalf("reserved adapter header accepted: %s", header)
			}
		})
	}
	for _, key := range []string{".", "..", "..data", "..token"} {
		t.Run(key, func(t *testing.T) {
			out, err := Convert([]byte(strings.Replace(input, "key: token", "key: "+key, 1)), schemas)
			if err == nil || len(out) != 0 {
				t.Fatalf("unsafe mounted key accepted: %s", key)
			}
		})
	}
}
