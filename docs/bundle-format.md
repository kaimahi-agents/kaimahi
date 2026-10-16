# AgentSuite format boundaries

The removed user PortableAgent operational bundle is **not** an AgentSuite
implementation bundle. There is no global meaning of “bundle” that permits
renaming or converting these formats into one another.

## Which format goes where

| Format | Current consumer / boundary |
|---|---|
| Editable native workspace | Root `create/eval/push`: `workspace.yaml`, member metadata/instructions, authored cases and pinned build profiles. [Workspace guide](suite-workspace.md). |
| Public AgentSuite content / OCI graph | External `github.com/agent-primitives/suite` validation, schemas, identities and semantic diff; CLI `suite validate/diff/build/push/pull`. [AgentSuite specification](https://github.com/agent-primitives/suite/blob/main/spec.md). |
| Public AgentSuite / ToolProvider implementation bundle | Typed portable graph content and locked references, not old user `agent.yaml` lifecycle state. Public format validation does not imply native execution-profile support. |
| Prepared native suite | Exact content-addressed OCI manifest plus KMX-private packaged runtime/profile/model/case metadata. Root digest Lift consumes it and separate private full-passing evidence, not workspace source. |
| Private native passing evidence | Full-workspace Orka or exact-v0.10.3 Go kagent proof. Evaluated push, same-runtime offline `eval diff` and digest Lift require exact coherence. Evidence is unsigned observation, not authentication. |
| Removed user PortableAgent operational bundle | Old `agent.yaml`, target bindings, lift/evaluation/replay receipts and policies no longer have persistent readers/writers or CLI consumers. Old Orka/kagent/AgentSessions receipts are unreadable. |

## Preserved public formats

AgentSuite and ToolProvider schemas, implementation-bundle graphs, image-backed
build/pull, extracted-directory validation and `suite push DIRECTORY REFERENCE`
remain supported within their existing limits. Native zero-member-image profiles
are separate from the experimental image-backed build workflow. No runtime
selection, model override or format conversion follows from public graph validity.

PortableAgent typed scaffolding used internally for native preparation is not a
persistent reader for old authored `agent.yaml` files. Public alpha `pkg/kmx`
AgentEnvironment, AgentSuites and AgentDeployments contracts remain unwired and
are not removed by CLI bundle retirement.

## Removed operational format

User bundle commands, readers/writers/receipts, `create --live` including previews
and wizard, root run bundle-directory mode and `--bundles` flags are removed
outright, with no stubs, migration or conversion. Saved receipts and resources on
disk/cluster are not deleted. Legacy RecordedExecution, Sessions replay/evaluation
and judge consumers are removed; `eval from-sessions` remains independent private
reference extraction/export with no native manifest consumer. Native eval requires
authored cases and private full-passing evidence.

Suite status/retirement remain unavailable, with no replacement claimed. See the
[CLI reference](kmx.md), [runtime matrix](runtime-adapters.md) and
[breaking/upgrading notes](../CHANGELOG.md#breaking). Historical release notes
refer to the format and readers available at their own pinned revision.
