# Migrating model traffic onto Orka

[Orka](orka.md) is the platform; Kaimahi tooling helps applications get onto it.
`kmx migrate` puts an existing application's **model traffic** through the
Kaimahi seam to Orka. It does not migrate the application into an Orka-managed
runtime, govern its tools, or transfer ownership of its Deployment.

No source edit, rebuilt image or chart fork is required for the supported
application shape. kmx writes four environment variables and one mounted CA file
as a Deployment patch; **the workload owner reviews and applies that patch**.
This bridge should shrink as Orka supplies the needed capabilities. Whether new
agents should use native Orka authoring only or kagent YAML over Orka is open;
this migration does not settle it. `orka.harness.v2` is outside the direction.

## Prerequisites

- A current development build of kmx; the published `v0.1.0` predates Orka
  commands. See [installation](kmx.md#install), not the release quickstart.
- An explicitly selected cluster: `kmx ctx <context>` or `kmx --context <context>`.
  Non-local mutations require confirmation naming that context; see
  [target safety](kmx.md#where-the-command-will-land).
- Orka installed, with a ready Provider for the requested model. Start with
  `kmx orka install` / `kmx orka status` and [the Orka guide](orka.md).
  Installing Orka alone does not enable this model governance.
- The Kaimahi plane deployed (`kmx plane` on kind; [AKS phases](aks.md) on AKS).
  Upgrading an older plane requires the [retirement steps](operations.md#upgrading-after-approval-retirement);
  stale tool/inbound/notifier configuration is rejected, and apply does not
  prune retired Services, network allowances or owner-managed references.
- An existing Deployment whose model base URL is configurable through its
  environment, and an application that can trust the mounted CA. The default
  variables target the OpenAI Python client shape; they are not proof that an
  arbitrary image reads those variables.

The current committed `orka` route accepts **non-streaming Responses API**
requests, translating to chat completions. A client posting chat completions to
that same seam is not supported merely because the base URL ends in `/v1`.
Review [translation limits](#responses-translation-and-refusals) before rollout.

## Procedure

Use an existing Deployment and Provider/model name appropriate to your cluster;
`concierge`, `demo`, and `local/qwen2.5:3b` below are example workload values.

```bash
kmx ctx <context>
kmx orka status
kmx migrate concierge --namespace demo --model local/qwen2.5:3b
```

`--namespace` and `--model` are required, with no inferred workload namespace or
model. With several containers, name the model client using `--container`.
For different variable names use `--base-url-var`, `--key-var`, `--model-var`;
`kmx migrate --help` lists identity, token-duration and output options.

The command:

1. Reads the live Deployment. It resolves ConfigMap `envFrom` sources in order,
   including prefixes, then explicit `env` overrides. It does not read Secrets
   to discover configuration; explicit references are reported as references.
   A missing base-URL variable or ambiguous container is refused.
2. Checks the Provider prefix in `<provider>/<model>` in `--orka-namespace`
   (default `orka-system`). A missing or not-ready Provider is refused before
   files are written. **Other read failures warn and continue**, leaving Orka to
   resolve the model; an unprefixed model also warns about the `default`
   Provider. This is not a universal fail-closed Provider preflight.
3. Writes the two files below. Identical files may be reused; differing files
   are refused so operator edits are not overwritten.
4. Applies the identity and namespace ingress allowance behind the context guard.
5. Mints Orka's ServiceAccount token, pipes it into `kaimahi/kaimahi-orka-token`,
   prints the granted expiry, and restarts the proxy to project the Secret.
6. Issues/reconciles the application's governed credential into its namespace
   and publishes `kaimahi-plane-ca` there. Tokens never enter argv or generated
   files. The plane stores only the governed token's hash.
7. Prints the owner's patch command and the values to carry into their release.

| Artifact | Contents | Applied by |
|---|---|---|
| `migrations/concierge.yaml` | Orka-namespace ServiceAccount, Role, RoleBinding; plane-namespace model ingress NetworkPolicy | kmx |
| `migrations/concierge-patch.yaml` | selected container's environment, CA Secret volume and mount | workload owner |

Review the files, then apply **only when ready to change the workload**:

```bash
kubectl --context <context> -n demo patch deployment concierge \
  --patch-file migrations/concierge-patch.yaml
kubectl --context <context> -n demo rollout status deployment/concierge
```

The default patch sets:

| Variable | Value/source |
|---|---|
| `OPENAI_BASE_URL` | `https://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/orka/v1` |
| `OPENAI_API_KEY` | Secret `kaimahi-concierge-token`, key `api-key` |
| `OPENAI_CHAT_MODEL` | the explicitly requested model |
| `SSL_CERT_FILE` | `/etc/kaimahi/plane-ca/ca.crt`, from the mounted CA Secret |

Explicit `env` overrides `envFrom`; the original ConfigMap is untouched.
**A Helm upgrade can remove this patch.** Move the variables, Secret references,
volume and mount into the application's own release configuration. Do not put
credential values into `--set`, values files or shell history.

For artifact review, `--no-apply` reads the workload/Provider and writes files
but makes no cluster mutation and issues no credentials. Re-run without it to
finish setup. `--dry-run` writes the files and server-validates the identity and
access manifest; it does **not** prove credential issuance, the owner's patch,
application compatibility or a model turn. These are not offline generation modes.

## Verify the migration

Ask the application a fresh question through its normal interface, then inspect:

```bash
kmx flow concierge
kmx ledger concierge
kmx credentials
```

Check actual model rows, model name, upstream, token counts and outcomes; a Ready
Deployment or a plausible answer alone is not evidence of governance. Exercise
multi-turn/tool-calling conversations too, including the continuation limit below.

- Missing/unknown seam credentials are refused. Unauthenticated requests have no
  attributable ledger row; look at proxy metrics instead.
- Orka resolves model names against its Providers; endpoint error bodies are
  relayed untranslated. Provider resolution is not a per-credential allowlist.
- `orka` is `metered` without configured prices. Tokens are counted, but `0 cents`
  is not evidence of free inference. A cents budget denies an unpriced pair;
  configure reviewed prices or use the appropriate token budget.
- `flow` and `watch` read only the model ledger. Credential and timestamp form a
  chronological view, not a causal correlation ID. Concurrent
  turns can interleave; this migration still governs only model traffic.
- The generated ingress rule admits the application's namespace to the model
  port only. Tool access is a separate decision. On an enforcing CNI, removing
  this allowance blocked the measured probe; merely applying a NetworkPolicy
  is not proof that your cluster enforces it.

## Credential expiry and renewal

There are **two independent deadlines**:

| Credential | Custody | Renewal |
|---|---|---|
| Orka ServiceAccount token | plane namespace, mounted by proxy | re-run the same `kmx migrate` invocation |
| Application's `kmh_` credential | application Secret; hash/deadline in plane | `kmx credential renew <credential> --ttl 720h` |

The Orka TokenRequest asks for `720h` by default; **the API server chooses the
actual lifetime**. Recorded runs received 30 days on kind and 24 hours on AKS.
Neither is a portable guarantee. Read the printed expiry and schedule renewal;
there is no automatic refresh here. `--token-duration` changes the request, not
that server-side cap.

A repeat migration refreshes the Orka token and rolls the proxy. It preserves an
already-bound application credential and Secret; it does not renew that
credential's deadline or patch the Deployment. `kmx credentials` reports the
application deadlines; renewal extends a date, not token material.

The default credential name is the Deployment name, shared across namespaces in
the plane. Two Deployments with the same name can collide. Choose a distinct
`--credential` rather than deleting another workload's live credential. A token
shown once by the plane cannot be recovered; missing/misbound Secrets require
explicit recovery, not a silent overwrite.

The Orka upstream Secret is shared by the committed routes. Choosing another
ServiceAccount/namespace and refreshing it affects that shared upstream identity;
it does not create a per-application Orka token mount.

## Responses translation and refusals

The reviewed configuration lives in
[`k8s/plane/upstreams.yaml`](../k8s/plane/upstreams.yaml):

```json
"orka": {
  "base_url": "http://orka-api.orka-system.svc.cluster.local:8080/openai",
  "path": "v1/chat/completions",
  "client_path": "v1/responses",
  "classification": "metered",
  "credential_file": "/etc/kaimahi/upstream-creds/orka/token",
  "extra_headers": {"X-Orka-Tools": "disabled"}
}
```

This is the only translation pairing implemented; unsupported pairings fail at
config load. The forwarded chat-completions path is not a second client route.
The translator carries text and function-call turns, restates supported request
settings, and reports token-bound answers as incomplete. It does not implement
the entire Responses API:

- Non-empty `previous_response_id` and `store: true` are refused: the seam holds
  no conversation state. Send the whole conversation in `input`.
- Streaming on a translating upstream is refused before upstream execution;
  chat SSE and Responses semantic events are not interchangeable.
- Unknown fields, unsupported nested fields, non-text modalities, non-function
  tools and unsupported truncation settings are refused by name, not dropped.
- `include` accepts only `reasoning.encrypted_content`: it requests optional
  output a chat completion does not carry, so the response contains none. Other
  include values are refused.
- Metering reads the upstream's own usage under its own protocol, independently
  of translation. An untranslatable successful reply fails closed but its billed
  usage is still ledgered; upstream error bodies are not translated.

The header is functional, not a tuning option. In the measured Orka `v0.1.3`,
the compatible endpoint otherwise injected coordinator instructions/tools and
ran its own loop. The `orka` entry disables that loop so the application's agent
can run its own tools. `orka-coordinator` omits the header for comparison; it is
not the application-migration recommendation. Recorded coordinator calls timed
out at the seam's five-minute upstream bound, with retries and server-side work
continuing after the caller was gone. This is version-scoped evidence, not a
performance prediction for other models or Orka versions.

## Continuation incompatibility

The recorded application used Microsoft Agent Framework's OpenAI client 1.14.
Some follow-up turns requested `previous_response_id` and were correctly refused.
The observed mechanism was:

1. The seam returned a `resp_…` ID and `store: false`.
2. The framework derived conversation state from its **outgoing** `store` option,
   not the response's `store` field. With that option unset, it kept the ID.
3. On the continuation path it sent `previous_response_id` and stripped
   server-issued item identities from the inlined history, assuming the server
   had retained them.
4. The seam refused rather than answer from history it did not possess.

This establishes an incompatibility in that client/translation combination, not
an AKS failure rate or a universal claim about tool-calling turns. The historical
AKS sample included both successes and refusals; the frequency and selection of
that path were not established across clients or clusters. Ignoring the marker
is not a safe fix. Test your client's explicit stateless/full-history behavior;
no universally verified client workaround is claimed here.

## 8. Limits, stated

The recorded kind (2026-09-09) and AKS (2026-09-10) migrations used Orka `v0.1.3`,
`sundae-funday` at `bd2a035`, and local `qwen2.5:3b`. Successful application tool
turns produced model ledger rows; repeated migrations preserved the owner
Deployment and bound application token. The original composition investigation
is [here](reviews/2026-09-09-orka-composition.md).

- On that Orka tag, `/openai/v1/responses` returned dashboard HTML with status
  200, and the compatible endpoint authenticated without per-route authorization.
  The generated Role (`create` on `core.orka.ai/chats`) was checked via explicit
  SubjectAccessReview, **not observed enforcing at the endpoint**. Route
  authorization arrived later; Orka `main` was not tested by those runs.
  Unqualified `kubectl auth can-i` was misleading for the unregistered `chats`
  resource; use the explicit group/resource review when checking this permission.
- The endpoint address is committed. `--orka-namespace` selects identity/Provider
  operations; it does not rewrite that URL. An Orka installation elsewhere needs
  a reviewed table change and plane redeploy.
- Application-to-seam traffic uses TLS. The committed seam-to-Orka hop is HTTP
  on 8080, including the ServiceAccount bearer, bounded by network policy.
- Every plane credential can reach each configured model upstream; there is no
  model per-credential allowlist. Budgets bound spend, not destination choice.
- Tool traffic remains the application owner's responsibility. The Kaimahi tool
  gateway is retired; do not infer tool governance from a model ledger row or
  silently repoint an application's tools during an upgrade.
- A full `kmx lift` still installs kagent/Copilot demo agents and obtains a
  Copilot credential by device login if absent. Orka migration uses selected phases;
  [AKS](aks.md) records monitoring, ownership and cloud verification limits.

## Retirement regression evidence

The migration implementation in `internal/kmx/app/migrate.go` and
`internal/kmx/scaffold/migrate.go` remains byte-identical for generated-artifact
and rerun compatibility. Historical tool wording in generated comments does not
restore runtime tool governance.

### Gateway retirement

**Historical — PR #184 (recorded 2026-09-12).** This evidence predates final
approval retirement. Its positive budget-grant result describes the prior
contract-4 runtime, **not current grant authority**.

The runtime at `e9373d8ed61e` was exercised against a fresh, dedicated kind
cluster: Orka `v0.1.3`, Ollama `qwen2.5:3b`, and an owner-managed Python 3.12
standard-library HTTP client. Baseline `a8694b8` first migrated the workload;
the owner applied the generated patch and recorded a real 44-token model turn.

After upgrading the same plane/database to contract 4:

- The identical migration invocation accepted its existing identity/patch files
  as byte-identical and reused its bound credential. Snapshots confirmed the
  owner Deployment UID, generation and specification, token, original ConfigMap
  and application source were unchanged.
- A real Responses request returned `BRIDGE WORKS.` and recorded 39 input / 6
  output tokens. The pre-upgrade ledger row survived.
- A pre-upgrade tool grant, still within its expiry/use bounds, was readable
  but inactive. A historical pending tool request could not be approved
  (HTTP 400, unsupported kind) but could be denied.
- A zero token cap denied a model request (429). An admin-approved one-use budget
  grant admitted one real model turn (200, 42 tokens); the following turn was
  denied (429). This is an automatic admin fixture, not a human approval test.
- Fresh migration issued a new credential without patching the owner's
  Deployment. Owner application of that patch enabled a separate real answer
  and its own 44-token ledger row.

Both replicas became Ready with only model/admin/ops ports. The old MCP Service
survived apply and was explicitly deleted in the disposable cluster. This proves
model migration and the then-retained budget-grant boundary, not arbitrary SDKs,
network policy enforcement, tool routing, AKS or the current contract-5 retirement.

### Earlier inbound/notification retirement

**Historical — PR #183 (recorded 2026-09-12).** This evidence also predates final
approval retirement.
The inbound/notification retirement was exercised on a dedicated kind cluster
with Orka `v0.1.3`, Ollama `qwen2.5:3b` and an owner-managed Python 3.12
standard-library HTTP client fixture. This is a controlled model-route smoke
check, not a claim about every SDK or an already-operating source provider.

- The original-main migration left the Deployment unpatched. Applying its
  generated patch as the owner enabled a real Responses request through the TLS
  seam to Orka: `BRIDGE WORKS`, with 39 input and 5 output tokens in the ledger.
- After upgrading the same plane/database to the reduced runtime, repeating
  migration preserved Deployment UID, generation and specification, the bound
  application credential, original ConfigMap and application source. A new
  request returned the same answer and another 44-token model ledger row;
  the pre-upgrade row survived.
- A fresh Deployment migrated against the reduced runtime received a new
  credential, remained unpatched until owner application, then produced a real
  answer and its own 44-token ledger row.

Both proxy replicas became Ready. The old inbound Service remained after apply,
as expected, and was explicitly deleted in the disposable cluster. This smoke
check does not certify network-policy enforcement, arbitrary framework
continuations, tool governance or AKS. See the
[retirement upgrade steps](operations.md#upgrading-after-inbound-retirement) and
retain the translation/ownership limits above.

## Teardown

For an owner-managed workload, restore its release configuration deliberately;
there is no `kmx migrate down` command. Do not delete shared plane or Orka
resources as though they belonged only to this application.

For disposable kind clusters, `kmx down` deletes everything, including the ledger.
For AKS, follow [teardown](aks.md#teardown): confirm the resource group only for
created-cluster deletion, or the cluster for BYO monitoring cleanup. Cloud
absence checks cover named/recorded resources, not all subscription billing.
