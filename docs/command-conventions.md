# kmx command conventions

These rules describe how `kmx` commands, flags, output and interactive controls
should be named and shaped. Apply them to new commands and to renames.

- [kmx reference](kmx.md) documents the commands that exist today.
- [CLI presentation contracts](cli-ux-plan.md) records terminal and automation
  output details.
- [Current exceptions](#8-current-exceptions) lists commands that predate
  these rules.

## Help and discovery

Root help lists `create`, `run`, `test`, `chat`, `model`, `package`, `cluster`,
then `version`, under “Write, run and test agent files.” `mcp`, `completion` and
`help` remain callable but are not root help entries. Keep root ordering local;
other command groups retain their normal discovery order.

Inner-loop help uses a one-line summary, at most five logical description
lines, two or three truthful examples, and a flag block fitting 24 lines at
80 columns. Run separates file-preview, shared-input and live-Orka flags;
`--prompt` and `--prompt-file` are shared input options. Kubernetes selectors
are not advertised for file-only operations; global parsing is unchanged.
File help links once to [preview storage and privacy](local-preview.md#cli-storage-privacy-and-evidence-caveats)
rather than repeating journal, failure-retention and evidence limitations.
Package/native help keeps its own safety disclosures. Canonical flag-parse errors
point to the safely quoted executable and resolved topic's `--help`; native
Windows hints are explicitly labeled PowerShell and use its call operator.
Legacy Eval/Lift refusals, argument/runtime errors, stdout and exit codes stay unchanged.

## 1. Grammar

```text
kmx <verb> [object] [flags]
kmx <noun> <verb> [object] [flags]
```

- **Everyday workflows use root verbs:** native workspace `create`, file `run`
  and `test`, live-Orka `chat`, and model access. Workspace evaluation, publication
  and deployment use `package test/push/deploy`. Package Test retains `diff`,
  `explain`, `export`, `verify`, `from-sessions` and `cleanup`; root file Test
  retains file-only Explain. Standalone packaged evaluation remains unimplemented.
- **Resource and domain operations use noun first, then verb:** `cluster task result`,
  `package build/push/push-image/pull/validate`, `package run-profile export` and
  `cluster orka install/status/show/list`. `kmx agent` is removed entirely,
  with no alias or retirement stub.
- **Singular nouns.** Use `orka list`, not `orkas`. A plural may exist only
  as an explicitly approved hidden alias.
- **Cluster entry points** include `cluster quickstart/status/console`.
  `version` and hidden `completion` remain whole-tool entry points. `package status --to NAME` is GET-only destination inventory with
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
| `list` | Read many resources, one summary row each. | `kmx cluster orka list` |
| `show` | Read one resource and what it depends on, from live state. | `kmx cluster orka show <name>` |
| `status` | Report readiness, health or drift. | `kmx cluster orka status` |
| `create` | Author one tool-free workspace member offline; terminal prompts complete missing model/file inputs, scripts require both flags. No runtime/model bootstrap. | `kmx create <name>` |
| `install` | Install runtime software onto a cluster. | `kmx cluster orka install` |
| `up` / `down` | Bring an environment up, or tear it down including its data. | `kmx cluster aks up` |
| `run` | Execute once on a live Orka Agent, including a suite-owned Agent; not workspace-local execution or suite evaluation. | `kmx run --agent NAME` |
| `chat` | Open an interactive session. | `kmx chat <name>` |
| `test` | Place and evaluate the full packaged native workspace inventory. | Orka: `kmx package test --to-context <ctx> --inference provider:NAME --out <private.json>`; kagent: replace `--inference` with `--bindings FILE` |
| `export` | Write a portable offline artifact without execution: evaluation evidence from completed exact-suite proof, or a frozen source-file run profile. | `kmx package test export --member greeter --out ../evidence.json`; `kmx package run-profile export FILE` |
| `explain` | Inspect private local evaluation failures offline; content opt-in. `--clear` deletes one ID, or all only with explicit workspace or agent-file selection. | `kmx test explain [failure-id] [--workspace DIR\|--file FILE] [--show-content\|--clear]` |
| `verify` | Check public evidence against a local suite and independent accepted subject/policy, offline. | `kmx package test verify --suite ./layout --evidence evidence.json --expect expected.json` |
| `mcp` | Serve five original-server agent/settings tools over stdio, optionally root-bound; preview children retain four; no network listener | `kmx mcp --root DIRECTORY` |
| `cleanup` | Inspect candidate runs; delete one explicitly selected run after confirmation. | `kmx package test cleanup [run-id] --to dev` |
| `validate` | Check an artifact offline, without a cluster. | `kmx package validate <path>` |
| `policy` | Resolve portable suite restrictions offline without granting authority or selecting an enforcement provider. | `kmx package policy POLICY.json [--administrative-policy ADMIN.json]` |
| `deploy` | Deploy an evaluated suite; no-reference workspace mode composes missing Orka eval/push/Deploy. | `kmx package deploy --to NAME`; deploy-only: `kmx package deploy <ref>@<digest> --to-context <ctx> --evidence FILE` plus Orka `--inference` or kagent `--bindings` |
| `result` | Retrieve the outcome of an existing execution. | `kmx cluster task result <task>` |

Keep these distinctions:

- **Run / Test / Publish** is the developer vocabulary. Run is existing live
  Orka invocation, Test is full-workspace `test`, and Publish is registry `push`.
  Deploy remains real-target placement. `test` and `deploy` are unreleased;
  v0.8.1 uses `eval` and `lift` with the same arguments. Keep both hidden
  deprecated routes for at least one released version, preserving stdout,
  arguments, flags, exit codes and state, with one stderr notice per legacy
  invocation. See [Test compatibility](kmx.md#test-compatibility-unreleased) and
  [Deploy compatibility](kmx.md#deploy-compatibility-unreleased). Data directory
  names, fields, schemas, APIs and machine JSON are not renamed.
- **Workload management is not `down`.** Destination status is read-only inspection;
  retirement remains unavailable. Environment teardown grants no workload-specific replacement contract.
- **`result` is not retry.** Reading a result never resubmits work.
- **`validate` is offline.** Root Deploy `--plan` performs GET-only target checks, not admission.
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
| `--to` | Named destination from workspace `kmx.yaml` for eval, Deploy or destination status. Never switches a saved/current context. |
| `-o`, `--output` | Report format: `table` or `text`, `json`, `yaml`. Machine formats print exactly one document. |
| `-i`, `--interactive` | Switch a command that has a scripted mode to its guided terminal mode. Not used on commands that are only interactive. |
| `--out` | Path for written artifacts. Never reuse `--output` for a path. |
| `--workspace` | Exact workspace root without discovery fallback; default create initializes it if absent, and push requires an existing workspace. |
| `--evaluation` | Explicit standalone passing receipt for root push; overrides managed lookup without fallback. |
| `--unevaluated` | Explicit root push opt-out from passing evidence; never evaluates or deploys. |
| `--runtime-profile` | Exact public runtime profile ID for offline v3 workspace push or suite build planning, never an execution override. Profiled push requires --unevaluated; AgentKit refuses profiled builds. |
| `--platform` | Exact OS/ARCH for offline profile/build selection; v3 push may infer it only for one candidate in the explicitly selected profile. Legacy workspace push rejects it. |
| `--plan` | Read the target and print what would change. No writes. |
| `--show-content` | Opt into sanitized bounded failure excerpts and expected values: stderr for test/executing workspace Deploy, stdout for offline test explain. Workspace records may include retained rationale; file records do not retain judge rationale. Not evidence; never implied by --verbose; conflicts with explain --clear. |
| `--verbose` | Where supported, add content-free diagnostics and timings on stderr. File Run/Test includes request progress, endpoints, HTTP status, timings and safe failures, without repeating the final report; never implies --show-content. |
| `--github-summary` | File Test only: with `-o json`, append the same result's bounded content-free Markdown projection to `$GITHUB_STEP_SUMMARY`. Never changes the Test exit code or grants evidence. |
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
| `2` | File Run/Test: could not run or decide (missing sign-in/tests, execution failure or inconclusive judge); a completed failed check exits 1. Live Task/wait: pending. Destination status: known pending placement in a complete observation; unknown/unhealthy/drift takes precedence with 1. |

  A new exit code needs its own documentation in [kmx.md](kmx.md).
- **Errors say what failed and what to run next.** Generated commands keep
  the caller's executable, context, selectors and shell quoting, so a checkout
  binary does not redirect recovery to an older `kmx` on `PATH`.

## 6. Interactive surfaces

- **A TUI requires a terminal.** Without one, refuse and name the
  non-interactive command, or provide one.
- **Slash commands use the CLI vocabulary.** Prefer `/<noun> <verb>` where the
  scope matches a CLI command. Retired `/lift` and console Deploy only print
  `Deploy moved: kmx package deploy <ref>@<digest> --to-context … --evidence …`;
  do not open a new release GUI or copy source-less live Agents.
- **Modes are arguments, not separate commands.** On the CLI, use
  `kmx cluster quickstart --interactive`, not a separate `quickstart-wizard`. In
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

The `lift` → `deploy` transition must keep `lift` hidden but callable for at
least one released version. Each legacy invocation adds exactly one stderr line:
`kmx lift is deprecated; use kmx package deploy (same arguments).` Keep legacy stdout,
stderr stage labels, arguments, flags and exit codes unchanged. Canonical human
help/output says Deploy; machine JSON keys/values (including `targets` operation
`"lift"`), public Go/API/internal identifiers, evidence, storage and annotations
remain unchanged. Help/completion stays inert. Preserve old guide fragments and
historical release/design records. This transition requires a later announced
breaking release for removal; the optional immediate-removal policy below does
not apply. See [Deploy compatibility](kmx.md#deploy-compatibility-unreleased).

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
Root `deploy <ref>@<digest>` is packaged-suite deployment, not infrastructure setup.
The deprecated packaged-suite `lift` route is distinct from those removed AKS aliases.
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
- a `changes/` release-note fragment

## 8. Current exceptions

These commands differ from the rules above. Follow section 7 before changing
any current syntax. Proposed names are not available until they ship; remove a
row when its rename lands.

| Current command | Rule it breaks | Proposed |
|---|---|---|
| `ctx [--check]` | Abbreviated root name and a list action without a verb. | Explicitly approved list-only named destinations; not the removed context-switching alias. Keep `--to` explicit and `targets` separate. |
| `package suite push <directory> <reference>` | Two positional arguments: extracted source directory and OCI destination reference. | Retain extracted-artifact transfer syntax without workspace discovery or `--workspace`; root `push [--workspace <directory>] [reference]` discovers a workspace and publishes a prepared native suite with at most one positional destination. An explicit tag is preserved; bare push derives a full-S tag from saved publishRepository. Completed private exact-S proof is required unless --unevaluated explicitly opts out. `--workspace` selects an exact root without fallback. |
| `package build --output <file>`, `package pull --output <directory>` | `--output` is an artifact path, not a report format; neither command has the report-format `-o` shorthand. | Retain current syntax until a replacement is agreed. |
| `package build --attestations` | Defaults to true; `--attestations=false` opts out of SBOM and provenance requests. | Keep the documented default; `--require-attestations` refuses missing or unsupported attestations. |
| `package push-image <archive> <reference>` | Two positional arguments: built OCI archive and registry destination. | Explicit experimental exception matching extracted `package suite push`; keeps image transport separate from suite publication and deployment admission. `--force` permits changing an existing tag; platform is `--platform OS/ARCH`. |
