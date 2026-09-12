package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

// A stale base table must stop rollout, not boot a plane that silently
// ignores the connector or notification it still declares.
func TestRetiredConnectorConfigIsRejected(t *testing.T) {
	base := `"upstreams":{"o":{"base_url":"http://o","path":"v1/chat/completions","classification":"free"}}`
	for _, tc := range []struct{ name, field, value string }{
		{"empty hooks", "inbound_hooks", `{}`},
		{"null hooks", "inbound_hooks", `null`},
		{"configured hook", "inbound_hooks", `{"demo":{"credential":"hook","auth":"bearer","agent_namespace":"kagent","agent":"demo","budget_credential":"demo"}}`},
		{"null notifier", "approval_notifier", `null`},
		{"configured notifier", "approval_notifier", `{"tool_upstream":"slack","tool":"post","credential_file":"/token","channel_file":"/channel"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{` + base + `,"` + tc.field + `":` + tc.value + `}`)
			for _, merge := range []bool{false, true} {
				candidate := raw
				if merge {
					var err error
					candidate, err = config.Merge(raw, nil)
					require.NoError(t, err)
				}
				_, err := config.Parse(candidate)
				require.ErrorContains(t, err, `unknown field "`+tc.field+`"`)
			}
		})
	}
}
