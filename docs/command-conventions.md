# kmx command conventions

These rules describe how `kmx` commands, flags, output and interactive controls
should be named and shaped. Apply them to new commands and to renames.

- [kmx reference](kmx.md) documents the commands that exist today.
- [CLI presentation contracts](cli-ux-plan.md) records terminal and automation
  output details.
- [Current exceptions](#8-current-exceptions) lists commands that predate
  these rules.

## 1. Grammar

```text
kmx <verb> [object] [flags]
kmx <noun> <verb> [object] [flags]
```

- **Everyday workflows use root verbs:** `create`, `chat`, `run` and workspace
  `push` and full-workspace `eval`. Root `eval` also retains helpers (`diff`,
  `export`, `verify`, `from-sessions`, `cleanup`); root digest `lift` deploys an evaluated package. Standalone packaged evaluation remains unimplemented.
- **Resource and domain operations use noun first, then verb:** `task result`,
  `suite build/diff/push/pull/validate` and `orka install/status/show/list`. `kmx agent` is removed entirely,
  with no alias or retirement stub.
- **Singular nouns.** Use `orka list`, not `orkas`. A plural may exist only
  as an explicitly approved hidden alias.
- **Whole-tool entry points** include `quickstart`, `status`, `console`, `version`
  and `completion`. Root `status --to NAME` is GET-only destination inventory with
  table/JSON output; `orka status` retains the table-only runtime report. Neither
  restores legacy bundle status or implies evaluation.
  New root verbs need an explicit workflow decision, not a synonym.
- **At most two nouns deep.** `kmx <noun> <noun> <verb>` is the limit.
- **A group without a verb prints help.** A noun on its own never acts.
- **Lowercase, hyphen-separated words.** The canonical name is never an
  abbreviation; a short form may be an alias.

## 2. Verb vocabulary

Use one verb per meaning. Do not add a synonym for an existing verb.

| Verb | Meaning | Example |
|---|---|---|
| `list` | Read many resources, one summary row each. | `kmx orka list` |
| `show` | Read one resource and what it depends on, from live state. | `kmx orka show <name>` |
| `status` | Report readiness, health or drift. | `kmx orka status` |
| `create` | Author one tool-free workspace member offline; terminal prompts complete missing model/file inputs, scripts require both flags. No runtime/model bootstrap. | `kmx create <name>` |
| `install` | Install runtime software onto a cluster. | `kmx orka install` |
| `up` / `down` | Bring an environment up, or tear it down including its data. | `kmx aks up` |
| `run` | Execute once on a live Orka Agent, including a suite-owned Agent; not workspace-local execution or suite evaluation. | `kmx run --agent NAME` |
| `chat` | Open an interactive session. | `kmx chat <name>` |
| `eval` | Place and evaluate the full packaged native workspace inventory. | Orka: `kmx eval --to-context <ctx> --inference provider:NAME --out <private.json>`; kagent: replace `--inference` with `--bindings FILE` |
| `export` | Write new portable evaluation evidence from completed exact-suite proof, without evaluation. | `kmx eval export --member greeter --out ../evidence.json` |
| `verify` | Check public evidence against a local suite and independent accepted subject/policy, offline. | `kmx eval verify --suite ./layout --evidence evidence.json --expect expected.json` |
| `cleanup` | Inspect candidate runs; delete one explicitly selected run after confirmation. | `kmx eval cleanup [run-id] --to dev` |
| `validate` | Check an artifact offline, without a cluster. | `kmx suite validate <path>` |
| `policy` | Resolve portable suite restrictions offline without granting authority or selecting an enforcement provider. | `kmx suite policy POLICY.json [--administrative-policy ADMIN.json]` |
| `lift` | Lift an evaluated suite; no-reference workspace mode composes missing Orka eval/push/Lift. | `kmx lift --to NAME`; deploy-only: `kmx lift <ref>@<digest> --to-context <ctx> --evidence FILE` plus Orka `--inference` or kagent `--bindings` |
| `result` | Retrieve the outcome of an existing execution. | `kmx task result <task>` |

Keep these distinctions:

- **Lift, not deploy.** The local-to-remote story is Create, Prove, Lift.
  "Deployment" may describe the resources lift produces, but it is not a
  command verb.
- **Workload management is not `down`.** Destination status is read-only inspection;
  retirement remains unavailable. Environment teardown grants no workload-specific replacement contract.
- **`result` is not retry.** Reading a result never resubmits work.
- **`validate` is offline.** Root Lift `--plan` performs GET-only target checks, not admission.
- **`show` is the only verb for detail.** Do not add `get`, `describe` or
  `info`.

## 3. Arguments and flags

- **Positional arguments name the primary object only**, such as an agent name
  or an immutable suite reference. Use flags for everything else.
- **Long flags are kebab-case.** Short flags are reserved for `-o`
  (`--output`) and `-i` (`--interactive`). Add another only for a flag that
  is shared across commands with one meaning.
- **Reuse shared flag names with their existing meaning:**

| Flag | Meaning |
|---|---|
| `--context` | Kubernetes context for this invocation. Global, accepted anywhere on the line. |
| `--namespace` | Kubernetes namespace the command reads or writes. |
| `--to` | Named destination from workspace `kmx.yaml` for eval, Lift or destination status. Never switches a saved/current context. |
| `-o`, `--output` | Report format: `table` or `text`, `json`, `yaml`. Machine formats print exactly one document. |
| `-i`, `--interactive` | Switch a command that has a scripted mode to its guided terminal mode. Not used on commands that are only interactive. |
| `--out` | Path for written artifacts. Never reuse `--output` for a path. |
| `--workspace` | Exact workspace root without discovery fallback; default create initializes it if absent, and push requires an existing workspace. |
| `--evaluation` | Explicit standalone passing receipt for root push; overrides managed lookup without fallback. |
| `--unevaluated` | Explicit root push opt-out from passing evidence; never evaluates or deploys. |
| `--runtime-profile` | Exact public runtime profile ID for offline v3 workspace push or suite build planning, never an execution override. Profiled push requires --unevaluated; AgentKit refuses profiled builds. |
| `--platform` | Exact OS/ARCH for offline profile/build selection; v3 push may infer it only for one candidate in the explicitly selected profile. Legacy workspace push rejects it. |
| `--plan` | Read the target and print what would change. No writes. |
| `--no-apply` | Where supported by runtime setup, render without cluster writes; removed from workspace create. |
| `--dry-run` | Where supported by runtime setup, check server admission without persistent writes; removed from workspace create. |
| `--step` | Run one named phase of a multi-phase workflow. |
| `--wait` | How long to wait, as a duration. |
| `--prompt`, `--prompt-file` | Input text for an agent. `--prompt-file -` reads stdin. |

- **Precedence:** a flag overrides its environment variable, which overrides a
  saved selection, which overrides the default.
- **Declare flag relationships with Cobra helpers** (mutually exclusive,
  required together). Refuse invalid combinations before loading
  configuration or contacting a cluster. A flag that belongs to another mode
  of the same command is refused and named, never silently ignored.
- **Boolean flags default to false** and use positive names, such as
  `--follow`.
- **Never accept a secret value as a flag, and never print one.** Refer to
  Kubernetes Secrets by name.

## 4. Safety

- **Name the target before cluster writes.** Every cluster mutation prints the
  context, how it was chosen, the server and the namespaces on stderr, then runs the guard.
  A local kind cluster proceeds. A remote context needs typed confirmation or
  `KAIMAHI_CONFIRM=<context>`. A non-interactive run without consent refuses.
- **Offline create resolves the workspace before writing.** Without a workspace,
  terminal use asks `Start one here? [Y/n]`; nonterminal or redirected use
  refuses with a `--workspace` hint. Invalid nearest markers and second members
  refuse without changing source. Exact model and file-valued instructions are
  required; no offline wizard or implicit live dispatch is provided.
- **Candidate cleanup always confirms**, including local kind: display the selected
  run and frozen UID plan before context-naming consent. Without a run it only lists.
- **Read-only commands never prompt.**
- **Help and completion never mutate.** Completion lookups are bounded and
  read-only.
- **Unknown is not absent.** Report state that could not be read as unknown.
- **Destructive commands name what they delete** and ask for that name when
  ownership is unclear.

## 5. Output and exit codes

- **stdout carries the result only:** a report, an answer, a manifest or a
  JSON document. Progress, diagnostics, notices and deprecation warnings go to
  stderr.
- **Machine output is never styled.** Rich output respects `TERM=dumb` and
  `NO_COLOR`.
- **Exit codes:**

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | Failure. |
| `2` | Not finished yet: a pending Task/wait, or known pending deployed placement in a complete destination-status observation. Unknown/unhealthy/drift takes precedence with1. |

  A new exit code needs its own documentation in [kmx.md](kmx.md).
- **Errors say what failed and what to run next.** Generated commands keep
  the caller's executable, context, selectors and shell quoting, so a checkout
  binary does not redirect recovery to an older `kmx` on `PATH`.

## 6. Interactive surfaces

- **A TUI requires a terminal.** Without one, refuse and name the
  non-interactive command, or provide one.
- **Slash commands use the CLI vocabulary.** Prefer `/<noun> <verb>` where the
  scope matches a CLI command. Retired `/lift` and console Lift only print
  `Lift moved: kmx lift <ref>@<digest> --to-context … --evidence …`;
  do not open a new release GUI or copy source-less live Agents.
- **Modes are arguments, not separate commands.** On the CLI, use
  `kmx quickstart --interactive`, not a separate `quickstart-wizard`. In
  slash commands, prefer `/verbose on|off` over separate on and off commands.
- **A command with only one mode takes no mode flag.** `kmx chat` is
  always a session, so it has no `--interactive`.
- **Common keys mean the same thing everywhere:** `?` help, `esc` back or
  cancel, `ctrl+c` quit, `enter` select, `j`/`k` or arrows to move.
- **Every interactive action has a CLI equivalent,** or the docs say why not.

## 7. Changing a command

1. **Add the canonical name first.** Reuse the existing operation handler.
2. **Keep the old name callable.** Hide it from help and completion, print a
   deprecation notice on stderr, and keep its arguments, stdout and exit codes
   unchanged.
3. **Remove the old name only in an announced breaking minor release,** listed
   under **Breaking** in the [CHANGELOG](../CHANGELOG.md), following
   [releases](releases.md#versions).
4. **After removal,** a hidden stub may remain that refuses and names the
   replacement.

While KMX has no external users, a rename may retire the old name in the
same change instead of keeping it callable: remove the alias outright, leave
an explicitly chosen hidden stub that refuses before loading configuration and
names the replacement, or remove a flag that no longer does anything. Record
the removal and replacements under **Breaking** and **Upgrading** in the CHANGELOG.
Root `up`, `down`, the AKS `lift` alias and `lift down` are removed
outright, without stubs; use `local up/down` and `aks up/down`. The old root `ctx`
context-selection alias is also removed; use `context show/use` for access
selection. New `ctx [--check]` has list-only named-destination semantics: no
positional name, current/default marker, switching or persisted selection.
`targets` remains the runtime-capability view, not this destination list.
Root `lift <ref>@<digest>` now means deploy-only suite lift, not infrastructure setup.
User `agent.yaml` readers/writers/receipts, `bundle` commands, `create --live`
including previews/wizard, run bundle mode and `--bundles` flags are removed
outright. Old receipts are unreadable; no migration or file/resource deletion.
The entire `agent` group is also removed outright, without an alias or stub;
see the [implemented mapping](command-semantics.md#implemented-workflow-command-mapping-unreleased).

Update these in the same change:

- command registration, help text and completion
- chat and console labels that name the command
- generated next-step and recovery commands
- [kmx.md](kmx.md) and the guides that use the command
- command-tree and compatibility tests
- CHANGELOG

## 8. Current exceptions

These commands differ from the rules above. Follow section 7 before changing
any current syntax. Proposed names are not available until they ship; remove a
row when its rename lands.

| Current command | Rule it breaks | Proposed |
|---|---|---|
| `ctx [--check]` | Abbreviated root name and a list action without a verb. | Explicitly approved list-only named destinations; not the removed context-switching alias. Keep `--to` explicit and `targets` separate. |
| `suite push <directory> <reference>` | Two positional arguments: extracted source directory and OCI destination reference. | Retain extracted-artifact transfer syntax without workspace discovery or `--workspace`; root `push [--workspace <directory>] [reference]` discovers a workspace and publishes a prepared native suite with at most one positional destination. An explicit tag is preserved; bare push derives a full-S tag from saved publishRepository. Completed private exact-S proof is required unless --unevaluated explicitly opts out. `--workspace` selects an exact root without fallback. |
| `suite build --output <file>`, `suite pull --output <directory>` | `--output` is an artifact path, not a report format; neither command has the report-format `-o` shorthand. | Retain current syntax until a replacement is agreed. |
| `suite build --attestations` | Defaults to true; `--attestations=false` opts out of SBOM and provenance requests. | Keep the documented default; `--require-attestations` refuses missing or unsupported attestations. |
