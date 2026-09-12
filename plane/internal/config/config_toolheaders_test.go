package config_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/stretchr/testify/require"
)

// Exercise the actual shipped model table with the strict boot parser.
func TestCommittedUpstreamTableLoads(t *testing.T) {
	raw, err := os.ReadFile("../../../k8s/plane/upstreams.yaml")
	require.NoError(t, err)
	body := literalBlock(t, string(raw), "upstreams.json")
	require.True(t, json.Valid([]byte(body)))
	c, err := config.Parse([]byte(body))
	require.NoError(t, err)
	require.NotEmpty(t, c.Upstreams)
	up, ok := c.Upstreams["copilot"]
	require.True(t, ok)
	require.True(t, up.Internet)
	require.True(t, strings.HasPrefix(up.CredentialFile, "/etc/kaimahi/upstream-creds/"))
}

// literalBlock reads the committed ConfigMap's block-scalar shape without
// adding a YAML dependency. A missing or empty block fails loudly.
func literalBlock(t *testing.T, doc, key string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	start, indent := -1, 0
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == key+": |" {
			start, indent = i+1, len(line)-len(trimmed)
			break
		}
	}
	require.NotEqual(t, -1, start, "no %q literal block in the document", key)
	var out []string
	for _, line := range lines[start:] {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		if len(line)-len(strings.TrimLeft(line, " ")) <= indent {
			break
		}
		out = append(out, line[indent+2:])
	}
	require.NotEmpty(t, out, "the %q block is empty", key)
	return strings.Join(out, "\n")
}
