# Orka CRD schema fixtures

These are byte-exact upstream Orka CRDs, embedded for network-free validation.
They are not a second handwritten schema. `Offline("")` selects `v0.2.0`;
`Offline("v0.1.3")` retains the previous release explicitly, and
`Offline("main")` selects the immutable snapshot below, not the moving branch.
`Installed` accepts the same three CRDs as bytes under `Agent`, `Provider`, and
`Task` keys and uses exactly the same schema compilation/validation path.

## Sources and attribution

Upstream: <https://github.com/orka-agents/orka>.

- `v0.1.3`: dereferenced release commit
  `b07d42c0b9e52fe511b434827a342b4720f5d422`.
- `v0.2.0`: dereferenced release commit
  `5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1`.
- `main`: snapshot commit
  `7c4753c2c68a510112ea2bb25b60a406d9c45686`.

Each source URL follows this immutable template:

```text
https://raw.githubusercontent.com/orka-agents/orka/<commit>/config/crd/bases/core.orka.ai_<plural>.yaml
```

| Fixture | Source URL | SHA-256 |
| --- | --- | --- |
| `fixtures/v0.1.3/agents.yaml` | [release Agent](https://raw.githubusercontent.com/orka-agents/orka/b07d42c0b9e52fe511b434827a342b4720f5d422/config/crd/bases/core.orka.ai_agents.yaml) | `d6b9123ea29d904846777b63c59e8f5c054ac851e63d6c1bf8177a4accc44f4d` |
| `fixtures/v0.1.3/providers.yaml` | [release Provider](https://raw.githubusercontent.com/orka-agents/orka/b07d42c0b9e52fe511b434827a342b4720f5d422/config/crd/bases/core.orka.ai_providers.yaml) | `2c9b4b25800a8d6a57494fc9e267d7ebd7b3cd52c9a2956ba87544a8c3388ff6` |
| `fixtures/v0.1.3/tasks.yaml` | [release Task](https://raw.githubusercontent.com/orka-agents/orka/b07d42c0b9e52fe511b434827a342b4720f5d422/config/crd/bases/core.orka.ai_tasks.yaml) | `8672cf42f1b2dc17020df1fc539ebdfa5ac67604ecfb8a272cd6eac2a51c1d6d` |
| `fixtures/v0.2.0/agents.yaml` | [release Agent](https://raw.githubusercontent.com/orka-agents/orka/5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1/config/crd/bases/core.orka.ai_agents.yaml) | `01005e7fc327b2b5b7d8dc32ffa27d80d3c1ccfe9a2a36bab6dda84dafcff912` |
| `fixtures/v0.2.0/providers.yaml` | [release Provider](https://raw.githubusercontent.com/orka-agents/orka/5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1/config/crd/bases/core.orka.ai_providers.yaml) | `6185d760bd43d00a4594cfa8fd50ff86b269f952be6656884d000f67e64562f8` |
| `fixtures/v0.2.0/tasks.yaml` | [release Task](https://raw.githubusercontent.com/orka-agents/orka/5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1/config/crd/bases/core.orka.ai_tasks.yaml) | `045a954158061183150067a1615bf037df337eaaa1f6b3c4e408b25c54a3e39c` |
| `fixtures/main/agents.yaml` | [snapshot Agent](https://raw.githubusercontent.com/orka-agents/orka/7c4753c2c68a510112ea2bb25b60a406d9c45686/config/crd/bases/core.orka.ai_agents.yaml) | `9e7bc6252cdf45cdc9fc127a558b3b2f77eb4ff1a1388386996acbf1d58da43c` |
| `fixtures/main/providers.yaml` | [snapshot Provider](https://raw.githubusercontent.com/orka-agents/orka/7c4753c2c68a510112ea2bb25b60a406d9c45686/config/crd/bases/core.orka.ai_providers.yaml) | `6185d760bd43d00a4594cfa8fd50ff86b269f952be6656884d000f67e64562f8` |
| `fixtures/main/tasks.yaml` | [snapshot Task](https://raw.githubusercontent.com/orka-agents/orka/7c4753c2c68a510112ea2bb25b60a406d9c45686/config/crd/bases/core.orka.ai_tasks.yaml) | `e0c657f3a9c0878665e36ae18a3cfe62ecb563efb8b5141576b57024e8b52279` |

The fixtures are distributed under the upstream **MIT License**,
**Copyright (c) Microsoft Corporation.** The adjacent [`LICENSE`](LICENSE)
is an unmodified copy of upstream
[`LICENSE` at the release commit](https://raw.githubusercontent.com/orka-agents/orka/b07d42c0b9e52fe511b434827a342b4720f5d422/LICENSE).
The [v0.2.0 license](https://raw.githubusercontent.com/orka-agents/orka/5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1/LICENSE)
and [snapshot license](https://raw.githubusercontent.com/orka-agents/orka/7c4753c2c68a510112ea2bb25b60a406d9c45686/LICENSE)
are byte-identical. All have SHA-256
`7df20dcdf9197e9945c14858d41c60f11b52b93e5b69e2b63416b874d598d322`.

## Validation contract and limits

- Select the served `core.orka.ai/v1alpha1` version, checking CRD identity and
  namespaced scope. Missing, unserved, malformed or incompatible schemas fail;
  installed schemas never fall back to fixtures.
- Compile the **whole** `openAPIV3Schema` with
  `github.com/santhosh-tekuri/jsonschema/v6` **v6.0.2**, using draft 7 and format
  assertions. External schema resolution is disabled, including file URLs.
  Compilation uses `Compiler.AddResource`; there are no runtime downloads.
- Normalize OpenAPI `nullable` types and intersect `int32`/`int64` formats with
  exact numeric bounds. JSON number decoding preserves 64-bit integer precision.
- Refuse unknown fields wherever a schema explicitly declares properties and
  does not permit additional properties or preserved unknown fields. Open
  metadata and explicitly permitted arbitrary maps remain open; typed map
  values and known properties are still checked. Normalization changes only
  the in-memory schema, never the fixture bytes.
- Upstream integer-or-string schemas already express both branches with
  `anyOf`; the library validates those branches. Kubernetes list/map merge
  extensions are not runtime or reconciliation guarantees.
- CEL, defaulting, admission, Secret provisioning, controller readiness,
  authorization and runtime rate-limit enforcement are **not evaluated**.
  Callers must separately validate the stronger Provider/Agent bundle invariant,
  validate the metadata-only Secret skeleton, and run strict server admission
  preflight before any live custom-resource writes.
- The v0.1.3 release has Agent and Provider `spec.rateLimit`; v0.2.0 and
  the main snapshot lack both. Refusal diagnostics name the resource and exact
  field path rather than dropping requested fields or suggesting they will be
  enforced.

To refresh intentionally, choose immutable commits, download each source into
its fixture path without reformatting, verify license attribution, and update
this table and the byte-integrity tests together. Do not replace the pins with
branch URLs. Run `go test ./internal/kmx/orkaschema ./internal/kmx/scaffold`.
