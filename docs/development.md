# Development

For work **on** Kaimahi; contribution/PR expectations are in
[CONTRIBUTING.md](../CONTRIBUTING.md). For use, start with
[getting started](getting-started.md) and [Orka](orka.md).

## Direction and current implementation

**Orka remains the first-class/default platform.** Kaimahi tooling helps
agents get onto it. `kmx agent create` without a runtime flag authors native
Orka Provider + Agent resources with an optional Task. The explicit kagent
path is intentionally narrower: create against preinstalled exact v0.10.2,
and Go declarative bundle lift/status against preinstalled exact v0.10.3.
There is no installer, upgrade, discovery, standalone chat or evaluation.
It is not a conversion to Orka; see the
[safety contract](kmx.md#kmx-agent-create). `orka.harness.v2` is outside the
direction. Runtimes and harnesses own execution, enforcement and model access.

Plane CLI administration, model overlays, credential exchange and workload
migration are removed, together with the nested source module, image-build
helper, embedded plane/observability assets and plane-only scripts/fixtures.
Historical source, including checked-in SQL migrations, remains in Git history,
not this checkout. No databases, credentials, caches or deployed resources are
deleted. Cloud ownership/teardown records and their tests remain. The old
gateway, approvals/grants, workflow runner and connector fixtures remain retired.

## Repository layout

Consult the [repository map](repository-map.md) for tracked membership and caller
classification.

| Path | Responsibility |
|---|---|
| `cmd/kmx/`, `internal/kmx/`, `embed.go` | CLI command tree, Orka and version-bounded kagent lifecycle adapter, portable authoring, shared plumbing and embedded manifests |
| `pkg/kmx/` | experimental public lifecycle contracts, not wired into the installed CLI |
| `k8s/ollama.yaml`, `k8s/orka-k8s-tool.yaml` | native local model and read-only Kubernetes Tool |
| `scripts/`, `Makefile` | checks, packaging and repository helpers |
| `.github/workflows/` | verification jobs and docs-only routing |

The root CLI is the only Go module. Plain `make` builds `bin/kmx` only. The supported
kagent surface is version-qualified create or declarative bundle lift/status,
not a general legacy-YAML conversion layer. One production LifecycleAdapter
owns both profiles; experimental public ports remain unwired.

## Build and verify

Run from the repository root. AgentSuite is a public package in the same Go
module, so these commands include it without a workspace or a dependency pin.
CI's Go hygiene checks remain unguarded, including on documentation-only changes;
only the live and clone-free operation steps use documentation-only guards.
Formatting checks must test `gofmt` output because
`gofmt -l` alone exits successfully even when it lists files.

Tests that read or write KMX state must set `KMX_HOME` to `t.TempDir()`. Do not
use `XDG_CONFIG_HOME` as the test boundary: Go uses it for the native Linux
config location, while macOS uses `~/Library/Application Support`. `KMX_HOME`
is the repository's explicit cross-platform isolation contract.

```bash
formatted=$(gofmt -l agentsuite cmd internal pkg/kmx embed.go embed_test.go) && test -z "$formatted" && go vet ./... && go build ./... && go test ./...
python3 scripts/check-doc-links.py --selftest
python3 scripts/check-doc-links.py
python3 scripts/check-secret-shapes.py --selftest
python3 scripts/check-secret-shapes.py
bash scripts/check-no-azure-ids-test.sh
bash scripts/check-no-azure-ids.sh
bash scripts/kube-guard-test.sh
python3 scripts/test_model_fixtures.py -v
```

### App test timing

`internal/kmx/app` fixtures inject invocation-local polling intervals and phase
deadlines. Keep setup budgets intact and shorten only the wait under test;
assert that the intended readiness, admission, or result boundary was reached.
An unset timing hook must retain the production intervals and deadlines.

The fake kubectl handlers live in `internal/kmx/app/testdata/kubectl`. App tests
compile this lightweight executable once per package run instead of launching
the full UI-bearing App test binary for every CLI call. It still runs as a real
subprocess with the fixture's environment and call log. A `go test -race` App
run also compiles the helper with `-race`; the build directory is removed when
the package run finishes. Keep helper imports independent of App and UI code.

The workflow's hygiene job is authoritative for the full checker/self-test/
mutation set; do not replace it with this focused list. The retained native
Orka Tool fixture tests exercise actual HTTP requests and health/refusal
responses without a cluster. No plane-module or plane Postgres service job
remains.
[CI configuration](../.github/workflows/ci.yml) also checks its own guards and
aggregator membership. The retired-command scanner rejects concrete removed
commands/assets, not kagent vocabulary or version claims. It uses bounded literal
command tokenization and exact inert exceptions; production command tests own
runtime selection and supported-operation validation.

### Local loop

```bash
make
export KIND_CLUSTER=dev-local
export KUBE_CTX=kind-dev-local
bin/kmx quickstart
bin/kmx status
bin/kmx agent chat --namespace orka-system hello-world-agent
bin/kmx local down
```

A bare `bin/kmx local up` installs the runtime without an Agent: kind, Ollama, the
model and pinned Orka with its keyless `local` Provider. Author your own Agent
with `bin/kmx agent create`, or use `bin/kmx quickstart --interactive` for guided
creation. `kmx status` is the Orka-only runtime report; bundle-aware
`agent status` is a separate command.

KMX never installs or upgrades kagent. Explicit create targets exact v0.10.2;
explicit bundle lift/status uses `--runtime kagent --runtime-version v0.10.3`,
`--to-context` and validated `KagentBindings`. The normal development loop remains
Orka; the profiles never fall back between installed runtimes.

Pick a distinct cluster name and explicit context. Keep
`CONTAINER_ENGINE=podman` consistent if selected; Docker and Podman inventories
are separate. `down` destroys all data in the named local cluster, including
any historical database still present; export needed data first. Cloud cleanup
has different [ownership rules](aks.md#teardown).

### What CI proves

The workflow emits `hygiene` and `e2e-hello-world` merge gates. The latter
aggregates the native `e2e-quickstart`, `e2e-kagent-create`, `e2e-orka-runtime`,
`e2e-orka-helm`, clusterless `e2e-eval-loop` and dedicated `e2e-kagent-lift`
shards, plus successful change classification.
Required-check rules are managed separately on GitHub: removing a workflow job
does not change the active ruleset or satisfy a retired required context.

`state-paths-macos` is a focused native macOS lane for KMX state routing. Linux
`hygiene` exercises the same contract under XDG semantics; the macOS lane proves
the `~/Library/Application Support` default and shared `KMX_HOME` override
without duplicating cluster setup or the full Linux suite.
Add probes to the shard owning their state lineage, or arrange independent setup.
Existing cluster steps retain docs-only guards. `e2e-kagent-lift` instead routes
at job level: relevant/unknown PR changes run it, as do every main push, nightly
and manual dispatch. The aggregator uses `always()`, requires classification and
all six shards, and accepts only intentional skipped outcomes. A failed or
cancelled shard is never an intentional skip.

`e2e-eval-loop` is a required clusterless, secret-free evaluation boundary on
non-docs-only changes. It runs digest-pinned AIKit Qwen3.5-2B on CPU and builds
`agentsessionsd` at the module revision pinned by KMX. Through that sessions
endpoint, `kmx agent evaluate` runs the tiny public bundle in
`internal/kmx/app/testdata/live-eval-loop`: both cases must pass, containing
the literal substrings `Paris` and `4` respectively, not exact answer wording.
The capital case also exercises a Go RE2 pattern and `notContains` assertion.
Tool assertions are tested as unknown on these tool-disabled paths, not added to
this passing live fixture. Judge assertions are not part of these live CLI
agent cases either. Before sessions startup, CI separately opts into the action's
AIKit-only `judge-proof: 'true'` (default `'false'`).
`TestLiveEvaluationJudgeAIKitProof` calls the production judge client/evaluator
with public literal answers and the rubric “100 if exactly YES, otherwise 0”,
threshold 50, explicitly selecting `qwen-3.5-2b` and the running image SHA-256
as **user-declared** version. Two temperature-zero calls per answer must yield
`YES` → `[100, 100]`/pass and `NO` → `[0, 0]`/fail. Unknown is never accepted;
there are no retries or model substitutions. This is a real provider/client
boundary, not a live CLI agent-judge end-to-end assertion.

One local run demonstrated those exact scores in **four calls, 17.82 seconds**
for the judge test, not total runner/build time. The required shard gates the
same proof on non-docs-only changes; neither this trivial pair nor the agent
happy cases prove general 2B judge quality. The proof prints safely rendered
public fixture reasoning and saves actual payload-free rows in `judge-proof.json`.

The shard then stops the model provider, keeps the daemon and journal alive, and
requires `kmx agent verify` to report every case equivalent with zero model
calls through local-reference replay.

The job uses the same [eval action](../.github/actions/kmx-eval/action.yml)
users can run [in their own CI](agent-lift.md#run-evals-in-ci). Evaluation stays
in the checkout so the receipt names the tested commit. Uploaded artifacts are
only the payload-free evaluation receipt, verify report and public judge-proof
JSON, not private session evidence or logs. Successful agent-evaluation logs
omit prompts and answers; the separate public judge proof prints safe reasoning.
For user-authored judges, reasoning can repeat answer text: terminal output is
not payload-free, external judge retention applies to input/answer/rubric, and
private runtime journals remain outside receipt privacy. Failure diagnostics
print only digest-checked, redacted failing answers, never raw daemon/model logs.
Hygiene self-tests the evidence/provenance gates and failure diagnostics with
`scripts/test_eval_loop.py`, and runtime lifecycle/credential handling with
`scripts/test_eval_runner.py`. This check proves no lift gate, host implementation
attestation, cluster integration or hosted-model provider behavior.

`e2e-orka-runtime` brings up kind, Ollama and the model with compatibility `kmx up --step`
component steps, installs pinned Orka and its keyless Provider, and requires a
Provider → Agent → Task round trip to return an exact, non-empty local-model
answer. It applies the committed native Orka
[Kubernetes Tool](orka-k8s-tool.md) and checks its boundary directly over HTTP:
an allowed ConfigMap listing containing a freshly created ConfigMap, and
refusal of Secret reads and pod mutation at both tool validation and cluster
RBAC. It does not prove that the local model chose to call the tool; small-model
tool selection is left unasserted rather than asserted flakily.

`e2e-kagent-create` is the live boundary for explicit Kagent create. CI creates
its own dedicated kind cluster and installs official `kagent-crds` and `kagent`
OCI charts at exactly v0.10.2 as an **external test precondition**. It checks
published OCI digests and unpacked chart names/versions, scales the UI to zero,
disables tools, built-in agents and optional subcharts, and retains bundled
Postgres. It provisions a keyless deterministic in-cluster OpenAI fixture and
separately named dummy Secret. With `KMX_TOOLCHAIN=off` and failing `helm` and
`kagent` executables first on `PATH`, create must return the exact answer. The
shard independently checks controller identity, current-generation conditions,
Agent ownership of its Deployment and Service, official Go runtime image,
artifact/live ownership-marker split, cluster identity, and a private
prompt/answer-free two-resource receipt. No hosted credential is used.

The workflow, not KMX, installs those charts. This proves no standalone Kagent
operation beyond create's optional single A2A message, no managed-cluster path,
and no hosted model provider.

`e2e-kagent-lift` continuously qualifies Go declarative v0.10.3
plan/lift/repeat/status and specific binding, missing-receipt and operator-drift
refusals. The metadata-only kubectl audit checks complete ordered server-admission
and persistent-create frames, effective dry-run strategy and resource identities;
unsupported command forms leave a closed rejection and never forward. Negative
calls must exit 1 with the exact final ordinary CLI error, not an unrelated failure.
A separate one-shot native A2A message must return the fixture's random MCP sentinel
with one actual tool call, two model calls and one terminal answer. Only identities,
counts and hashes enter its private qualification artifact; readiness receipts
remain separate. Python deployment, managed clusters and hosted providers remain
unqualified.

The checkout-only [qualification shell](../scripts/ci/kagent-lift.sh) defaults to
`qualify`; build the final Go state with `go build -o bin/kmx ./cmd/kmx`, then
run `bash scripts/ci/kagent-lift.sh`. Explicit `bootstrap` stops after readiness
and discovery; run `bash scripts/ci/kagent-lift.sh bootstrap`
on Linux amd64 with Docker. It downloads checksum-verified local tools, pins
the kind node and official v0.10.3 OCI charts, disables optional workloads/UI
and telemetry, and checks rendered and running image identities. Its
[driver](../scripts/ci/kagent-lift.py) provisions a private dummy Secret and the
[keyless model/MCP fixture](../scripts/ci/kagent-lift-fixture.py), then proves
health and discovery with zero tool calls through an owned loopback forward.
This is bootstrap characterization, not lift/native A2A qualification.

The script refuses an existing `kmx-kagent-lift` cluster. Kubeconfig, Kubernetes
discovery cache (`KUBECACHEDIR`), Helm config/cache/data, registry/repository/plugin
paths and raw tool output stay in private temporary work, removed on exit.
Inherited `HELM_KUBE*` connection/auth overrides are cleared. Chart pulls use a
keyless private registry config, also scoped to Helm's Docker credential fallback;
kind and Docker retain the caller's Docker configuration and daemon access.
Closed phase/exit-status diagnostics identify failures without replaying payloads. Cleanup deletes the cluster only after successful creation;
a failed kind create is ambiguous and left for inspection. No caller tools,
Helm credentials, daemon configuration or unrelated clusters are changed.
Hygiene unconditionally runs bootstrap, shell, HTTP fixture, qualification,
metadata-audit and CI-routing tests, including the actual qualifier with hermetic
external boundaries and unrelated failures injected at all five negative calls.
It creates no cluster. The dedicated shard runs the live qualification.

`kmx-clone-free` runs on main/manual dispatch, not as a required PR shard. It
checks installation, bare `up` and native Orka creation with an actual Task
answer. Tags trigger the separate release workflow. None of these proves an
AKS run: no Azure credentials belong in fork-exposed CI. A docs-only shortcut
is not an end-to-end rerun.

### CI registry mirrors

CI's Docker-using jobs configure the host daemon with
[`scripts/ci/registry-mirrors.py`](../scripts/ci/registry-mirrors.py) and report
pull endpoint evidence. Setup merges `registry-mirrors=["https://mirror.gcr.io"]`
and `debug=true` into existing daemon configuration, preserving unrelated
settings, engine version and running containers. It reloads Docker with SIGHUP
rather than restarting or replacing it. Only Docker Hub routing changes:
references, digest pins, manifests and source stay unchanged; GHCR and ECR are
unaffected.

Component/direct-kind jobs configure nodes after cluster creation and before
workloads. The pinned kind node uses containerd 2.3.4's default
`/etc/containerd/certs.d` path: `docker.io/hosts.toml` keeps
`server = "https://registry-1.docker.io"` as the origin fallback and adds
`https://mirror.gcr.io` with `pull` and `resolve` capabilities. Setup enables
containerd debug logging for the endpoint report.

Compound quickstart and clone-free journeys use a CI-only Docker-named symlink
wrapper. It forwards Docker calls unchanged, then hooks a successful kind-node
creation identified by reserved kind cluster and node-role labels, configuring
hosts and debug logging before kind returns. This keeps bare `up` and fetched-kind
proof intact without changing KMX or kind. Clone-free CI fetches only this helper
at the workflow SHA into `RUNNER_TEMP`; it does not check out the repository.

Reports print only successful HTTP 200 endpoint responses: endpoint, image and
manifest/blob category, without headers, query strings or payloads. A cache hit
is not mirror-routing evidence; successful Docker Hub fallback is identified
separately. Hygiene runs
[`scripts/test_registry_mirrors.py`](../scripts/test_registry_mirrors.py) to test
configuration merging, wrapper/node selection and evidence filtering.

Check coverage against existing image references and digests, including
`kindest/node` and Ollama. A cache miss or mirror outage can fall back to Docker
Hub; reports do not call that a mirror success. Existing Postgres ECR pins stay
unchanged. Model-weight downloads from `registry.ollama.ai` are separate from
OCI pulls and do not use these mirrors.

## Invariants to preserve

1. Use platform capabilities rather than rebuilding them. Legacy-shaped helpers
   do not authorize expanding Kaimahi into another agent runtime.
2. Fail closed on missing proof: HTML with 200 is not a valid endpoint answer,
   unreadable is not absent, and scanner failure is not a clean scan.
3. Keys never enter argv, logs, manifests or ConfigMaps. Native Provider Secrets
   are provisioned separately; host Copilot credentials remain managed by its
   installed CLI. Preserve each native input contract.
4. Billed work must be recorded even when the surrounding operation fails.
   Historical cloud ownership records remain necessary for conservative teardown.
5. Mutations need context/cloud guards; name and loopback server are independent
   evidence. Confirmation is scoped consent, not a general bypass.
6. Repository cleanup does not authorize deleting databases, caches, credentials
   or deployed resources. Historical source remains available in Git history.
7. Metrics labels must not leak tokens or channel/user/request/delivery IDs.
8. State evidence accurately: continuously tested, demonstrated, schema-valid,
   proposed or unbuilt. Configuration and a fluent answer are not enforcement proof.

## Troubleshooting traps

- A Provider can exist without resolving a model. Inspect Provider and Agent
  readiness, not only controller pod health; then prove a real Task answer.
- Setup does not adopt or redirect existing applications. Owners must deliberately
  remove or replace obsolete gateway references and external subscriptions.
- A Secret created after an optional mount may need a rollout restart.
- A Service port-forward selects one pod, not every replica. Use distinct fixed
  ports across concurrent checks; native chat allocates a free loopback port.
- Ambiguous-disconnect retries can repeat effects; read the
  [retry limits](kmx.md#retry-limits) before resending work.
- A bare shell `wait` waits on long-running forwards too; collect worker PIDs.
- Podman machines need checkout mounts for image builds; restarted machines may
  leave kind nodes stopped. The cluster step recovers named nodes and checks API/DNS.
- Python scripts supporting macOS Python 3.9 need postponed annotations before
  PEP 604 annotations. Ignore rules such as `bin/` match at every depth; verify
  tracked membership and beware newly unignored files entering `stash -u`.

Use `kmx orka status`, `kmx agent status`, Agent/Provider/Task conditions and
context-pinned controller logs. Never paste live infrastructure IDs into
evidence: scan shapes and manually redact names too.
