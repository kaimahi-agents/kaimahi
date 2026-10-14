# Historical Azure SDK discovery benchmark

The SDK-backed AKS cluster picker belonged to the retired interactive Lift
workflow. Chat and quickstart `--azure-discovery` are removed; these measurements
are historical, not an available discovery alternative or a current qualification.
Standalone `kmx aks up/down` and host inference discovery through the Azure CLI
remain independent.

The [archived implementation and reproduction instructions](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/azure-discovery-performance.md)
describe the former credential chain, paging and client reuse. Its live benchmark
and SDK-only tests are no longer in this checkout.

## Historical measurements

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
faster**, saving approximately **9 seconds** per cluster-list step. Timing included
credential resolution and all response pages, but excluded executable compilation
and the subscription picker. CLI and SDK could use different ARM API versions;
service/network variability applies. No cloud resources were changed.
