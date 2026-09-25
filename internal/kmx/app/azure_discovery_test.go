package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type discoveryCredential struct{}

func (discoveryCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "test", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type discoveryTransport func(*http.Request) (*http.Response, error)

func (f discoveryTransport) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestAzureSDKDiscoveryPaginatesAndReusesClient(t *testing.T) {
	credentials, requests := 0, 0
	d := &azureSDKDiscovery{credential: func(tenant string) (azcore.TokenCredential, error) {
		credentials++
		if tenant != "tenant" {
			t.Fatalf("tenant=%q", tenant)
		}
		return discoveryCredential{}, nil
	}, options: &arm.ClientOptions{ClientOptions: azcore.ClientOptions{Transport: discoveryTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if !strings.HasPrefix(r.URL.Path, "/subscriptions/sub/") {
			t.Fatalf("unscoped URL: %s", r.URL)
		}
		body := `{"value":[{"id":"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks","name":"aks","location":"eastus","properties":{"kubernetesVersion":"1.32"}}]}`
		if r.URL.Query().Get("page") == "" {
			body = `{"value":[],"nextLink":"https://management.azure.com/subscriptions/sub/providers/Microsoft.ContainerService/managedClusters?page=2"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}}
	for i := 0; i < 2; i++ {
		clusters, err := d.clusters(t.Context(), "sub", "tenant")
		if err != nil || len(clusters) != 1 || clusters[0].ResourceGroup != "rg" || clusters[0].KubernetesVersion != "1.32" {
			t.Fatalf("clusters=%v err=%v", clusters, err)
		}
	}
	if credentials != 1 || requests != 4 {
		t.Fatalf("credentials=%d requests=%d", credentials, requests)
	}
}

// Opt-in read-only comparison; ordinary tests never use real Azure credentials.
// Set KMX_BENCH_AZURE_SUBSCRIPTION and optionally KMX_BENCH_AZURE_TENANT.
func TestLiveAzureDiscoveryPerformance(t *testing.T) {
	sub := os.Getenv("KMX_BENCH_AZURE_SUBSCRIPTION")
	if sub == "" {
		t.Skip("explicit live benchmark only")
	}
	tenant := os.Getenv("KMX_BENCH_AZURE_TENANT")
	d := newAzureSDKDiscovery()
	var expected []string
	identities := func(clusters []azureCluster) []string {
		ids := []string{}
		for _, c := range clusters {
			ids = append(ids, c.ResourceGroup+"/"+c.Name)
		}
		sort.Strings(ids)
		return ids
	}
	for i := 0; i < 3; i++ {
		for _, backend := range []string{"cli", "sdk"} {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			start := time.Now()
			var clusters []azureCluster
			var err error
			if backend == "sdk" {
				clusters, err = d.clusters(ctx, sub, tenant)
			} else {
				var raw []byte
				raw, err = (&App{Run: &run.Runner{}}).liftDiscovery(ctx, "az", aksListArgs(chatLiftTarget{Subscription: sub})...)
				if err == nil {
					err = json.Unmarshal(raw, &clusters)
				}
			}
			cancel()
			if err != nil {
				t.Fatalf("%s run %d: %v", backend, i+1, err)
			}
			t.Logf("%s run=%d duration=%.3fs clusters=%d", backend, i+1, time.Since(start).Seconds(), len(clusters))
			ids := identities(clusters)
			if expected == nil {
				expected = ids
			} else if !reflect.DeepEqual(ids, expected) {
				t.Fatal("CLI and SDK returned different cluster sets")
			}
		}
	}
	// A new credential/client represents reopening KMX, rather than a warm browse.
	start := time.Now()
	clusters, err := newAzureSDKDiscovery().clusters(t.Context(), sub, tenant)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sdk fresh-client duration=%.3fs clusters=%d", time.Since(start).Seconds(), len(clusters))
}
