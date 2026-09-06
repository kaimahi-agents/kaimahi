package config_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

// A tool upstream may carry committed, non-secret headers — the
// lever that narrows what a hosted server we did not write is willing to
// offer (X-MCP-Toolsets and friends). The rules are load-time, because a
// header that fails open at the first call is a header nobody notices.
func TestToolUpstreamExtraHeaders(t *testing.T) {
	base := `{"upstreams": {"o": {"base_url": "http://o", "path": "p", "classification": "free"}}, "tool_upstreams": `

	t.Run("accepted and parsed", func(t *testing.T) {
		c, err := config.Parse([]byte(base + `{"gh": {"url": "https://api.githubcopilot.com/mcp/", "internet": true,
			"credential_file": "/etc/k/t",
			"extra_headers": {"X-MCP-Toolsets": "repos,actions", "X-MCP-Readonly": "true"}}}}`))
		require.NoError(t, err)
		require.Equal(t, map[string]string{"X-MCP-Toolsets": "repos,actions", "X-MCP-Readonly": "true"},
			c.ToolUpstreams["gh"].ExtraHeaders)
	})

	// The whole point of the load-time rule: a committed header must
	// never be able to displace a credential the plane holds in custody.
	bad := map[string]string{
		"displaces the default credential slot": `{"gh": {"url": "https://h.example/mcp", "internet": true,
			"credential_file": "/etc/k/t", "extra_headers": {"Authorization": "Bearer nope"}}}`,
		"displaces it in another case": `{"gh": {"url": "https://h.example/mcp", "internet": true,
			"credential_file": "/etc/k/t", "extra_headers": {"authorization": "Bearer nope"}}}`,
		"displaces a named credential slot": `{"gh": {"url": "https://h.example/mcp", "internet": true,
			"credential_file": "/etc/k/t", "credential_header": "X-Api-Key",
			"extra_headers": {"x-api-key": "nope"}}}`,
		// Authorization is refused even on a keyless upstream: an
		// upstream that gains a credential later must not silently keep
		// a committed one that outranks it.
		"authorization on a keyless upstream": `{"t": {"url": "http://tools.kagent:8084/mcp",
			"extra_headers": {"Authorization": "Bearer nope"}}}`,
		"header name with a space": `{"t": {"url": "http://tools.kagent:8084/mcp",
			"extra_headers": {"X Bad": "v"}}}`,
		"header name with a colon": `{"t": {"url": "http://tools.kagent:8084/mcp",
			"extra_headers": {"X:Bad": "v"}}}`,
		"empty header name": `{"t": {"url": "http://tools.kagent:8084/mcp",
			"extra_headers": {"": "v"}}}`,
	}
	for name, in := range bad {
		_, err := config.Parse([]byte(base + in + "}"))
		require.Error(t, err, name)
	}
}

// The committed table is the thing that actually ships. Parsing it here
// means a typo in k8s/plane/upstreams.yaml fails `go test` rather than a
// rollout — the loader is strict (DisallowUnknownFields), so an entry
// using a key the plane does not have is a boot failure otherwise.
func TestCommittedUpstreamTableLoads(t *testing.T) {
	raw, err := os.ReadFile("../../../k8s/plane/upstreams.yaml")
	require.NoError(t, err)
	body := literalBlock(t, string(raw), "upstreams.json")
	require.True(t, json.Valid([]byte(body)), "upstreams.json must be valid JSON")

	c, err := config.Parse([]byte(body))
	require.NoError(t, err, "the committed table must load")

	// The three hosted seams, and the fact that every one of them names
	// its credential rather than carrying it.
	for _, name := range []string{"github", "github-release", "ado"} {
		up, ok := c.ToolUpstreams[name]
		require.True(t, ok, "tool upstream %q", name)
		require.True(t, up.Internet, "%q must be marked hosted", name)
		require.NotEmpty(t, up.CredentialFile, "%q must NAME a credential", name)
		require.True(t, strings.HasPrefix(up.CredentialFile, "/etc/kaimahi/upstream-creds/"),
			"%q credential must come from the custody mount", name)
	}

	// Both release seams reach servers whose consequential tools
	// are CONSOLIDATED DISPATCHERS — one tool name, an `action`/`method`
	// argument selecting what it does. Binding the dispatcher argument is
	// not optional there: without it, approving "run a pipeline" also
	// approves "create a pipeline".
	dispatchers := map[string]string{
		"pipelines_write":     "action",
		"pipelines_build":     "action",
		"actions_run_trigger": "method",
		"actions_list":        "method",
		"actions_get":         "method",
	}
	for tool, selector := range dispatchers {
		fields, ok := c.Policy().Declared(tool)
		require.True(t, ok, "%q must declare policy fields", tool)
		require.Equal(t, selector, fields[0],
			"%q must bind %q FIRST, so the audit summary reads as the verb it really is", tool, selector)
	}

	// The two tools that change the world on a real repository bind the
	// artifact, not just the verb.
	branch, ok := c.Policy().Declared("create_branch")
	require.True(t, ok)
	require.Equal(t, []string{"owner", "repo", "branch", "from_branch"}, branch)
	trigger, ok := c.Policy().Declared("actions_run_trigger")
	require.True(t, ok)
	require.Equal(t, []string{"method", "owner", "repo", "workflow_id", "ref"}, trigger)
}

// literalBlock pulls one `key: |` literal block out of a ConfigMap
// without a YAML parser — the plane module has no direct YAML dependency
// and this test is not worth acquiring one for. It reads the ONLY shape
// the committed file uses (a block scalar whose body is indented further
// than its key) and fails loudly on anything else, rather than returning
// a partial document that would make the parse below pass vacuously.
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
