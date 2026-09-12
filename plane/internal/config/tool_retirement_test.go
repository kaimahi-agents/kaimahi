package config_test

import (
	"testing"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/stretchr/testify/require"
)

// Removing enforcement must not turn an old policy into accepted, ignored config.
func TestRetiredToolConfigIsRejected(t *testing.T) {
	const base = `{"upstreams":{"o":{"base_url":"http://o","path":"v1/chat/completions","classification":"free"}}}`
	for _, field := range []string{"tool_upstreams", "standing_constraints", "Tool_Upstreams", "Standing_Constraints"} {
		for _, value := range []string{`{}`, `null`, `{"old":{}}`} {
			t.Run(field+"/"+value, func(t *testing.T) {
				raw := []byte(base[:len(base)-1] + `,"` + field + `":` + value + `}`)
				_, err := config.Parse(raw)
				require.ErrorContains(t, err, `unknown field "`+field+`"`)
				merged, err := config.Merge(raw, nil)
				require.NoError(t, err)
				_, err = config.Parse(merged)
				require.ErrorContains(t, err, `unknown field "`+field+`"`)
				_, err = config.Merge([]byte(base), []config.Fragment{{Name: "retired.json", Raw: []byte(`{"` + field + `":` + value + `}`)}})
				require.ErrorContains(t, err, field)
			})
		}
	}
}
