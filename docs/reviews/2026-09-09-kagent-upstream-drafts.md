# Upstream drafts for kagent — verification and paste-ready text

**Status: drafts only. Nothing here has been posted.** This lane drafts;
the user posts. No issue was opened, no comment was made, and nothing was
sent to kagent's repository or to any person.

The board's "Upstream candidates (kagent)" section records three
candidates — U1, U2b, U3 — and says plainly that none has ever been
filed. This document verifies each one against kagent as it stands
today, drops the ones that no longer hold, and leaves text that can be
pasted with a read-through.

## The verification basis, and why the pinned version was not enough

This repository pins **kagent 0.9.12**. That is not the version a
maintainer will read an issue against, and kagent's own bug template asks
the reporter to confirm "I am using the latest version of the software".
So every candidate was checked against three refs:

| Ref | What it is |
|-----|------------|
| `v0.9.12` | what this repository pins, and what every recorded observation was made against |
| `v0.10.1` | the current release — **published 2026-09-08 20:45 UTC, hours before this pass** |
| `main` | unreleased work |

**`v0.10.1` is not cut from `main`.** `compare/v0.10.1...main` reports
`status=diverged`, 143 ahead and 32 behind. A candidate can therefore be
true of the latest release and already fixed in the repository, which is
exactly what happened to one of the three below. Checking only the tag
would have filed it.

Everything marked "reproduced live" was run on a fresh kind cluster
(Kubernetes v1.32.3) with `kagent-crds` and `kagent` **0.10.1** installed
from `oci://ghcr.io/kagent-dev/kagent/helm/…`. The cluster was deleted
afterwards.

## Verdicts

| # | Candidate | Still true? | Already reported? | Action |
|---|-----------|-------------|-------------------|--------|
| **U1** | Agent `Ready` does not go False while the Deployment rolls, so `kubectl wait --for=condition=Ready` returns on a stale True | **YES — reproduced live on 0.10.1, and root cause found in source** | No | **File**, scoped to v0.10.x. Draft 1 below. |
| **U2b** | Agent image declares its user by name (`python`), forcing every downstream to hardcode a UID | **NO — fixed upstream** | n/a | **Do not file.** See below; a comment on the still-open #2244 is drafted instead. |
| **U3** | `kagent invoke` emits raw JSON with no human-readable mode | True of the release, **already fixed on `main`** | No | **Do not file.** See below. |
| **U4** | *(new, not on the board)* ModelConfig admits a `spec.tls` block beside an `http://` baseUrl; RemoteMCPServer rejects the same mistake | **YES — reproduced live on 0.10.1, present at all three refs** | No | **File.** Draft 2 below. |

**One candidate should not be filed at all, and it is U3** — not because
it is weak, but because the thing it asks for already exists in the
repository. Filing it would repeat, exactly, the mistake that got U2
withdrawn: asking kagent for something kagent already ships. U2b is a
second instance of the same trap, caught the same way.

---

## U2b — WITHDRAWN. The image was fixed upstream.

The candidate said kagent's agent image declares its user by name
(`USER python`), so `runAsNonRoot: true` cannot be used against it
without every downstream hardcoding a UID, and that Kubernetes refuses
the container with `image has non-numeric user (python), cannot verify
user is non-root` at CreateContainer time.

**That was true at 0.9.12 and is no longer true.**

- `python/Dockerfile` at `v0.9.12` line 93: `USER python`.
- The same file at `v0.10.0-beta5` and every ref after it, including
  `v0.10.1` and `main`: **`USER 65532:65532`**. The image moved to a
  distroless base as part of
  [#2498](https://github.com/kagent-dev/kagent/pull/2498) ("refactor:
  simplify runtime images and add lifecycle e2e", merged 2026-08-19).

Reproduced live on 0.10.1, and this is the test that settles it. An Agent
was applied with `runAsNonRoot: true` and **no `runAsUser`** — the exact
manifest that died at `CreateContainerConfigError` on 0.9.12:

```yaml
podSecurityContext:
  runAsNonRoot: true
  seccompProfile:
    type: RuntimeDefault
securityContext:
  allowPrivilegeEscalation: false
  capabilities:
    drop: [ALL]
```

The pod started:

```
STATE={"running":{"startedAt":"2026-09-09T04:00:14Z"}}
STARTED=true
PODSC={"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}}
```

So the cost U2b asked kagent to remove has been removed. Filing it would
have been the withdrawn U2 all over again.

### What survives, and it is somebody else's issue already

While verifying, one thing that is *still* true turned up: at 0.10.1 the
Deployments the controller creates from an `Agent` still carry an empty
`securityContext` at both pod and container level. Checked live on the
chart's own `k8s-agent`:

```
POD={}
CTR=
IMG=ghcr.io/kagent-dev/kagent/golang-adk:0.10.1
```

That is **already reported** as
[#2244](https://github.com/kagent-dev/kagent/issues/2244) ("Default
regular Agent Deployments to satisfy restricted Pod Security"), open
since 2026-07-14. Two PRs aimed at it —
[#2251](https://github.com/kagent-dev/kagent/pull/2251) and
[#2462](https://github.com/kagent-dev/kagent/pull/2462) — were both
**closed without merging**, which is why the issue is still open.

A duplicate would be worthless. A comment carrying evidence they do not
have is worth something, and there is one specific thing worth saying:
**the image change makes their fix easier than it was when the issue was
written.** Draft 3 below.

---

## U3 — DO NOT FILE. Already implemented on `main`.

The candidate said `kagent invoke` emits raw JSON with no human-readable
mode. The board rated it **Low** confidence — "a preference, not a
defect, and possibly deliberate" — and said to offer it, not press it.

Verification improved the ask and then killed it.

**It got stronger first.** `kagent` has a *documented global* flag:

```
  -o, --output-format string   Output format (default "table")
```

`kagent get agent -o table` honours it (via `printOutput` /
`format.go`). `kagent invoke` never reads `output_format` at all — it
hand-rolls `fmt.Fprintf(os.Stdout, "%+v\n", string(jsn))`
(`invoke.go:169-175`, byte-identical at `v0.9.12` and `v0.10.1`). So
`kagent -o table invoke …` silently emits JSON. That is a consistency
defect rather than a preference, and it would have been a much better
issue than the one recorded.

**Then it died.** [#2559](https://github.com/kagent-dev/kagent/pull/2559)
("feat(cli): add API v2 AgentInstance workflows", merged 2026-08-29),
tidied by [#2606](https://github.com/kagent-dev/kagent/pull/2606),
already implements precisely what U3 asks for, on `main`:

- a root `-o/--output-format` defaulting to `table`, with a
  `FormatTable`/`FormatJSON` type that rejects anything else
  (`internal/output/output.go`);
- `invoke` rendering extracted text in table mode
  (`writeSendResult`/`writeTableResult`);
- incremental readable streaming that writes only the delta of the
  assembled text, while JSON mode still emits per-event lines for pipes;
- `input_required` / `auth_required` rendered as English.

Down to keeping machine output on the JSON path for pipes, that is the
ask. It is unreleased — `v0.10.1` predates it on the diverged release
line — but it is written.

**So the honest answer is a question, not an issue**, and it is small
enough that it may not be worth the user's time at all: whether the v2
CLI lands in a 0.10.x patch or waits for 0.11. That is a comment on an
existing thread if the user happens to want to know, and nothing
otherwise. **Recommendation: drop U3.**

(One genuine scrap, recorded rather than filed: `StreamA2AEvents`
(`utils.go:92-111`) has an `if verbose` whose two branches are identical
code, so `--verbose` does nothing on the stream path. It is also fixed on
`main`. Not worth an issue.)

---

# Draft 1 — U1

**File as:** 🐞 Bug. Title below. The template's dropdowns:
Affected Service = **Controller Service**; Severity = **Minor
inconvenience** (it has a workaround; claiming Blocker would be wrong and
would cost credibility).

Prerequisite checkboxes: "searched existing issues" ✅ (searched;
nothing), "latest version" ✅ (reproduced on 0.10.1, released
2026-09-08), "can consistently reproduce" ✅.

**Three things to know before pasting, because they are what keep this
issue from being closed:**

1. **Scope it to v0.10.x deliberately.** On `main` the `Agent` v1alpha2
   type, its reconciler and its Deployment are *gone* — replaced by
   `AgentTemplate`/`AgentInstance` v1alpha3, whose status is keyed on
   observed backend state and carries real revision tracking, so the bug
   class is architecturally absent there. The draft says this out loud.
   Saying it pre-empts a "being replaced, won't fix" close by showing we
   checked, and leaves the maintainer a real decision. `release/v0.10.x`
   is actively maintained (its HEAD landed 2026-09-08), kagent backports
   to release branches, and there are **no v0.11 tags at all** — not a
   beta — so v0.10.x is where users will be for a while.
2. **Do not call it a regression.** The `AvailableReplicas > 0` test was
   a *deliberate* fix (#2263) for #2250, where an agent with
   `replicas > 1` vanished when a single pod was disrupted. The ask is
   for a gap that fix did not cover, and the suggested change preserves
   its intent.
3. **Two claims from our own records were cut because the live run
   refuted or failed to support them** — see "What was cut" at the end
   of this document. Do not put them back.

> ### Title
>
> `[BUG] Agent Ready condition does not go False while the Deployment rolls, so `kubectl wait --for=condition=Ready` returns before the new pods are serving`
>
> ### 🐛 Bug Description
>
> When an `Agent`'s spec changes in a way that rolls its Deployment, the
> Agent's `Ready` condition stays `True` throughout. It never transitions
> to `False` and back.
>
> The consequence is that `kubectl wait --for=condition=Ready agent/<name>`
> — the obvious way to wait for an agent after changing it, and the one a
> script reaches for first — returns on a stale `True`, reporting on the
> *previous* rollout. In my measurement it returned **17 seconds before**
> the rollout actually finished.
>
> This is deterministic rather than a race. `reconcileAgentStatus`
> (`go/core/internal/controller/reconciler/reconciler.go`) decides Ready
> from `AvailableReplicas` alone:
>
> ```go
> if deployment.Status.AvailableReplicas > 0 {
>     deployedCondition.Status = metav1.ConditionTrue
>     deployedCondition.Reason = AgentReadyReasonDeploymentReady
>     deployedCondition.Message = "Deployment is ready"
> }
> ```
>
> It never reads `UpdatedReplicas` or the Deployment's own
> `ObservedGeneration`, so it structurally cannot distinguish "the new
> pod is up" from "an old pod is up". And because the generated
> Deployment uses `maxUnavailable: 0, maxSurge: 1` (confirmed on the
> live object), at least one old pod stays Available for the whole
> rollout — so `AvailableReplicas > 0` holds continuously and Ready
> *cannot* go False during a spec change.
>
> I want to be careful about one thing: I can see that the
> `AvailableReplicas > 0` test is deliberate — #2263 introduced it to fix
> #2250, where an agent with `replicas > 1` disappeared when a single pod
> was disrupted. That fix is about **availability during disruption**;
> this report is about **update completion**, which it did not aim to
> cover. I am not asking to revert it.
>
> I am also unsure whether the intended meaning of `Ready` is "the
> current spec is serving" or "some pod is serving". Either is
> defensible; right now the name suggests the first and the behaviour is
> the second, and nothing in the docs says which to expect.
>
> ### 🔄 Steps To Reproduce
>
> On a fresh kind cluster with kagent 0.10.1 installed (`kagent-crds` +
> `kagent` Helm charts), in the `kagent` namespace:
>
> 1. Create two ModelConfigs that differ in *effective* configuration, so
>    the rendered pod spec genuinely changes. (Two configs naming the same
>    provider, endpoint and model render an identical Deployment, roll
>    nothing, and will not show this.) The chart already installs
>    `default-model-config` (provider `OpenAI`); add a second:
>
>    ```yaml
>    apiVersion: kagent.dev/v1alpha2
>    kind: ModelConfig
>    metadata:
>      name: ollama-model-config
>      namespace: kagent
>    spec:
>      provider: Ollama
>      model: qwen2.5:3b
>      ollama:
>        host: http://ollama.ollama.svc.cluster.local:11434
>    ```
>
> 2. Create an Agent on the first one:
>
>    ```yaml
>    apiVersion: kagent.dev/v1alpha2
>    kind: Agent
>    metadata:
>      name: switch-demo
>      namespace: kagent
>    spec:
>      type: Declarative
>      description: demonstrates the Ready condition during a switch
>      declarative:
>        runtime: python
>        modelConfig: default-model-config
>        systemMessage: hello
>    ```
>
>    Wait for it to come up, and note when `Ready` last transitioned:
>
>    ```bash
>    kubectl -n kagent get agent switch-demo \
>      -o jsonpath='{range .status.conditions[*]}{.type} {.status} {.lastTransitionTime}{"\n"}{end}'
>    ```
>
> 3. Switch it to the other ModelConfig and wait the way a script would:
>
>    ```bash
>    date -u +%H:%M:%S.%N
>    kubectl -n kagent patch agent switch-demo --type merge \
>      -p '{"spec":{"declarative":{"modelConfig":"ollama-model-config"}}}'
>    kubectl -n kagent wait --for=condition=Ready agent/switch-demo --timeout=120s
>    date -u +%H:%M:%S.%N          # <-- returns almost immediately
>    kubectl -n kagent rollout status deploy/switch-demo --timeout=180s
>    date -u +%H:%M:%S.%N          # <-- ~18s later
>    ```
>
> 4. Immediately after both commands return, list the pods with their
>    template hash and deletion timestamp:
>
>    ```bash
>    kubectl -n kagent get pods -l kagent=switch-demo \
>      -o jsonpath='{range .items[*]}{.metadata.name} {.metadata.labels.pod-template-hash} {.status.phase} del={.metadata.deletionTimestamp} ready={.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}'
>    ```
>
> 5. Re-read the `Ready` condition's `lastTransitionTime`. It is unchanged
>    from step 2.
>
> ### 🤔 Expected Behavior
>
> Either:
>
> - `Ready` goes `False` while the new template is rolling and back to
>   `True` when the agent is serving the current spec — so
>   `kubectl wait --for=condition=Ready` is a real barrier; or
> - the documentation states that `Ready` does not track rollout, and says
>   what to wait on instead.
>
> ### 📱 Actual Behavior
>
> Measured on kagent 0.10.1, kind, Kubernetes v1.32.3:
>
> - Patch applied at `04:02:06.428`.
> - `kubectl wait --for=condition=Ready agent/...` returned at
>   `04:02:07.028` — **0.6 seconds later**, "condition met".
> - `kubectl rollout status deploy/...` did not return until
>   `04:02:24.168` — **18 seconds after the patch**, having printed
>   `Waiting for deployment ... 1 old replicas are pending termination...`
>   twice.
>
> So the Ready wait returned about 17 seconds before the rollout finished.
>
> Sampling the Agent's `Ready` condition roughly every 0.9s across a full
> switch, **163 of 163 samples read `True`**. It never went `False`. The
> condition's `lastTransitionTime` after the switch was still the
> timestamp from the *initial* rollout, 26 seconds before the patch.
>
> To be clear about what is *not* wrong here: `kubectl rollout status`
> behaved correctly, taking the full 18 seconds and printing
> `Waiting for deployment ... 1 old replicas are pending termination...`
> along the way. The defect is in the Agent's `Ready` condition only.
>
> ### 🔍 Additional Context
>
> **Why this matters beyond scripts: kagent's own MCP surface keys on
> this condition.** `list_agents` filters on
> `reason == "DeploymentReady"` (noted in #2123 in another context), so
> during a spec change kagent advertises an agent as ready on its new
> configuration while the pod serving it is still the old one.
>
> **What this costs downstream.** Anything driving kagent
> programmatically — CI, a controller, an operator's script — needs a
> "the agent is now serving the new spec" barrier. The obvious candidate
> is `Ready`, and it silently is not one, so the working wait becomes
> two steps that a consumer has to discover for themselves:
>
> 1. wait for the Agent's `status.observedGeneration` to reach
>    `metadata.generation` — otherwise step 2 can be looking at the
>    *previous* rollout, because reconcile is asynchronous;
> 2. then `kubectl rollout status` on the Deployment.
>
> We arrived at that the expensive way, each wait added after something
> passed without it. The failure mode while a consumer has not yet
> discovered it is a silent one: a request answered plausibly, by the
> previous configuration, with nothing in any status to say so.
>
> **A note on `observedGeneration`, in kagent's favour.** The Agent has
> `status.observedGeneration`, and each condition carries its own
> `observedGeneration` too, so a correct wait *is* constructible today —
> this is not a missing field, and I checked that the controller stamps
> it after the Deployment is patched, which is what makes it usable as a
> barrier. (One caveat for anyone relying on it: it is stamped on the
> failure path too, so it should be paired with `Accepted=True`.) The
> problem is only that the obvious command is not that wait and does not
> say so.
>
> **On `main` this shape is gone.** I checked before filing: `main`
> replaces `Agent` v1alpha2 with `AgentTemplate`/`AgentInstance`
> v1alpha3, whose Ready condition is set from observed backend state
> (`ActorTemplatePending` while the golden snapshot is absent) and whose
> status carries `DesiredRevision` vs `LatestSuccessfulRevision`. So this
> is a v0.10.x-line report, not a request to redesign something you are
> already replacing. I am raising it because `release/v0.10.x` is still
> being maintained and there are no v0.11 tags yet, so it looks like the
> line users will be on for a while. If you would rather not touch it,
> that is a completely reasonable answer and the documentation note below
> would still help.
>
> ### 💡 Smallest change that would fix it, offered as a suggestion
>
> In `reconcileAgentStatus`, additionally require
> `UpdatedReplicas == *Spec.Replicas` and
> `deployment.Status.ObservedGeneration >= deployment.Generation` before
> reporting `DeploymentReady`, so Ready is `False` (reason
> `Progressing`) while an update is in flight. Since #2250 was about
> availability during disruption rather than update completion, that
> should not reopen it — an agent with healthy replicas and no spec
> change still reports Ready.
>
> If the current meaning is deliberate, a sentence in the Agent docs
> saying `Ready` does not track rollout, and naming
> `status.observedGeneration` as the thing to wait on instead, would be
> worth nearly as much and costs almost nothing.
>
> ### 💻 Environment
>
> - Kubernetes: v1.32.3 (kind)
> - kagent: 0.10.1, installed from the `kagent-crds` and `kagent` Helm
>   charts
> - `Agent` `kagent.dev/v1alpha2`, `runtime: python`, `replicas: 1`
>
> ### 🙋 Are you willing to contribute?
>
> Happy to open a PR for either the status change or the doc note, if you
> say which you would take.

---

# Draft 2 — U4

**File as:** 🐞 Bug. Affected Service = **Controller Service**;
Severity = **Minor inconvenience**.

**Read this before pasting.** kagent's own source *already documents this
asymmetry*, in a comment on the RemoteMCPServer type. The issue must
therefore be framed as "you noted this gap; here is what it costs" — not
as a discovery. Leading with "we found an inconsistency" would read as
not having looked at the code. The draft below does that, and it also
concedes the reason the rule is harder for ModelConfig than it was for
RemoteMCPServer, because a maintainer will raise it in the first reply
otherwise.

> ### Title
>
> `[BUG] ModelConfig admits spec.tls beside an http:// baseUrl (the pairing RemoteMCPServer rejects), and the runtime logs "Custom CA certificate loaded" for a plaintext connection`
>
> ### 🐛 Bug Description
>
> `RemoteMCPServerSpec` carries a spec-level `XValidation` rule rejecting
> a `spec.tls` block beside an `http://` URL:
>
> ```
> spec.tls must be unset when spec.url has http:// scheme: a TLS opinion
> contradicts a plaintext URL. Either drop spec.tls, or use https:// / a
> scheme-less URL.
> ```
>
> `ModelConfig` has no equivalent rule, and the code says so out loud —
> `remotemcpserver_types.go`, on the `TLS` field:
>
> ```go
> // Note one asymmetry with ModelConfig: a spec-level XValidation rule
> // on RemoteMCPServer rejects spec.tls when spec.url has the http://
> // scheme (a TLS opinion contradicts a plaintext URL). ModelConfig has
> // no equivalent rule, so a TLS block can sit alongside any baseUrl.
> ```
>
> So the asymmetry is known and intentional to leave. This report is
> about what it costs the operator, which I do not think is visible from
> the type definition: **the mistake is not merely unvalidated, it is
> actively corroborated by the runtime's logs.** The manifest reads as
> configured, the startup logs say the custom CA was loaded, and every
> byte leaves in clear.
>
> ### 🔄 Steps To Reproduce
>
> On a fresh kind cluster with kagent 0.10.1 installed:
>
> 1. Apply a ModelConfig with a TLS block beside a plaintext baseUrl:
>
>    ```yaml
>    apiVersion: kagent.dev/v1alpha2
>    kind: ModelConfig
>    metadata:
>      name: plaintext-with-tls-block
>      namespace: kagent
>    spec:
>      provider: OpenAI
>      model: gpt-4o-mini
>      apiKeySecret: some-secret
>      apiKeySecretKey: OPENAI_API_KEY
>      openAI:
>        baseUrl: http://gateway.example.svc.cluster.local:8080/v1
>      tls:
>        caCertSecretRef: my-ca
>        caCertSecretKey: ca.crt
>    ```
>
>    → `modelconfig.kagent.dev/plaintext-with-tls-block created`
>
> 2. Apply the same mistake on the other kind:
>
>    ```yaml
>    apiVersion: kagent.dev/v1alpha2
>    kind: RemoteMCPServer
>    metadata:
>      name: plaintext-with-tls-block
>      namespace: kagent
>    spec:
>      description: the same mistake, on the other kind
>      url: http://gateway.example.svc.cluster.local:8084/mcp
>      protocol: STREAMABLE_HTTP
>      tls:
>        caCertSecretRef: my-ca
>        caCertSecretKey: ca.crt
>    ```
>
>    → ```
>      The RemoteMCPServer "plaintext-with-tls-block" is invalid: spec:
>      Invalid value: "object": spec.tls must be unset when spec.url has
>      http:// scheme: a TLS opinion contradicts a plaintext URL. Either
>      drop spec.tls, or use https:// / a scheme-less URL.
>      ```
>
> 3. Point an Agent at the ModelConfig from step 1 and read the agent
>    pod's startup logs.
>
> ### 📱 Actual Behavior
>
> Step 1 is admitted; step 2 is rejected. Both were run on kagent 0.10.1
> — the pasted outputs above are verbatim.
>
> Then the runtime reinforces the mistake. `create_ssl_context` is not
> passed the URL, so it cannot know the scheme:
>
> ```python
> def create_ssl_context(
>     disable_verify: bool,
>     ca_cert_path: str | None,
>     disable_system_cas: bool,
> ) -> ssl.SSLContext | bool:
> ```
>
> With the manifest above it logs, for a connection that carries no TLS
> at all:
>
> ```
> TLS Mode: Custom CA + System CAs (additive)
> Using system CA certificates
> Custom CA certificate loaded from: /etc/ssl/certs/custom/my-ca/ca.crt
> ```
>
> The Go path is the same shape — `BuildTLSTransport(base,
> insecureSkipVerify, caCertPath)` also takes no URL and sets
> `TLSClientConfig` on a transport that `http://` requests never consult.
> And with `disableVerify: true` the operator gets the full
> "SSL VERIFICATION DISABLED" banner about a connection that was never
> going to be encrypted.
>
> The controller mounts the CA volume regardless of scheme and raises no
> condition or event about it.
>
> ### 🤔 Expected Behavior
>
> The same thing that happens on `RemoteMCPServer`: the pairing is
> refused at admission, with a message naming the contradiction.
>
> Failing that, the manifest should at least not produce logs asserting
> that a custom CA is in use on a connection that has no TLS.
>
> ### 🔍 Additional Context — including why this is harder than it looks
>
> **Why I am not just asking you to copy the one-liner.**
> `RemoteMCPServer` has a single `spec.url`, so its rule is
> `!self.url.startsWith('http://') || !has(self.tls)`. `ModelConfig`
> spreads the endpoint across **seven** inline fields in five provider
> blocks:
>
> | field | note |
> |---|---|
> | `spec.anthropic.baseUrl` | |
> | `spec.openAI.baseUrl` | |
> | `spec.azureOpenAI.azureEndpoint` | required |
> | `spec.ollama.host` | not named `baseUrl` — easy to miss |
> | `spec.sapAICore.baseUrl` | required |
> | `spec.sapAICore.authUrl` | **the OAuth2 token endpoint** |
> | `spec.foundry.endpoint` | |
>
> (`gemini`, `bedrock`, `geminiVertexAI` and `anthropicVertexAI` carry no
> user-settable URL, so there is nothing to check on those.)
>
> `spec.sapAICore.authUrl` is worth calling out separately: it is where
> the OAuth2 credentials are exchanged, so a plaintext `authUrl` leaks
> the token exchange even when `baseUrl` is `https`.
>
> And `spec.foundry.endpointFrom` is a `ConfigMapKeySelector`, whose
> value CEL cannot dereference at admission time at all — so that one
> case is structurally uncoverable by an `XValidation` rule. That is a
> real difference from `RemoteMCPServer`, and I assume it is part of why
> the rule was never lifted across.
>
> So the ask splits, and the second half is the one I would actually
> argue for:
>
> 1. **Admission**, for the seven inline fields above — a disjunction
>    rather than a one-liner, and one that needs extending whenever a
>    provider gains a URL. Catches the common case at apply time.
> 2. **A status condition from the controller** for what CEL cannot see
>    (`foundry.endpointFrom`, whose value *is* resolvable at reconcile
>    time, and as a backstop for providers added later), plus **not
>    logging "Custom CA certificate loaded" when the resolved endpoint is
>    `http://`**. Passing the resolved URL into `create_ssl_context` /
>    `BuildTLSTransport` would be enough for the logging half.
>
> If only one of those is worth doing, (2) is the one that removes the
> misleading signal, and it covers every provider including future ones
> without a rule that has to be maintained per field.
>
> **What it costs downstream.** We run agents against an in-cluster
> gateway and had to be sure every manifest reaching it was actually
> encrypted. Because `RemoteMCPServer` is checked at admission and
> `ModelConfig` is not, we could not rely on the cluster to tell us, and
> ended up writing a repository-wide checker that parses every committed
> manifest and refuses a `ModelConfig` whose `tls` block sits beside a
> non-`https` URL — reimplementing, in a linter, the rule the API server
> already applies to the neighbouring kind. Anyone running models through
> an internal gateway with a private CA hits the same asymmetry, and the
> silent-downgrade direction is the dangerous one: it reads exactly like
> a manifest that is configured correctly.
>
> **Prior art.** #1905 is the PR that added the `RemoteMCPServer` rule
> and lifted the `caCertSecretRef`/`caCertSecretKey` pairing rules onto
> the shared `TLSConfig` type. Its own description states the intended
> contract — "Only one combination is admission-rejected: `http://` URL +
> non-nil `spec.tls`" — for `RemoteMCPServer`. This issue is asking
> whether that contract should extend to the other consumer of the same
> `TLSConfig` type.
>
> To be explicit about one thing that is **not** part of this report: an
> `https://` baseUrl with **no** `spec.tls` is fine and should stay
> admitted — verifying against the system trust store is the correct
> default, and #1905 says so.
>
> ### 💻 Environment
>
> - Kubernetes: v1.32.3 (kind)
> - kagent: 0.10.1 (also present at 0.9.12 and on `main`; on `main` the
>   group has moved to `kagent.dev/v1alpha3` and the asymmetry is
>   unchanged)
>
> ### 🙋 Are you willing to contribute?
>
> Yes — happy to open a PR for the admission rule, the logging change, or
> both, if you tell me which shape you would accept.

---

# Draft 3 — a comment on the open issue #2244, not a new issue

**Post to:** https://github.com/kagent-dev/kagent/issues/2244 —
"Default regular Agent Deployments to satisfy restricted Pod Security",
open since 2026-07-14.

**Why a comment.** The issue is open, correct, and already well
described; two PRs at it were closed unmerged. A duplicate would add
nothing. What we can add is a fact the thread does not contain — the
runtime image change has removed the obstacle that made this awkward
when the issue was written.

> Still reproduces on 0.10.1 (fresh kind cluster, chart install). The
> Deployments the controller creates from an `Agent` carry an empty
> security context at both levels — from the chart's own `k8s-agent`:
>
> ```
> spec.securityContext:                     {}
> spec.containers[0].securityContext:       <unset>
> image:                                    ghcr.io/kagent-dev/kagent/golang-adk:0.10.1
> ```
>
> One thing that has changed since this was filed, and it makes the fix
> cheaper than it looks: **the runtime images now declare a numeric
> user**, so defaulting `runAsNonRoot: true` no longer requires the
> controller to know a UID per image.
>
> Until #2498 the Python runtime image ended in `USER python`, and
> Kubernetes refuses a `runAsNonRoot` container whose user it cannot
> prove is non-root — `image has non-numeric user (python), cannot verify
> user is non-root`, raised at CreateContainer time, and the message
> names neither the image nor its version. Hardening agents therefore
> meant pinning `runAsUser` to a number discovered by running the image,
> which is not something a controller default can do.
>
> From `v0.10.0-beta5` onward the image ends in `USER 65532:65532`. I
> checked that this is enough on 0.10.1 by applying an Agent with
> `runAsNonRoot: true` and **no `runAsUser`**:
>
> ```yaml
> podSecurityContext:
>   runAsNonRoot: true
>   seccompProfile:
>     type: RuntimeDefault
> securityContext:
>   allowPrivilegeEscalation: false
>   capabilities:
>     drop: [ALL]
> ```
>
> The pod starts:
>
> ```
> STATE={"running":{"startedAt":"2026-09-09T04:00:14Z"}}
> STARTED=true
> ```
>
> So the container-level defaults this issue asks for
> (`runAsNonRoot`, `allowPrivilegeEscalation: false`,
> `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`) can now
> be applied without the controller carrying any per-image UID knowledge,
> with user-supplied values still winning.
>
> For what it is worth as a data point on impact: we set exactly that
> context by hand on every agent we run, and the only reason it was not a
> one-line default for us was the non-numeric user.

---

## What was cut from U1, and why it matters that it was

The board's U1 row and its written-out reproduction assert two things
that the live run on 0.10.1 **did not support**. Both were removed from
the draft. Recording them here so nobody helpfully puts them back.

1. **"`kubectl rollout status` can report on the OLD template."**
   Cut as stated. In the measured run `rollout status` behaved
   *correctly* — it took the full 18 seconds and printed
   `1 old replicas are pending termination...` while waiting. The true
   statement is narrower: `rollout status` can look at the previous
   rollout if it is run before the controller has reconciled the Agent
   at all, which is why the `observedGeneration` wait has to come first.
   That is a sequencing requirement, not a defect in `rollout status`,
   and the issue says so.

2. **"a turn issued immediately afterwards runs on the previous
   configuration."** This was the original 0.9.12 symptom — a governed
   chat completed and the ledger had zero rows, because the ungoverned
   pod answered. **It did not reproduce on 0.10.1.** Sampling the
   Service's EndpointSlice roughly every 0.9 seconds across a full
   switch, the two pods were never both `ready=true`; the endpoint
   flipped cleanly. The old pod was indeed still Running and Ready with a
   `deletionTimestamp` after the waits returned, but that is ordinary
   Kubernetes behaviour under `maxSurge: 1`, and whether a request
   actually lands there depends on EndpointSlice and kube-proxy timing
   that kagent does not control. Claiming it without evidence would have
   been the strongest sentence in the issue and the one a maintainer
   could most easily disprove.

The issue is better for losing both. What is left — Ready returning in
0.6s against an 18s rollout, 163/163 samples True, and the
`AvailableReplicas > 0` source line that explains exactly why — is
entirely reproducible and needs no interpretation.

This is the second time on this board that a claim arriving inside the
prompt turned out to be looser than the evidence behind it. The rule that
caught it is the same one: measure it again before repeating it in
public.

## For the coordinator

Four things this lane produced that the board should absorb. The lane
did not edit `docs/COORDINATION.md` — that file has a single writer.

1. **U2b and U3 should be struck from the candidates table**, with the
   reason recorded rather than the rows deleted. Both died the same way
   the withdrawn U2 died: kagent already ships the thing. That is now
   three out of four, and it is a pattern worth naming — **a candidate
   recorded against a pinned version decays**, because the fix can land
   upstream at any time and nothing here notices. The board's rule says
   to record a workaround with its reproduction; it does not say to
   re-check it before filing, and it should.

2. **U4 is new and is not on the board**, which is itself a finding:
   the seam-TLS lane (#150) worked around kagent behaviour — it wrote
   `scripts/check-seam-tls.py`, whose docstring states the asymmetry
   plainly — and did not record it in the candidates table, which the
   board's own rule requires in the same PR. It is the strongest of the
   four.

3. **The pin is now three minor versions behind** (0.9.12 vs 0.10.1,
   with `main` diverged 143 ahead). Not this lane's work, but two things
   follow that someone will hit: the group moves to `kagent.dev/v1alpha3`
   on `main`, and `scripts/check-agent-uid.py` will correctly fail the
   moment the pin moves to 0.10.x, because the image's uid is now
   **65532**, not 1001. That checker did exactly its job here.

4. **U1's row overstates its own evidence in two places** — see "What
   was cut" above. The row should be corrected whether or not the issue
   is ever filed, because the next lane to read it will inherit the
   overstatement. `wait_switched`'s third wait (the pod-template-hash
   poll) is the one now lacking a stated justification against 0.10.1:
   it was added for the terminating-pod case, and the terminating pod
   turns out to be ordinary Kubernetes. That is **not** a suggestion to
   remove it — it may well still be earning its place on 0.9.12, which
   is what we pin — but its comment claims more than this pass could
   confirm, and somebody should measure it before the pin moves.

5. **A live cluster changed three of the four verdicts.** Source
   reading alone would have filed U2b (the Dockerfile fix is invisible
   from our checkout), would have kept U1's two overstated claims, and
   would not have produced the 0.6s-versus-18s measurement that is now
   the whole of U1. The kind cluster cost about fifteen minutes. Where a
   candidate says "we hit this", hitting it again is the cheap part.
