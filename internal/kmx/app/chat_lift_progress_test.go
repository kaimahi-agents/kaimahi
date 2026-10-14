package app

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestAzureFetchLabelsIdentifyPhaseAndScope(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"account", "show"}, "Fetching active Azure tenant"},
		{[]string{"group", "list", "--subscription", "sub"}, "Fetching resource groups · subscription:sub"},
		{[]string{"aks", "list", "--subscription", "sub", "--resource-group", "rg"}, "Fetching AKS clusters · subscription:sub · rg:rg"},
		{[]string{"aks", "get-credentials", "--name", "cluster"}, "Fetching AKS credentials · resource:cluster"},
		{[]string{"cognitiveservices", "account", "list-models", "--name", "foundry"}, "Fetching available Foundry models · resource:foundry"},
		{[]string{"cognitiveservices", "account", "deployment", "list"}, "Fetching Foundry deployments"},
		{[]string{"cognitiveservices", "account", "keys", "list"}, "Fetching Foundry credential"},
		{[]string{"cognitiveservices", "usage", "list", "--subscription", "sub", "--location", "eastus"}, "Checking available Foundry quota · subscription:sub · region:eastus"},
		{[]string{"cognitiveservices", "model", "list"}, "Fetching regional Foundry models"},
		{[]string{"rest", "--method", "get"}, "Fetching Azure REST response"},
	} {
		if got := azureFetchLabel(tc.args); got != tc.want {
			t.Fatalf("got=%q want=%q", got, tc.want)
		}
	}
}

func TestLiftFetchPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := liftFetchProgress(ctx, &bytes.Buffer{}, "Fetching", func(ctx context.Context) ([]byte, error) { return nil, ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
