# Demo paths

## Hello world to governed model traffic

[`scripts/demo-hello-to-governed.sh`](../scripts/demo-hello-to-governed.sh)
is a terminal demo using the existing kmx commands, local Ollama `qwen2.5:3b`,
and the existing `pauldotyu/sundae-funday` application pinned to
`bd2a0354e38e7fe92a7b24e6a29ba637e745e91e`. It does not add an application or
change the CLI. No model API subscription or paid endpoint is used.

The four beats are:

1. `kmx orka install` installs the pinned platform on a dedicated kind cluster.
2. `kmx agent create hello --task ...` creates Provider, Agent and Task and
   retrieves a real local-model answer.
3. Show the generated YAML, then `kmx agent show hello` for the live dependency
   chain, Provider readiness and Secret name. `show` itself supports table/JSON,
   not YAML. The full bundle is saved; its Secret skeleton contains no values
   and must not be bulk-applied.
4. `kmx migrate concierge --namespace demo --model local/qwen2.5:3b` prepares
   the model route. **The application owner then applies the generated patch.**
   The script compares Deployment snapshots before/after kmx, verifies the
   image and UID survive the owner patch, calls the application's normal
   `/api/chat` endpoint, and checks successful model rows with nonzero tokens.
   `kmx watch concierge` occupies the second pane. Its connection starts after
   migration rolls the proxy, on a distinct local admin port.

The script checks `orka-agents/orka` PR #564 live. While unmerged, it prints one
line naming external A2A discovery as next, without simulating it. If merged,
this script still claims no A2A proof: adapter deployment compatibility must be
verified against the installed Orka before extending the demo.

### Prepare, record, verify

Use a current checkout and a Linux terminal with Go, Docker, kind, kubectl,
Helm, Python 3 + PyYAML, curl, gh, tmux, asciinema 2 and
[asciinema agg](https://github.com/asciinema/agg) on PATH. Public downloads must
be reachable. The script uses a private kubeconfig in each run directory and
explicit contexts on kmx/kubectl/Helm; it never selects the shared current context.

```bash
export KIND_CLUSTER=kmx-hello-governed-1
export DEMO_RECORD_PROFILE=presenter
run=/home/tng/kaimahi-local/demo-hello-to-governed/take-1
bash scripts/demo-hello-to-governed.sh prepare "$run"
bash scripts/demo-hello-to-governed.sh record "$run"
bash scripts/demo-hello-to-governed.sh verify "$run"
```

Repeat with a fresh run directory and cluster suffix for the second cold run.
Preparation refuses an existing cluster or directory. It reuses `kmx up --step`
for cluster/Ollama/model and `kmx plane`; bare `up` and `quickstart` include a
different first-answer journey, so they are not duplicated or run in this demo.
Preparation also builds the unchanged application's Dockerfile and installs its
unchanged chart with local configuration. Its archive is SHA-256 checked.

**Timing boundary:** preparation includes cluster creation, the roughly 1.9 GB
model download, plane deployment, application build/install and prerequisite
Secrets. Its elapsed time is printed at the beginning of the recording and
saved separately; it is not part of the four-beat demo duration. No Orka install
or model answer is pre-run. Host image/build caches may already exist, but each
kind node and its model storage are fresh. The under-five-minute target is for
the recorded four beats, not total machine provisioning. Cold waits over 90
seconds are reported, not removed.

`DEMO_RECORD_PROFILE=presenter` pauses two seconds after commands; `docs` pauses
one second. Neither profile types slowly, drops beats, limits idle time or
accelerates playback. This borrows the profile idea from
[Orka's recording guide](https://github.com/orka-agents/orka/blob/main/hack/demos/RECORDING.md),
not its code. Every beat's first plain sentence is the talk track.

Successful recording deletes the dedicated cluster on screen, then renders
`demo.gif` at original speed. On failure, evidence is retained and the command
exits nonzero; inspect it, then explicitly clean up:

```bash
bash scripts/demo-hello-to-governed.sh teardown "$run"
```

Keep `demo.cast`, `demo.gif`, `timings.txt`, `setup-time.txt`, `teardown.txt` and
supporting evidence **outside Git**. There is no existing committed recording
convention here, and the run directory includes a private kubeconfig. Do not
publish the whole directory. `verify` rechecks the saved application answer and
model ledger after teardown; it does not rerun inference or certify a recording.

The demo proves model traffic through the seam, not tool governance, network
policy enforcement or general application compatibility. It uses one fresh
conversation; the [documented continuation limits](migrate.md#continuation-incompatibility)
still apply. A generated answer is not promised to have exact wording. The
ledger's `unpriced`/zero cents is not itself the proof of free inference; the
actual model endpoint is the in-cluster Ollama Provider.

Cluster-free safety and evidence-parser checks:

```bash
bash -n scripts/demo-hello-to-governed.sh
python3 scripts/test_demo_hello_to_governed.py
```

## Other paths and retired demonstrations

The gateway-backed tool, workflow and business-scenario demonstrations are
**retired with their code**, not current operator procedures. Their former
checklist is preserved in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/demo.md).

For current demonstrations use [native Orka creation](orka.md),
[model-traffic migration](migrate.md), or the retained [kagent first-answer
path](getting-started.md#one-command-and-an-agent-that-answers). The original
[direct kagent MCP example](tools.md) remains; not all MCP support is retired.
Model ledger rows prove traffic crossed the model seam, not governance of tools
or ownership of the application Deployment.

Existing deployments need [explicit retirement cleanup](operations.md#upgrading-after-gateway-retirement).
For disposable kind clusters, `kmx down` destroys the cluster and ledger;
managed-cluster cleanup follows the [AKS ownership rules](aks.md#teardown).
No new live kind or cloud proof is claimed by this documentation update.
