# Azure SDK discovery alternative

Use the native Go ARM SDK for subscription-wide AKS cluster listing in `/lift`:

```sh
./bin/kmx quickstart --interactive --azure-discovery sdk
```

`--azure-discovery cli` retains the original Azure CLI implementation (the
default). Both populate the same searchable cluster list, including resource
group, location and Kubernetes version. Subscription discovery, AKS credential
retrieval and Foundry operations still use `az`; this alternative currently
replaces the slow `az aks list` operation only.

## Authentication and lifecycle

The SDK uses `azidentity.DefaultAzureCredential`, which includes Azure CLI
credentials from `az login`. It honors the normal Azure credential chain, so a
configured service principal/workload identity can take precedence. To explicitly
restrict that chain to your Azure CLI credential for a comparison:

```sh
AZURE_TOKEN_CREDENTIALS=AzureCLICredential \
  ./bin/kmx quickstart --interactive --azure-discovery sdk
```

The selected subscription's tenant ID is passed to the credential. SDK clients
are retained per subscription/tenant during chat, allowing bearer tokens and
HTTP connections to be reused. Resource lists are fetched anew and fully paged;
this is not a stale results cache. Cancellation and the existing 30-second
discovery deadline apply. SDK errors are reported rather than silently falling
back to another identity or discovery backend.

## Live benchmark

Measured against the active Azure subscription on 2026-09-16, listing **19 AKS
clusters**. Requests alternated CLI and SDK sequentially. All runs returned the
same set of resource-group/cluster identities. The SDK was not primed before the
first run.

| Run | CLI | SDK |
| --- | ---: | ---: |
| First | 13.179 s | 3.506 s |
| Repeat 1 | 11.212 s | 2.384 s |
| Repeat 2 | 11.257 s | 2.357 s |

A separate fresh SDK credential/client took **3.435 s**. In this small sample,
first-call listing was about **3.8× faster** and repeated listing about **4.7×
faster**, saving approximately **9 seconds** per cluster-list step. Timing includes
credential resolution and all response pages, but excludes executable compilation
and the subscription picker. CLI and SDK can use different ARM API versions;
service/network variability applies. No cloud resources were changed.

Reproduce the opt-in read-only benchmark:

```sh
KMX_BENCH_AZURE_SUBSCRIPTION=<subscription-id> \
KMX_BENCH_AZURE_TENANT=<tenant-id> \
  go test ./internal/kmx/app -run '^TestLiveAzureDiscoveryPerformance$' \
    -v -count=1 -timeout=240s
```

The ordinary test suite skips live discovery and uses a mock credential and
transport to verify subscription scoping, pagination and client reuse.
