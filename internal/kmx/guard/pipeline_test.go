package guard

import (
	"context"
	"io"
	"testing"
)

func TestPipelineConfirmationContexts(t *testing.T) {
	cfg, err := ParseKubeconfig([]byte(`{"clusters":[{"name":"local","cluster":{"server":"https://127.0.0.1:6443"}},{"name":"remote","cluster":{"server":"https://cluster.example.invalid"}}],"contexts":[{"name":"kind-dev","context":{"cluster":"local"}},{"name":"dev-remote","context":{"cluster":"remote"}},{"name":"aks-prod","context":{"cluster":"remote"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		contexts []string
		confirm  string
		pass     bool
	}{
		{[]string{"kind-dev", "aks-prod"}, "aks-prod", true},
		{[]string{"kind-dev", "aks-prod"}, "kind-dev,aks-prod", false},
		{[]string{"dev-remote", "aks-prod", "dev-remote"}, "dev-remote,aks-prod", true},
		{[]string{"dev-remote", "aks-prod"}, "aks-prod,dev-remote", false},
		{[]string{"kind-dev", "kind-dev"}, "kind-dev", true},
		{[]string{"kind-dev"}, "", false},
		{[]string{"absent"}, "absent", false},
	} {
		if err := ConfirmPipeline(context.Background(), cfg, tc.contexts, tc.confirm, "eval,push,lift", io.Discard, nil); (err == nil) != tc.pass {
			t.Errorf("contexts=%v confirm=%q err=%v", tc.contexts, tc.confirm, err)
		}
	}
}
