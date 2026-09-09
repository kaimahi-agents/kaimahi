package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

func TestParseValid(t *testing.T) {
	c, err := config.Parse([]byte(`{
	  "upstreams": {
	    "ollama": {"base_url": "http://ollama.ollama.svc:11434", "path": "v1/chat/completions", "classification": "free"},
	    "copilot": {"base_url": "https://api.githubcopilot.com", "path": "chat/completions",
	                "classification": "metered", "credential_file": "/etc/x/token", "internet": true,
	                "prices": {"gpt-5-mini": {"in_cents_per_1m": 25, "out_cents_per_1m": 200}}}
	  }
	}`))
	require.NoError(t, err)
	require.Len(t, c.Upstreams, 2)
	require.Equal(t, config.ClassFree, c.Upstreams["ollama"].Classification)
	require.Equal(t, 25, c.Upstreams["copilot"].Prices["gpt-5-mini"].InCentsPer1M)
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"no upstreams":         `{"upstreams": {}}`,
		"missing class":        `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions"}}}`,
		"inferred class":       `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "local"}}}`,
		"free with prices":     `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "free", "prices": {"m": {"in_cents_per_1m": 1, "out_cents_per_1m": 1}}}}}`,
		"bad base_url":         `{"upstreams": {"a": {"base_url": "not a url", "path": "v1/chat/completions", "classification": "free"}}}`,
		"leading-slash path":   `{"upstreams": {"a": {"base_url": "http://x", "path": "/p", "classification": "free"}}}`,
		"empty path":           `{"upstreams": {"a": {"base_url": "http://x", "path": "", "classification": "free"}}}`,
		"negative price":       `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "metered", "prices": {"m": {"in_cents_per_1m": -1, "out_cents_per_1m": 1}}}}}`,
		"unknown field (typo)": `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "free", "credental_file": "x"}}}`,
	}
	for name, raw := range cases {
		_, err := config.Parse([]byte(raw))
		require.Error(t, err, name)
	}
}

func TestParseToolUpstreams(t *testing.T) {
	c, err := config.Parse([]byte(`{
	  "upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {"kagent-tools": {"url": "http://kagent-tools.kagent:8084/mcp"}}
	}`))
	require.NoError(t, err)
	require.Equal(t, "http://kagent-tools.kagent:8084/mcp", c.ToolUpstreams["kagent-tools"].URL)

	// Optional: a config with only LLM upstreams still parses.
	c, err = config.Parse([]byte(`{"upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}}}`))
	require.NoError(t, err)
	require.Empty(t, c.ToolUpstreams)

	base := `{"upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}}, "tool_upstreams": `
	for name, bad := range map[string]string{
		"empty url":     `{"t": {"url": ""}}`,
		"relative url":  `{"t": {"url": "not-a-url"}}`,
		"non-http":      `{"t": {"url": "ftp://x/mcp"}}`,
		"unknown field": `{"t": {"url": "http://x/mcp", "extra": true}}`,
	} {
		_, err := config.Parse([]byte(base + bad + `}`))
		require.Error(t, err, name)
	}
}

// A tool upstream may carry its OWN credential (the Slack MCP
// server's SLACK_MCP_API_KEY), named — never valued — in the committed
// table, exactly like the LLM upstreams' credential_file.
func TestParseKeyedToolUpstreams(t *testing.T) {
	c, err := config.Parse([]byte(`{
	  "upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {"slack": {
	    "url": "http://kaimahi-slack-mcp.kaimahi:13080/mcp",
	    "credential_file": "/etc/kaimahi/upstream-creds/slack/mcp-api-key",
	    "credential_header": "Authorization"
	  }}
	}`))
	require.NoError(t, err)
	require.Equal(t, "/etc/kaimahi/upstream-creds/slack/mcp-api-key", c.ToolUpstreams["slack"].CredentialFile)
	require.Equal(t, "Authorization", c.ToolUpstreams["slack"].CredentialHeader)

	base := `{"upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}}, "tool_upstreams": `
	for name, bad := range map[string]string{
		// A header with no file would silently forward bare — the
		// confusing direction of fail-open. Reject at load.
		"header without file": `{"t": {"url": "http://x/mcp", "credential_header": "Authorization"}}`,
		"malformed header":    `{"t": {"url": "http://x/mcp", "credential_file": "/f", "credential_header": "X Api Key"}}`,
		// Key material never belongs in the committed table.
		"inline credential": `{"t": {"url": "http://x/mcp", "credential": "xoxb-secret"}}`,
	} {
		_, err := config.Parse([]byte(base + bad + `}`))
		require.Error(t, err, name)
	}
}

const inboundBase = `"upstreams": {"ollama": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "free"}}`

func TestParseInboundHooks(t *testing.T) {
	c, err := config.Parse([]byte(`{` + inboundBase + `, "inbound_hooks": {
	  "demo": {"credential": "inbound-demo", "auth": "kaimahi-hmac", "signing_secret_file": "/etc/kaimahi/inbound/demo",
	           "agent_namespace": "kagent", "agent": "hello-world", "budget_credential": "hello-world"},
	  "slack": {"credential": "inbound-slack", "auth": "slack", "signing_secret_file": "/etc/kaimahi/inbound/slack-events",
	            "slack_channels_file": "/etc/kaimahi/slack/channel", "slack_approvers_file": "/etc/kaimahi/slack/approvers",
	            "agent_namespace": "kagent", "agent": "hello-slack", "budget_credential": "hello-world"},
	  "slack2": {"credential": "inbound-slack", "auth": "slack", "signing_secret_file": "/etc/kaimahi/inbound/slack-events",
	            "slack_channels_file": "/etc/kaimahi/slack/channel", "slack_approvers_file": "/etc/kaimahi/slack/approvers",
	            "slack_default_uses": 3, "slack_default_ttl": "2h",
	            "agent_namespace": "kagent", "agent": "hello-slack", "budget_credential": "hello-world"},
	  "plain": {"credential": "inbound-plain", "auth": "bearer",
	            "agent_namespace": "kagent", "agent": "hello-world", "budget_credential": "hello-world",
	            "max_body_bytes": 1024, "rate_per_minute": 5, "burst": 2}
	}}`))
	require.NoError(t, err)
	require.Len(t, c.InboundHooks, 4)
	require.Equal(t, "/etc/kaimahi/slack/channel", c.InboundHooks["slack"].SlackChannelsFile)
	require.Equal(t, "/etc/kaimahi/slack/approvers", c.InboundHooks["slack"].SlackApproversFile)
	// A Slack approval that names no bounds gets the hook's
	// defaults — one use, 15 minutes unless the table says otherwise.
	slack := c.InboundHooks["slack"].Bounded()
	require.Equal(t, config.DefaultSlackUses, slack.SlackDefaultUses)
	require.Equal(t, config.DefaultSlackTTL, slack.SlackDefaultTTL)
	slack2 := c.InboundHooks["slack2"].Bounded()
	require.Equal(t, 3, slack2.SlackDefaultUses)
	require.Equal(t, "2h", slack2.SlackDefaultTTL)
	demo := c.InboundHooks["demo"].Bounded()
	require.Equal(t, int64(config.DefaultInboundMaxBody), demo.MaxBodyBytes)
	require.Equal(t, config.DefaultInboundRate, demo.RatePerMinute)
	require.Equal(t, config.DefaultInboundBurst, demo.Burst)
	plain := c.InboundHooks["plain"].Bounded()
	require.Equal(t, int64(1024), plain.MaxBodyBytes)
	require.Equal(t, 5, plain.RatePerMinute)
	require.Equal(t, 2, plain.Burst)
}

func TestParseInboundHooksRejects(t *testing.T) {
	hook := func(fields string) string {
		return `{` + inboundBase + `, "inbound_hooks": {"demo": {` + fields + `}}}`
	}
	good := `"credential": "c", "auth": "bearer", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`
	cases := map[string]string{
		"unknown auth":               hook(`"credential": "c", "auth": "basic", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`),
		"hmac without secret file":   hook(`"credential": "c", "auth": "kaimahi-hmac", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`),
		"slack without secret file":  hook(`"credential": "c", "auth": "slack", "slack_channels_file": "/y", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`),
		"slack without channels":     hook(`"credential": "c", "auth": "slack", "signing_secret_file": "/x", "slack_approvers_file": "/z", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`),
		"slack without approvers":    hook(`"credential": "c", "auth": "slack", "signing_secret_file": "/x", "slack_channels_file": "/y", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`),
		"approvers file off slack":   hook(good + `, "slack_approvers_file": "/x"`),
		"slack defaults off slack":   hook(good + `, "slack_default_uses": 2`),
		"bad slack default ttl":      hook(`"credential": "c", "auth": "slack", "signing_secret_file": "/x", "slack_channels_file": "/y", "slack_approvers_file": "/z", "slack_default_ttl": "soon", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`),
		"slack default ttl too long": hook(`"credential": "c", "auth": "slack", "signing_secret_file": "/x", "slack_channels_file": "/y", "slack_approvers_file": "/z", "slack_default_ttl": "31d", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`),
		"bearer with secret file":    hook(good + `, "signing_secret_file": "/x"`),
		"channels file off slack":    hook(good + `, "slack_channels_file": "/x"`),
		"missing credential":         hook(`"auth": "bearer", "agent_namespace": "kagent", "agent": "a", "budget_credential": "b"`),
		"missing agent":              hook(`"credential": "c", "auth": "bearer", "agent_namespace": "kagent", "budget_credential": "b"`),
		"uppercase agent":            hook(`"credential": "c", "auth": "bearer", "agent_namespace": "kagent", "agent": "Hello", "budget_credential": "b"`),
		"body bound too large":       hook(good + `, "max_body_bytes": 99999999`),
		"negative rate":              hook(good + `, "rate_per_minute": -1`),
		"unknown field (typo)":       hook(good + `, "signing_secret": "x"`),
		"bad hook name":              `{` + inboundBase + `, "inbound_hooks": {"Demo Hook": {` + good + `}}}`,
	}
	for name, raw := range cases {
		_, err := config.Parse([]byte(raw))
		require.Error(t, err, name)
	}
}

func TestParseApprovalNotifier(t *testing.T) {
	base := `"upstreams": {"ollama": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {"slack": {"url": "http://kaimahi-slack-mcp.kaimahi:13080/mcp"}}`
	c, err := config.Parse([]byte(`{` + base + `, "approval_notifier": {"tool_upstream": "slack",
	  "tool": "conversations_add_message", "credential_file": "/etc/kaimahi/notifier/api-key",
	  "channel_file": "/etc/kaimahi/slack/channel"}}`))
	require.NoError(t, err)
	require.NotNil(t, c.ApprovalNotifier)
	require.Equal(t, "conversations_add_message", c.ApprovalNotifier.Tool)

	// Absent is fine (nobody is told, as before).
	c, err = config.Parse([]byte(`{` + base + `}`))
	require.NoError(t, err)
	require.Nil(t, c.ApprovalNotifier)

	for name, raw := range map[string]string{
		"upstream not in table": `{` + base + `, "approval_notifier": {"tool_upstream": "discord", "tool": "t", "credential_file": "/a", "channel_file": "/b"}}`,
		"no credential file":    `{` + base + `, "approval_notifier": {"tool_upstream": "slack", "tool": "t", "channel_file": "/b"}}`,
		"no channel file":       `{` + base + `, "approval_notifier": {"tool_upstream": "slack", "tool": "t", "credential_file": "/a"}}`,
		"bad tool name":         `{` + base + `, "approval_notifier": {"tool_upstream": "slack", "tool": "no spaces", "credential_file": "/a", "channel_file": "/b"}}`,
		"unknown field (typo)":  `{` + base + `, "approval_notifier": {"tool_upstream": "slack", "tool": "t", "credential_file": "/a", "channel_file": "/b", "token": "x"}}`,
	} {
		_, err := config.Parse([]byte(raw))
		require.Error(t, err, name)
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90": 90 * time.Second, "90s": 90 * time.Second, "5m": 5 * time.Minute, "2h": 2 * time.Hour, "1d": 24 * time.Hour, "30d": 30 * 24 * time.Hour,
	} {
		got, err := config.ParseTTL(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "m", "5x", "-5m", "0", "31d", "1.5h", "5 m", "9999999999s", "999999999d", "999999999h"} {
		_, err := config.ParseTTL(in)
		require.Error(t, err, in)
	}
}
