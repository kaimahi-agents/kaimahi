package scaffold

import (
	"strings"
	"testing"
)

func TestSharedNamespaceAndObjectNameBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		namespaceOK, objectNameOK bool
	}{
		{"a", true, true},
		{"7", true, true},
		{"agent-7", true, true},
		{"agent.example", false, true},
		{strings.Repeat("a", 63), true, true},
		{strings.Repeat("a", 64), false, true},
		{strings.Repeat("a", 253), false, true},
		{strings.Repeat("a", 254), false, false},
		{"", false, false},
		{"Agent", false, false},
		{"agent_key", false, false},
		{"-agent", false, false},
		{"agent-", false, false},
		{"agent..example", false, false},
		{"agent.-example", false, false},
		{"agent.example-", false, false},
		{"agent/example", false, false},
		{"agent\nkind: Secret", false, false},
		{"agent key", false, false},
		{"билл", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateNamespace(tc.name); (err == nil) != tc.namespaceOK {
				t.Errorf("namespace validity = %t, want %t: %v", err == nil, tc.namespaceOK, err)
			} else if err != nil && !strings.Contains(err.Error(), "namespace name (RFC 1123 label)") {
				t.Errorf("namespace refusal lost classification: %v", err)
			}
			if err := ValidateObjectName(tc.name); (err == nil) != tc.objectNameOK {
				t.Errorf("object name validity = %t, want %t: %v", err == nil, tc.objectNameOK, err)
			} else if err != nil && !strings.Contains(err.Error(), "object name (RFC 1123 subdomain") {
				t.Errorf("object refusal lost classification: %v", err)
			}
		})
	}
}
