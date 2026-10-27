package app

import (
	"errors"
	"testing"
)

func TestOrkaMalformedServiceListsCannotAuthorizeManagementFallback(t *testing.T) {
	for _, raw := range []string{"null", "{}", `{"items":null}`, `{"items":false}`, `{"items":[]}`} {
		t.Run(raw, func(t *testing.T) {
			f := newEvalFixture(t, nil)
			t.Setenv("KMX_EVAL_SERVICES", raw)
			t.Setenv("KMX_EVAL_MANAGEMENT_SERVICES", splitResultService)
			t.Setenv("KMX_EVAL_MANAGEMENT_DEPLOYMENTS", splitResultController(t, []string{"--watch-namespace=tasks"}))
			valid := raw == `{"items":[]}`
			_, err := f.app.orkaAPIService(t.Context(), "tasks")
			if err == nil || errors.Is(err, errOrkaAPIServiceAbsent) != valid {
				t.Fatalf("Service absence classification: valid=%t err=%v", valid, err)
			}
			namespace, service, err := f.app.orkaResultAPI(t.Context(), "tasks", "")
			if valid {
				if err != nil || namespace != OrkaNamespace || service != "orka" {
					t.Fatalf("valid empty list refused management fallback: %s %s %v", namespace, service, err)
				}
			} else if err == nil || service != "" {
				t.Fatalf("malformed list authorized fallback: %s %s %v", namespace, service, err)
			}
		})
	}
}
