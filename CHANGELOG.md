# Changelog

Kaimahi is **pre-1.0 and incubating**. Versions are semantic with the pre-1.0
reading of that word:

- **Patch** (`v0.1.0` → `v0.1.1`) — fixes only. No schema change, no flag
  removed, no behaviour an operator was relying on.
- **Minor** (`v0.1.0` → `v0.2.0`) — anything else, including a breaking one.
  Below 1.0 the minor number is where breaking changes live, so every entry
  that breaks something says so under **Breaking** and says what to do about
  it.
- **Pre-release** (`v0.2.0-rc.1`) — a candidate for the version it names. Go's
  `@latest` ignores these, so a release candidate cannot become anybody's
  default install by accident.

There is no 1.0 promise yet and no support window. What there *is*: the
release job refuses to publish a tag that has no section here, so a version
without notes cannot exist.

Each entry names what changed, and — where it matters — what an operator has
to do. Sections: **Added**, **Changed**, **Fixed**, **Breaking**, **Upgrading**.

## Unreleased

## v0.9.0 — 2026-10-23

### Added

- `kmx mcp --root DIRECTORY` serves exactly Create/Edit/Run/Test over stdio.
  It confines canonical charter access to an explicit root, creates without
  replacement, edits at an expected revision and delegates Run/Test to the shared
  snapshot API. Fake/local previews use embedded Sessions and retain cleartext
  journals; invocations with a Copilot agent or judge remain direct and explicitly
  unjournaled. No extra MCP network listener or native evidence authority.

- Direct, explicitly unjournaled file-preview execution when the agent or judge
  uses Copilot, with one completion per input/example and one judge sample per
  llmJudge, without retries or fallback. Other fake/local invocations use embedded
  Sessions and two-sample judging. Neither route grants native judge evidence or
  deployment qualification.

- Canonical `run FILE` / `test FILE` adapters for markdown-agent local preview:
  stable `--case` selection, versioned payload-free text/JSON outcomes, separate
  ad-hoc input and distinct non-pass/cancel exits. The adapters share the runner
  interface, with journaled fake/local execution and the direct Copilot exception
  described above. No native evidence, file-scoped Explain or change to deployed
  Run/workspace Test/Eval.

- Read-only `kmx model list [-o table|json]` discovers cached Foundry Local
  models at the runtime-reported endpoint and installed Ollama models on loopback
  port 11434. Absent, empty, installed and failure states include explicit install
  guidance. Non-secret routes are independent of kind reuse; no runtime start,
  model download, automatic selection or local agent execution is added. Native
  package tests cover Windows/macOS/Linux; Windows CLI qualification follows
  native CLI support.

- Offline canonical single-file charter parser with exact Model/Preferred,
  separated inline Examples, existing assertion semantics, source locations,
  bounded regular-file loading and a greeter sample. Standalone files and
  matching `.squad/agents/<id>/charter.md` placements share one format. No
  execution command, model access, Squad registration or native suite-loader
  change is enabled; see [the format](docs/agent-file.md).

- Experimental offline multi-agent flow authoring with `kmx create NAME
  --workspace DIR --members FILE --flow FILE`, deterministic preparation and
  explicit native/public layout/archive inventory readers. Strict JSON imports
  preserve the critical flow contract and one artifact identity; default readers,
  execution and transfer/registry paths remain strict. No pipeline execution,
  approval enforcement, publication or whole-suite evaluation is enabled.

- `AgentSessionsBinding` (`kmx.kaimahi.dev/v1alpha1`) is a closed per-environment
  binding document for AgentSessions: target, substrate placement, atespace,
  sandbox, snapshot, required cpu/memory resources, worker selector and model
  name/provider/baseURL plus a typed credential reference: a Kubernetes Secret
  key (`secretRef`) or a host-owned source such as keyless workload identity
  (`hostRef: {type, name}`). It has strict parsing, a JCS digest and an
  exact check against the suite's pinned model; AgentSessions eval refuses a
  binding that serves another model.
  Resource quantities refuse a decimal exponent of more than two digits before
  parsing.
  `snapshot.storageLocation` refuses `\`, empty path segments and segments of
  only `.` and spaces; an empty `workerSelector.matchLabels` has the same digest
  as an omitted selector.
- Plan-only AgentSessions deploy: `kmx deploy REF@DIGEST --harness-image IMAGE@sha256:…
  --bindings FILE --evidence FILE --plan` renders a substrate ActorTemplate and
  HarnessRegistration offline from full-passing AgentSessions evidence, for
  agent-substrate/substrate main at `f4f7b806255ffdc824391b857dbbf329a2556402`.
  It reads no cluster configuration, calls no substrate or host and applies
  nothing; Apply and `--update` refuse.
  `scripts/validate-agentsessions-render.sh` checks the golden renders against
  that commit. The template name hash covers the renderer version, the full
  image reference (a mirrored image is another template) and the binding
  digest. Composed `kmx deploy --to NAME` refuses `--harness-image` and
  AgentSessions workspaces before evaluation or publication.
- Eval and composed workspace Lift show non-pass case/run, assertion IDs/types,
  both judge scores against the threshold and safe unknown-outcome classes.
  `--show-content` opts into sanitized bounded expected values, judge rationale
  and answer excerpts on stderr; outcome JSON remains payload-free. Diagnostics
  remain separate from evidence. Non-pass runs are retained privately under
  KMX_HOME: last 10 per workspace across revisions, 64 KiB answers/128 KiB records,
  owner-only and deterministically bounded. Offline `kmx test explain` lists or
  shows retained diagnostics with opt-in content; --clear removes one ID or all
  only with explicit --workspace, without touching passing proof or upstream data.
  Failure JSON adds diagnostic/provenance IDs, completion/attribution flags and
  retention status; receipt/evaluator fingerprints and portable exports stay
  unchanged. Interrupted staging writes remain inspectable through healthy
  history and recover with scoped clearing. Retention warnings include uncertain
  post-publication outcomes, not just absent records.
  Known credentials/headers are withheld before truncation, but
  shape checks cannot detect arbitrary secrets.
- Opt-in Orka `kmx lift --to <destination> --update` and digest/evidence update
  or rollback. Only newly created, acknowledged retained pairs qualify; v0.8.1
  and earlier, reused, mixed and unrecorded placements are never enrolled.
  Original passing evidence supports rollback without re-evaluation. Same-digest
  updates are no-write checks; workspace updates re-prove the actual cluster
  baseline instead of trusting a historical placement skip. Arrange a quiet
  window: there is no draining, zero-downtime or automatic recovery guarantee.
- Destination status reads v1 creates and v2 mutation attempts, with a bounded
  exact-name shared-record GET reporting pending/partial/completed/unknown
  separately from readiness. These unsigned observations never grant update
  authority or prove local completion acknowledgment.
- Public Go package `pkg/suiteview` returns a verified, read-only `MemberRecipe`
  for one explicit member: caller-owned instructions and the complete portable
  model requirement from a local
  AgentSuite OCI layout or bounded uncompressed layout archive. It retains exact
  artifact, graph, member and instruction identities, uses payload-free typed
  refusals including a distinct resource-limit category, and performs no configuration, registry, runtime, evaluation,
  publication or Lift operation. The recipe is intentionally not a complete
  runtime/build/tool/policy/evaluation plan. KMX native loaders call the public
  projection on their frozen snapshots before applying separate runtime-private
  checks.
- Internal offline detached Sessions association records bind exact selected suite,
  profile, build-profile, platform and chat config declarations without changing
  suite bytes. Image references are explicitly `declared-unverified`; no image
  verification, runtime qualification, executable plan, CLI consumer or Apply
  support is enabled.

- Internal experimental flow destination preflight and candidate projection: exact independent bindings, frozen observations, semantic/freshness separation and full-tuple ownership through cleanup/status. No CLI flow execution, evaluation, publication or Deploy is enabled.

- Explicit direct Copilot model access: `kmx model login` uses GitHub device
  flow; `login --gh` alone opts into bounded private gh token capture. Reusable
  credentials live only in kmx's native OS keychain item, never a plaintext
  cache; `model logout` removes only that item and clears memory. Explicit
  `model list --provider copilot [-o table|json]` returns enabled Chat-compatible
  non-secret routes without prompting for sign-in; default local discovery is
  unchanged. The independent `internal/kmx/modelaccess/copilot` package provides
  selected-route JSON/SSE Chat Completions, with memory-only bearer exchange and
  expiry. Auth is established through the catalog; legacy exchange follows only
  credential refusal. No inference replay, model substitution, implicit credential
  reuse, proxy or change to the existing Copilot CLI-chat adapter is added.
- The native credential backend supports CGO-free macOS releases, keeps secrets
  off subprocess arguments, and performs no keychain or session-bus I/O during
  help, completion or default local discovery. Explicit credential operations
  may display OS unlock UI; Linux requires an existing session bus. The dated
  access decision distinguishes this experimental compatibility route from
  official OAuth/SDK documentation and unqualified runner modes. Hermetic and
  native package tests do not replace live-account manual acceptance or full
  Windows CLI qualification.

- Copilot app/CLI plugin development package with Agent Plugins 1.0 manifests,
  a chat skill and an offline, explicitly root-bound packager for a pinned kmx.
  Native package tests exercise the real stdio Create/Edit/Run/Test loop.
  Signed distribution and installed Copilot chat qualification remain separate.

- Local preview library for one tool-free single-file agent. It runs in process
  on embedded Sessions with a private, bounded SQLite journal, against kmx's
  deterministic fake model or an explicit OpenAI-compatible endpoint, and
  reuses the existing assertions and `llmJudge`. The journal keeps
  instructions, inputs and answers in cleartext, never credentials: content
  containing the endpoint or judge credential is refused before the journal is
  created, and an answer echoing the endpoint credential is refused. Each new
  run prunes to the newest 20 runs, never deleting one still executing, and
  journals can be cleared per run or all at once. On Windows the journal is
  under `%AppData%` (roaming profile). Journal inspection replays a run with no
  model call and gives no verdict. Results are labeled local preview, not
  deployment qualification. File Run/Test and MCP use it for fake/local routes;
  if either the agent or judge uses Copilot, the whole invocation instead remains
  direct and explicitly unjournaled. See
  [local preview](https://github.com/kaimahi-agents/kaimahi/blob/main/docs/local-preview.md).

- Native CI qualification uploads separate payload-free phase timings on success or failure, without changing runtime proofs, cases or time budgets.

- Add an internal checked-read filesystem foundation with native Windows amd64
  tests for local-NTFS no-reparse opens, current-user protected DACL inspection
  and physical identity; Windows arm64 remains compile/vet only. The package is
  unwired, exposes no create/write/lock/sync/state/publication authority, and
  does not enable or qualify the guarded Windows CLI.

- Native Copilot plugin archives with a separate signed checksum inventory,
  exact engine/platform/source checks and explicit root setup before installation.
  An opt-in isolated host check covers install/discovery, cache bytes and four
  real MCP tools without changing normal Copilot settings. No Windows release,
  marketplace publication or OS trust qualification is implied.

- Add a conservative Orka path decision and unconditional hygiene checks for package initialization and typed caller drift across independent Go code. Runtime qualification frequency and existing shard coverage are unchanged.

- An explicit Squad 1.1.0 parser profile preserves native cast charters and source
  spans, recognizes valid task/model/examples additions and refuses ambiguous
  structures. Canonical parsing and current CLI/MCP routes are unchanged; team
  casting/enrichment, member execution and package-bound results are not enabled.

- Root-confined read-only MCP agent resource and `/agent-view` text fallback,
  plus a native Copilot agent canvas. Display minimal agent files and canonical
  Squad charters without adding a tool or changing Run/Test acceptance. Latest
  preview state is process-local. The resource and text fallback do not edit or
  execute; canvas editing, testing and failed-answer disclosure are described
  below. Initial views were demonstrated in the Windows Copilot app through a
  WSL MCP engine.

- Internal Squad 1.1.0 cast/enrich primitives validate a local initialized project,
  preserve native charter bytes outside approved task/model/examples additions,
  publish against the new starter's revision and file identity, and report partial
  native effects. Existing CLI/MCP bindings remain unchanged; native member
  execution, digest-bound results and coordinator dispatch are separate work.

- Add internal, operation-unwired private filesystem mutation primitives and
  explicit creating/non-creating state-root resolution. Native Windows amd64
  tests cover protected local-NTFS creation, process-held locks, exclusive
  publication and anchored flushes; arm64 remains compile/vet only, and the
  guarded Windows CLI and release assets remain unchanged.

- Optional native Squad chat/MCP flow with reviewed init → cast → enrich, native-state member validation, owned-field edits and revision-bound local previews. Requires an explicitly configured, separately qualified standalone Squad 1.1.0 dependency. Keeps the four tools and canonical v1 behavior; native Squad canvas and package/team execution are not included.

- Canonical agent files can declare `github_list_merged_prs` in an optional Tools
  section. Add host-owned binding validation and a bounded journaled tool-preview
  executor, qualified with hermetic tests. File Run/Test accepts `--tool-bindings
  FILE` for the read-only GitHub MCP provider described below. Tool-free execution
  is unchanged; Copilot model routes still refuse declared tools.

- `kmx run-profile export FILE` provides a temporary demo bridge, not the deploy contract. It writes a deterministic frozen canonical agent-file profile to stdout, with instructions, exact authored model/judge identities, ordered examples/assertions and SHA-256 hashes. Every target example is `not_run`; export is offline and is not evaluation evidence, a package or deployment authority. Output includes authored content and has no managed output-file privacy guarantee.

- Edit instructions and examples directly in the Copilot agent canvas, then save and test through the existing rooted MCP tools. The text fallback stays read-only.
- Show bounded failed-example answers by default in the canvas, with passing answers opt-in. Other MCP clients retain their existing disclosure defaults.

- Open an existing agent canvas with the discoverable `/kmx open PATH` skill. Opening reuses the setup-confirmed root and does not edit, run or test the agent; CLI clients retain a text fallback.

- Markdown-file `kmx run FILE` and `kmx test FILE`, plus their shared MCP path,
  accept explicit `--provider copilot` using prior model login and an exact
  authored Chat model. The endpoint is pinned, credentials stay in the existing
  model-access transport, and safe Copilot failures stop later sends. Selecting
  Copilot sends instructions and inputs (plus answers/rubrics for judging) to
  the hosted service. Execution remains tool-free preview without a Sessions
  journal, inference retries, provider/model fallback or deployment evidence;
  default fake/local behavior is unchanged.

- A Copilot-default Release writer sample and a checksum-verified, user-confirmed
  setup helper for macOS and Windows Copilot with WSL. The preview guide explains
  the chat-based Run/Test loop, model changes, uninstall and safe setup logs.
- Native Linux/macOS isolated plugin install checks on pull requests and on the
  exact cross-built release archives before publication. The helper uses HTTPS
  and checksums, not independent signature verification; older bundles refuse.

- File `kmx run FILE` and `kmx test FILE` accept `--verbose` for content-free
  execution diagnostics and timings on stderr, including safe transport failure
  categories. JSON stdout and exit contracts are unchanged; content still
  requires `--show-content`. Diagnostic write failures stop later execution.

- Canonical file Run/Test can use the host-bound read-only GitHub MCP tool through an explicit local binding and environment credential reference. Reads are bounded, journaled and replayable without network or credentials; Copilot tools remain unsupported.

### Changed

- Canonical `kmx test` replaces `kmx eval`, including all six helpers:
  `cleanup`, `diff`, `explain`, `export`, `verify` and `from-sessions`. Test is unreleased;
  v0.8.1 uses `eval`. The hidden legacy root and helpers remain callable for at
  least one released version, adding exactly one stderr line per invocation:
  `kmx eval is deprecated; use kmx test (same arguments).`
  Arguments, flags, stdout, existing stderr labels, exit codes and state are
  unchanged apart from the notice. Authored `eval/`, packaged `evals/`,
  `evalTarget`/`evaluationTarget`, schemas, APIs and machine JSON remain unchanged.
  Docs/site lead with Run / Test / Publish: existing live Orka Run, workspace
  Test, registry `push` for Publish, and Deploy to real targets. No local Run,
  numbered local versions, plugin or new Publish command is introduced.
  Old guide fragments remain valid; removal requires a later announced breaking
  release. See [Test compatibility](docs/kmx.md#test-compatibility-unreleased).

- Canonical `kmx deploy` replaces packaged-suite `kmx lift`, with the same
  arguments, flags, runtime/evidence/consent guards and exit contracts. Deploy is
  unreleased; v0.8.1 uses `lift`. Deprecated `kmx lift` stays hidden but callable
  for at least one released version, preserving stdout and stderr stage labels
  and adding exactly one stderr line per legacy invocation:
  `kmx lift is deprecated; use kmx deploy (same arguments).`
  Canonical human help/output says Deploy; machine JSON keys/values (including
  `targets` operation `"lift"`), public Go/API/internal `Lift` names, evidence,
  storage and annotations are unchanged. Old guide fragments remain valid.
  Removal requires a later announced breaking release.

- Loose-directory packaging uses default non-executable regular-file modes and
  exact declared ToolProvider payload modes, independent of host execute bits.
  Existing OCI tar metadata and exact artifact identities remain authoritative.
  Normal generated native artifacts retain their identities and eligible proofs.
- Workspace `lift --verbose` no longer prints judge rationale by itself; use
  `--show-content` for non-pass content. Deploy-only and plan-only Lift refuse
  that flag. The judge policy's local-diagnostics clarification preserves the
  historical v2 evidence fingerprint and existing receipts; no new receipt
  fields, diagnostic references, signatures or exports are introduced.
- AgentSuite validation now uses `github.com/agent-primitives/suite v0.5.0`.
  Editor and external-validator registries should select the complete v0.5.0
  schema bundle with `/schema/v0.5/` IDs. Historical hosted v0.1.0 schemas
  remain byte-for-byte unchanged, not aliases to the new contract. Wire versions,
  media types and digests of unchanged suite documents are unchanged.

- Credential-shaped suite instructions now add a fixed redacted warning before
  the existing source/package hard refusal. Instructions are non-confidential
  and must never contain secrets; clean lint is not a secret-free guarantee.
  Package diagnostics expose this fixed warning instead of only a generic
  verification failure. Other credential and invalid-Unicode refusals remain.

- Complete fresh Orka Lift creates now retain a namespace-local shared release
  record and explicit private ownership proof for the opt-in updater
  ([#561](https://github.com/kaimahi-agents/kaimahi/issues/561)). Composed Lift
  preflights retention before evaluation/publication spend. Shared completion
  uses conditional writes and requires a private acknowledgment checkpoint;
  ambiguous or partial first creates remain blocked, without automatic cleanup.
  Exact reuse/mixed reuse/skips never enroll. Kagent is unchanged; plain Lift
  remains create-only. See the [update contract](docs/suite-workspace.md#update-and-rollback).

- Record release notes in per-PR fragments and describe repository responsibilities at directory level, avoiding shared changelog entries and file-count edits when adding files.

- Update the portable AgentSuite dependency to published v0.7.0 and use its focused core, OCI, evidence and experimental flow imports instead of the deprecated root API. Current editor and validator setup uses its v0.7 schema bundle; frozen historical schema assets and consumer fixtures are unchanged. No CLI or runtime behavior change is intended.

- Route full Orka runtime qualification to relevant or unknown PRs, daily main nightly and fresh exact-commit release checks. Explicitly safe PRs and routine main pushes skip only this worker; required check names and other worker coverage remain intact. Nightly failures surface in Actions and one assigned health issue.

- File Run/Test and MCP now use embedded Sessions for fake/local routes, with selected examples and optional ad-hoc input in one private cleartext journal. Journaled judging uses two samples. If either the agent or judge uses Copilot, execution stays direct and explicitly unjournaled; fully journaled Copilot is follow-up work.

- Run hermetic kagent contracts on PRs and routine main pushes, explicitly not live qualification. Move the unchanged full A2A/evaluation/push/lift proof to nightly, manual and fresh exact-commit release checks. Required checks remain intact; live failures may be detected at nightly or before release rather than on the PR.

- Print payload-free UTC phase timestamps for full kagent bootstrap, live A2A, suite evaluation, private helpers and cleanup. Preserve qualification commands, cases, pins and failure behavior; no execution optimization.

- Make the read-only Copilot agent canvas author-focused, with instructions first, compact header status, a failure-only band and plain-language example checks.
- Show friendly source labels and complete paths in Details, with an explicit Copy path action that respects clipboard permissions.
- Keep incomplete, partial and stale observations distinct from full passes; mark failed contains checks as missing without changing tools, execution or the answer-content opt-in.

- The Copilot agent canvas uses compact main and test-detail views, keeps unsaved drafts when navigating, shows known pre-edit results in grey and server-stale observations neutrally, and supports testing one selected test through the existing revision-checked MCP flow. Light and narrow layouts share the same controls.
- Canonical agent files may save an explicit empty test list while authoring. Ad-hoc Run still works; Test asks for a test. Zero-test files remain ineligible for run-profile export and cannot supply packaging or deployment evidence.

- Run all four App test shards on both Linux and macOS in CI, with OS-specific
  diagnostic artifacts and both platforms required by the hygiene gate.

- Move per-package behavior descriptions from the repository map into Go package comments, so feature PRs no longer conflict on the map.

### Fixed

- A private Orka ownership checkpoint could leave visible `4.json` after a
  post-link persistence failure, incorrectly authorizing a later update. Fresh
  create and internal update now require a final `committed.json` marker after
  publication checks and output cleanup succeed. Lost shared-write responses
  still block; there is no retry, backfill or automatic cleanup.

- MCP Run/Test now return completed failing or inconclusive assertions as normal tool results with a compact, bounded summary instead of duplicated JSON. Full safe structured results remain available. Parse, model, execution, cancellation and journal errors remain tool errors; CLI exit codes are unchanged.

- Direct Copilot access can use explicit `COPILOT_GITHUB_TOKEN` or, when the OS
  keychain is unavailable, an existing github.com `gh auth token` at runtime.
  The stored-login fallback ignores ambient `GH_TOKEN` and `GITHUB_TOKEN`;
  explicit `login --gh` keeps normal gh environment behavior.
  kmx never saves these runtime tokens to a keychain or token file. Invalid
  explicit tokens, missing kmx credentials and service refusals do not silently
  switch accounts. `kmx model login` explains these options instead of failing
  when the keychain is unavailable, without claiming a credential was saved.

- Copilot plugin integration tests consume structured preview results rather than
  parsing compact human summaries as JSON. The skill preserves completed example
  failures as normal tool outcomes, without treating `isError: false` as a pass.

- Accept native absolute Create/Edit/Run/Test paths whose cleaned, symlink-resolved targets remain inside the configured MCP root and supported agent-file layouts. Preserve rooted file access and refuse outside targets, dangling links and symlink escapes.

- Local preview refuses missing or mismatched response models and assertion metadata containing a resolved credential before returning it. Keep the question-mark journal path test Unix-only so valid Windows paths remain testable.
- Run/Test help discloses cleartext journal content, retention and clearing, with the no-journal exception limited to Copilot routes. Packaged-plugin tests check the journaled fake route.

- Restore the Orka caller-boundary check after local-preview parser and App judge-test callers were added. Record the audited references without changing guard logic or path exceptions.

- Cancelling a Copilot agent-file Run/Test during judging leaves the interrupted test and later tests `not_run`, without partial answers or passing checks. Fully finished tests keep their results. MCP clients cancel the existing request and recover the safe result through the agent resource; no new tool or canvas UI change is included.

- Drop observations started before a newer canvas action or accepted result, even for unchanged source. Any prompt or test draft immediately greys every known result, including detail checks and answer highlights, without replacing the edited field. Repairs are invalidated when their original assertion changes.
- Preserve drafts when an agent changes outside the canvas, with explicit Compare, Keep my edits and Use the new version choices before another save. A refused or uncertain action never restores a previous green result as a fresh test.

- Resolve macOS temporary paths in App test fixtures, remove a Linux-only shell
  utility path and synchronize pagination cancellation with the intended request.
  Local `make test-app` artifact directories honor `TMPDIR` on macOS.

- Operational CLI adapters now pass the Cobra invocation context to an
  Runner copy on the invocation's App, so existing context-aware subprocesses,
  network requests and waits observe caller cancellation and deadlines. The
  App identity and recovery metadata are preserved, and the original shared
  Runner retains its context. Target detection
  uses the same binding; offline/help behavior, signal ownership and explicit
  child/cleanup context policies are unchanged.

- Synthetic release rehearsals include the workflow run ID and attempt in the
  stamped version, keeping repeated same-commit runs' binaries and checksum
  manifests distinct for transparency-log signing. Real tag versions and
  explicitly supplied dry-run versions are unchanged; signing and log upload
  remain required.

### Breaking

- Older preparation-cache entries refuse without a current packing-policy marker.
  Move aside only the named entry and retry; keep managed passing state and
  standalone receipts. Rebuilding a different artifact requires fresh evaluation,
  never edited or relabeled proof. See the
  [cache upgrade](docs/suite-workspace.md#portable-directory-modes-and-preparation-cache-upgrade).
- AgentSessions `kmx test --development-host FILE` requires `--bindings FILE`
  and records the binding digest in its evidence, including judged and repeated
  proof. AgentSessions passing receipts move to v2
  (`kmx.agentsessions-suite.passing/v2`); v1 receipts and managed v1 proof are
  refused, with no fallback to an older record; Orka/kagent receipts are unchanged.
  **Instead:** evaluate again with `--development-host FILE --bindings FILE`.
- Suite loading now refuses Suite/ToolProvider extensions without an explicit
  `critical` boolean, present empty/null ToolProvider `variants`, and explicitly
  null tool `outputSchema` properties. ToolProvider JSON decoding also rejects
  unknown or duplicate properties. Declare extension criticality and omit absent
  variants/output schemas; keep nonempty variants and valid schema descriptors.
  Changed documents require rebinding their descriptors and ancestors. Explicit
  `critical: false` remains valid; Agent extension acceptance is unchanged.

- Destination status now reads the exact shared release ConfigMap for owned Orka
  rows. Read-only roles need named-record `get` access; denial makes the shared
  observation unknown and the report incomplete (exit 1), including legacy v1
  rows. Existing object provenance remains visible; no permission is granted or
  inferred automatically. See [status access](docs/destination-status.md#selection-and-access).
- All unmarked private ownership proofs are refused, including successful test
  baselines from unreleased #573 builds. A visible `4.json` alone is not a
  completed acknowledgment. Recreate an owned pair explicitly when ready; no
  migration, enrollment, backfill or deletion is performed automatically. #573
  has not been released; this affects only those unreleased test baselines.
- Fresh Orka pair Lift now requires namespace-local ConfigMap create plus
  named-record get/patch access and available private retention capacity. Missing
  access/storage and stale records fail closed. Shared history rolls only old
  completed entries (ten retained, 64 KiB total); pending/partial proof never
  rolls. Agents lifted by v0.8.1 or earlier are not enrolled for update; explicitly
  recreate when ready. See [operator recovery](docs/suite-workspace.md#release-retention-and-operator-recovery).

### Upgrading

- For unreleased main builds, change maintained `kmx eval` invocations and all
  six helper routes to `kmx test`. v0.8.1 still uses `eval`. The hidden alias
  remains callable for at least one released version with one deprecation notice
  on stderr; stdout, arguments, exit codes and state do not change. Do not rename
  case directories, receipts or configuration fields. No data migration is needed.

Known gaps in this preview:
- Copilot-model runs aren't saved to the preview journal or replayable yet. Fake and local models are.
- Copilot-model previews can't call tools yet. Local file Run/Test can use the read-only GitHub tool when explicitly configured.
- The canvas has no version history or revert yet.

## v0.8.1 — 2026-10-17

### Added

- A copy-paste AKS Free-tier walkthrough now follows the supported lifecycle:
  author and prove a native Orka workspace locally before creating billable
  Azure resources, configure owner-held Provider credentials, publish an exact
  ACR digest, review source-evidence promotion, Lift once, inspect, and teardown.
  It validates conservative model/endpoint/name inputs before setup and records
  the local state that teardown intentionally leaves behind. [AKS demo](docs/aks-demo.md).

### Fixed

- Successful Orka result sessions print one concise ServiceAccount notice and
  link to the detailed authority boundary instead of repeating version-specific
  RBAC caveats before every answer. Permissions and failure diagnostics are unchanged.
- Orka installation inspects every Helm release state with status filters shared
  by Helm 3 and Helm 4. It no longer guesses from a second version probe or uses
  Helm 3's removed `helm list --all` flag.
- AKS plan, provisioning, optional observability, ownership reads, and teardown
  isolate Azure CLI core commands from installed extensions without moving the
  caller's authentication/configuration. Managed-cluster inspection uses the
  core ARM resource command. Azure CLI 2.86's non-fatal built-in Service
  Connector module-lock line is narrowly suppressed while every other diagnostic
  and command failure remains visible.
- Generated retry and follow-up commands preserve the executable that launched
  KMX. A run started with `./bin/kmx` no longer prints a bare `kmx` recovery that
  can resolve to an older installation on `PATH`.
- AKS setup and runtime/list/show messages no longer suggest the removed
  `create --live` command. They point to guided quickstart or offline
  Create-Prove-Lift, with owner-managed Provider configuration after AKS setup.
  Exact-version Helm releases missing KMX ownership/configuration now get a
  specific repair-or-cleanup refusal.
- Native workspace and Orka/kagent binding validation rejects unresolved
  whole-value model placeholders while loading source and angle-bracket endpoint
  placeholders before placement or execution. Pre-effect refusals name safe
  remediation without echoing authored values; later drift retains unknown,
  no-retry semantics. Candidate cleanup recovery also preserves the invoking
  executable.
- Orka workspace eval and composed Lift distinguish unsuccessful Task execution
  from assertion failures and report executed, planned, passed and unexecuted
  runs after an abort. Root eval adds `-o text|json` (default text; JSON is
  Orka-only); Lift JSON adds run-local execution counts without changing existing
  fields. Abort, verified cleanup, passing-evidence and publication rules are
  unchanged; fully executed threshold misses retain normal run-pass summaries.

## v0.8.0 — 2026-10-17

### Added

- Provisional multi-member workspaces support deterministic offline preparation
  and inventory loading with `schemaVersion: 2`. Multi-member eval, workspace push and Lift
  remain unavailable.
- `kmx suite policy FILE [--administrative-policy FILE]` resolves portable
  deny-write and v2 network policies offline; administrative policy can only
  add restrictions. Orka members can declare `policy: policy.json` for
  packaging and unevaluated publication. No policy is installed or enforced;
  eval, evaluated publication, Lift and AgentKit export refuse declared policies.
- `kmx eval export --member ID --out FILE` exports completed exact-suite/member
  evidence without model, cluster or registry calls.
  `kmx eval verify --suite LAYOUT --evidence FILE --expect FILE` checks it offline
  against independently reviewed expectations. Unsigned consistency is not
  evaluator authentication, execution attestation or a replacement for private
  push/Lift evidence.
- Release Lift records suite/consumed-evidence identity, evaluation environment,
  promotion gate and attempt time on newly created Orka/kagent primary objects.
  Exact reuse and verified skips remain no-write; existing objects are not
  backfilled and records grant no replay authority.
- `kmx status --to NAME [--workspace DIR] [-o table|json]` inventories deployed
  Orka/kagent Agents, provenance and placement readiness using bounded GETs only.
  Missing, unknown and foreign provenance remain distinct. No Secrets, private
  history, evaluation or repair; Ready means placement, not an evaluated answer.
- Single-member `schemaVersion: 3` packages explicit runtime/build profiles
  under one suite digest.
  `push --runtime-profile ID [--platform OS/ARCH] --unevaluated` selects an offline
  view while transferring every profile; `suite build` resolves exact profile
  tuples. Profiled execution, proof creation/export, evaluated publication, Lift
  and AgentKit production remain
  unavailable. [Offline runtime profiles](docs/suite-workspace.md#explicit-runtime-profiles-offline-only).

### Changed

- AgentSuite now comes from `github.com/agent-primitives/suite v0.4.0`.
- AgentSessions now uses `github.com/agent-primitives/sessions v0.2.0`.
  Development-runtime/profile and proof identity pins are unchanged; this does
  not upgrade operator daemons or qualify new lifecycle, approval or tool-executor
  capabilities.

### Breaking

- Root `kmx status` now requires a named destination.
  **Instead:** use `kmx status --to NAME` for destination inventory or
  `kmx orka status` for the unchanged table-only Orka runtime report.
- The in-repository portable AgentSuite Go package is removed without forwarding.
  **Instead:** import `agentsuite "github.com/agent-primitives/suite"` and pin
  `v0.4.0`; the package name remains `agentsuite`.

### Upgrading

- Portable evidence export needs fresh evaluation from an identified, unmodified
  build. Use a tagged Go module build with its checksum, or a clean checkout
  with a full VCS revision, and re-evaluate older records lacking capture metadata.
  Existing private receipts and push/Lift gates are unchanged.
- Destination status exits 1 for unknown/unhealthy/drift, 2 for complete known
  pending placement and 0 for complete ready/empty inventory; failure takes
  precedence over pending. Unsigned provenance does not prove original UID
  continuity.

## v0.7.0 — 2026-10-15

### Added

- Terminal `kmx create NAME` prompts for missing model and instructions-file inputs; nonterminal use still requires both flags, and blank inputs refuse.
- Named workspace destinations support `eval/lift --to NAME`; `kmx ctx [--check]` lists them without switching context.
- Root Orka/kagent eval supports `llmJudge` assertions with an explicit judge endpoint and two independent scores.
- Cases support repeated runs and pass thresholds with `runs`/`minPasses`; any unknown result vetoes a pass.
- Orka eval uses disposable candidates; leftovers are reported, never auto-deleted, and `kmx eval cleanup` requires confirmation to delete a selected run.
- Root eval saves private passing evidence automatically, with optional `--out`; push picks up matching evidence.
- Orka `kmx lift --to NAME` composes eval → push → Lift with exact-version skips, one confirmation and four default output lines.
- AgentSessions development-host eval supports judged and repeated cases; its proof supports push, not Lift or receipt diff.
- Native suites now package authored cases as public AgentSuite evaluations under `evals/<member>/`; YAML authoring stays unchanged.

### Fixed

- Orka result access selects a free loopback port automatically; explicit `--result-port` values are honored without fallback and startup failures name the port problem.
- Judge requests use `max_completion_tokens`, no temperature override, and exact model/version matching.
- `kmx local up` works with Helm 4 while retaining Helm 3 support.

### Breaking

- AgentSuite content references now require OCI-style descriptors; older extracted suites are not compatible.
  **Instead:** regenerate suites with current tooling or update their references as described in the [upgrade guide](docs/releases.md#upgrading-from-v060).
- v0.6.0 suites and receipts are refused with a named reason; there is no migration reader.
  **Instead:** rerun `kmx eval` and `kmx push`.
- Case IDs within a member must differ by more than letter case; `contains`/`notContains` values and tool names cannot contain Unicode line/paragraph separators.
  **Instead:** rename colliding case IDs and replace those separators in `contains`/`notContains` values or tool names.
- Root workspace push requires matching passing evidence by default.
  **Instead:** run eval first, supply `--evaluation FILE`, or explicitly opt out with `--unevaluated`.
- Judge receipts now bind the request style and older judge proof is refused.
  **Instead:** evaluate again to generate fresh evidence.

### Upgrading

- Bare push needs saved `publishRepository`; composed Orka Lift also needs the destination's `evaluationTarget`.
- Standalone digest Lift needs `--promote` for disposable candidate evidence, even in the same environment; composed Lift discloses that promotion.
- Upgrade readers before exchanging judged/repeated evidence; existing files and deployed resources are not automatically migrated or deleted.

## v0.6.0 — 2026-10-14

### Added

- **Create → Prove → Publish → Lift:** `kmx create NAME` authors one tool-free
  workspace member; `kmx eval` evaluates all cases and exports a private receipt
  only on a full pass; `kmx push --evaluation FILE REF` publishes the exact suite.
  Create/eval/push discover the nearest `workspace.yaml`; `--workspace DIR`
  selects an exact root. Orka is default; kagent supports exact v0.10.3
  Go/linux-amd64 with author-owned bindings. [Workspace guide](docs/suite-workspace.md).

- `kmx lift REF@sha256:DIGEST` deploys an immutable suite with explicit passing
  evidence and destination selectors. `--plan` is GET-only; `--promote` permits
  a matching destination with source-only evidence, not destination evaluation.
  Orka can reuse exact evaluated resources; kagent refuses occupied targets.
  Lift runs no model and does not update, adopt or roll back resources.

- Pinned AgentSessions chat supports offline packaging and explicit loopback
  `eval --development-host FILE`, with full-pass evidence for evaluated push.
  Hosts are unauthenticated and retain cleartext journals; this development proof
  cannot authorize Lift or receipt diff. [Development evaluation](docs/suite-workspace.md#agentsessions-development-evaluation).

- `kmx suite diff` compares extracted suites and OCI layouts semantically.
  Registry suite push attaches **unsigned** provenance by default; it is not
  authenticated attestation.

- AgentSuite Tools gain effect, approval and delegated-user authorization
  metadata. This specifies runtime obligations, not a credential broker or
  runtime approval implementation.

### Changed

- Quickstart and console creation now use native suite evaluation, publication
  and reviewed Lift. Console Ready means placement, not an answer.
- New local kind clusters use a five-second kubelet sync cadence; existing
  clusters are unchanged. Repeated local setup preserves matching inference
  resources, and quickstart checks current Agent readiness before reconnecting.

### Breaking

- Legacy `bundle` commands, user `agent.yaml` workflows/receipts,
  `create --live` and previews, bundle-mode run, `--bundles`, and the old
  evaluation CI action are removed; suite status/retirement have no replacement.
  **Instead:** author with `kmx create NAME --workspace DIR --model MODEL --instructions FILE`,
  review cases, then eval/push/Lift; retain a historical release for old workflows.

- The `kmx agent` group is removed without aliases or stubs.
  **Instead:** use root `create/chat/run`, `orka show/list`, and
  `eval diff/from-sessions`; `run --agent NAME` addresses independent live
  Orka Agents, not suite-owned objects.

- Root `ctx/up/down` and the old infrastructure `lift/lift down` aliases are removed.
  **Instead:** use `context show/use`, `local up/down` and `aks up/down`;
  root `lift` now means digest-pinned suite deployment.

- Workspace mode is removed from `kmx suite push`.
  **Instead:** use `kmx push [--workspace DIR] REF`; extracted-directory
  `suite push DIRECTORY REF` remains.

- `eval diff` accepts only full-passing Orka/kagent suite receipts, not old
  bundle or AgentSessions receipts.
  **Instead:** generate fresh suite evidence; no old-receipt conversion exists.

- Chat `/lift` and the old console Lift action are retired, with no live-copy fallback.
  **Instead:** use root digest Lift with explicit evidence; console creation
  separately composes the native workflow.

- Chat/quickstart `--azure-discovery` is removed.
  **Instead:** select the destination context explicitly and provision AKS
  separately with `kmx aks up`.

- Guided tool/host-inference shortcuts, `--skills`, `--out` and
  `--result-service-account`, and in-place tools/inference editing are retired.
  **Instead:** author tool-free workspace source, select a Ready Orka `openai`
  Provider for its exact model, and configure native result access separately;
  manage live resources with Kubernetes tooling.

- Orka installs using the old `kmx.kaimahi.ai/installed` Helm label are no
  longer recognized as kmx-owned; the label is now `kmx.kaimahi.dev/installed`.
  **Instead:** back up before replacing an owned disposable local cluster
  (`local down` deletes its data), or arrange an operator-managed fresh
  persistent installation; relabeling live objects alone is insufficient.

- `targets -o json` changes capability names to
  `create/eval/push/lift/chat/run/inspect/diff` (AgentSessions also has `export`),
  Orka/kagent `kind` to `native-profile`, and AgentSessions tier to
  `development-only`; `schemaVersion` remains 1.
  **Instead:** update JSON consumers to the new names and per-operation support flags.

- Alpha Go APIs remove speculative environment-management methods and replace
  the old AgentSuites package/build/publish methods with staged operations.
  **Instead:** adopt `EnvironmentSpec → Up → Environment` and
  `AgentSuites.Validate/Build/RecoverBuild/Push/RecoverPush`; removed environment
  inspection/teardown/recovery methods have no replacement.

### Upgrading

Upgrading the CLI migrates or deletes no saved files or deployed resources.
Old receipts remain readable only by the historical release that understands
them. See [upgrade instructions and cluster backup boundaries](docs/releases.md#upgrading-kmx).

## v0.5.1 — 2026-10-12

### Fixed

- `kmx targets --detect` accepts kubectl `List` Deployment responses, reporting empty lists as absent and labelled controllers as present.
  Malformed responses and failed reads remain unreadable and exit nonzero. [Target detection](docs/runtime-adapters.md#read-only-target-view) ([#429](https://github.com/kaimahi-agents/kaimahi/pull/429)).

- Homebrew formula tests check the binary's version instead of help prose; the release workflow runs the generated test before publication. [Formula test fix](https://github.com/kaimahi-agents/kaimahi/pull/426).

- Orka lift-prerequisite recovery commands preserve and safely quote the selected destination context.
  Prerequisite checks and installation defaults are unchanged. [Recovery advice](https://github.com/kaimahi-agents/kaimahi/pull/423).

## v0.5.0 — 2026-10-11

### Added

- `kmx agent evaluate` supports `contains`, `notContains` and RE2 `regex` assertions alongside `expectContains`, and keeps immutable run receipts.
  `toolCalled` and `toolNotCalled` syntax is accepted but currently reports unknown. [Evaluation assertions](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#evaluating-a-deployed-revision).

- `kmx agent eval diff <before.json> <after.json>` compares saved evaluations offline by case and assertion, showing regressions, fixes and lost evidence without rerunning models.
  Comparison requires compatible full-case v2 receipts; regressions and incomplete comparisons exit nonzero. [Compare evaluations](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#comparing-saved-evaluations-offline).

- The console's `R` key shows recent Orka Tasks, task lanes and sanitized activity for the selected agent, context and namespace.
  Discovery is bounded and reports missing or unreadable evidence without showing conversation payloads. [Interactive console](docs/interactive-chat.md).

- The reusable kmx GitHub evaluation action runs a tracked bundle's cases through Sessions, optionally replay-checks receipts, and fails the job unless every case passes.
  Sessions evaluation does not satisfy an Orka lift gate. [Run evals in CI](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#run-evals-in-ci).

- `kmx agent eval from-sessions <session-uid>...` extracts private single-turn UID/prefix references and authored assertions without copying recorded inputs or answers; existing `--out` manifests append atomically and refuse duplicates.
  `agent evaluate --from-sessions` verifies source prefixes in memory and runs fresh candidate sessions, with named candidate/judge confirmation and payload-free output unless `--show-content` is explicit. Expired/unavailable references remain unknown; current diff conservatively makes the whole comparison incompatible when a case lacks runtime identity ([#403](https://github.com/kaimahi-agents/kaimahi/issues/403)). Ordinary authored-input evaluation and v2 receipt formats are unchanged. [Recorded cases and privacy](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#recorded-sessions-as-reference-cases).

- `kmx agent evaluate` supports explicit `llmJudge` rubric/threshold/model/version assertions with `--judge-base-url` and optional `--judge-key-env` (environment name only), without model/endpoint defaults.
  Two temperature-zero scores must agree on the threshold side; split or unavailable evidence is unknown. Receipts bind identity/policy/threshold and both scores without payloads; input/answer/rubric go to the chosen judge provider and terminal reasoning may contain answer text. Replay makes zero judge calls and explicitly leaves judge outcomes unverified. [Judge configuration and privacy](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#explicit-llm-judge).

- v0.5.0 introduces `kmx context show/use` to inspect and save context selection, and `kmx local up/down` for the existing local kind setup and deletion operations. [Command names](docs/command-semantics.md#implemented-entry-point-alignment-unreleased).

- `kmx targets` reports runtime capabilities offline, with optional Orka and Sessions probes. It selects, installs and deploys nothing; unreadable probes exit nonzero, distinct from absence. [Target view](docs/runtime-adapters.md#read-only-target-view).

- `kmx suite validate`, `push` and `pull` validate and exchange portable AgentSuite artifacts through OCI registries or local layouts; push reports the digest and registry tag pulls show a digest-pinning hint. [Suite commands](docs/kmx.md#commands).

- Experimental `kmx suite build` builds an agent image with Docker buildx and an OCI-export-capable builder.
  Output is **not AgentSuite-conformant**; use `--require-attestations` to refuse missing attestations and never pass a key value to `--model-api-key-env`. [Build limits](docs/kmx.md#experimental-agentsuite-image-build).

- `kmx agent evaluate --sessions <host:port>` evaluates bundles on a Sessions chat host; `kmx agent verify --sessions <host:port>` replays receipts locally with zero live model calls.
  This is eval-only and reference verification, not host attestation or a lift gate; remote hosts require verified TLS, and host journals retain conversation content. [Sessions evaluation and verification](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#evaluating-on-agentsessions).

- Portable agents can declare an explicit `spec.coordination.allowedAgents` helper allowlist for Orka delegation.
  Older readers and kagent refuse this field: upgrade readers before using it with Orka. [Bundle compatibility](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/bundle-format.md).

- Core-only bundles may omit `extensions` or use `extensions: {}`.
  `extensions: null` and unsupported runtime behavior are refused, not silently dropped. [Bundle compatibility](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/bundle-format.md#what-a-reader-accepts).

- `kmx agent create --runtime kagent <name>` creates on preinstalled exact kagent v0.10.2, optionally sending one task; Orka remains the default.
  Create-only, with no rollback or ambiguous retry; kagent and Service-proxy audits may retain conversation content. [Prerequisites and limits](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/kmx.md#explicit-kagent-v0102-create).

- Explicit `kmx agent lift` and `kmx agent status` with `--runtime kagent --runtime-version v0.10.3` support Go declarative bundles on an operator-prepared target with `--to-context` and desired `KagentBindings`.
  Lift is create-only or complete UID-bound receipt reuse, with no adoption/update; receipts prove readiness, not execution. Dedicated CI qualifies Go plan/lift/repeat/status/conflicts and a separate one-shot native A2A MCP answer with payload-free evidence. [Version-qualified placement](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#exact-v0103-declarative-bundle-placement).

- v0.5.0 includes `checksums.txt.sigstore.json`, a keyless cosign bundle for the four binaries' checksum manifest.
  Earlier releases and the Homebrew formula are not covered; installers still verify checksums, not signatures automatically. [Verify a release](docs/releases.md#verify-a-release).

### Changed

- `kmx ctx`, `up` and `down` remain compatibility routes; help and hints use `context` and `local`.
  Notices go to stderr with no removal scheduled; hidden `kmx aks up/down` remains callable. [Command compatibility](docs/command-semantics.md#implemented-entry-point-alignment-unreleased).

- `kmx agent run` submits one Task without rewriting a deployed bundle or live Orka Agent; `kmx task result` retrieves its phase/answer without resending, with answer-only run stdout.
  Timed-out Tasks remain retrievable; pending results exit 2, failed/cancelled results exit 1. [Run and retrieve results](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#running-an-existing-agent).

- Native Orka coordination supports same-namespace helpers, concurrency/depth limits and drift checks. Lift/`--plan` refuse missing helpers or coordination schema; autonomous Job loops remain unsupported. [Configure coordination](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#coordination-in-agentyaml).

- Orka create and its wizards accept native `azure-openai` inference.
  Supply a matching deployment/model, HTTPS resource root and existing Secret; lift requires a matching Ready Provider, while `/openai/v1` remains `openai`. [Azure inputs](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/kmx.md#default-orka-contract).

- Bundle lift can require a passing staging evaluation through `lift-policy.yaml` or `--require-evaluated <context>`.
  Missing, stale, partial, failed or unknown evidence blocks lift; receipts are forgeable local evidence: **do not commit them**. [Evaluation gates](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#requiring-evaluation-before-lift).

- `kmx agent lift --override-gate "reason"` explicitly bypasses only an evaluation gate and prints/records the reason and bypassed condition. Interactive `/lift` and console refuse before deployment review, with no silent bypass. [Gate overrides](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#requiring-evaluation-before-lift).

- `kmx agent retire` checks dependents, deletes created objects and releases adopted/legacy objects.
  `--delete-adopted` opts into deleting adopted objects; the selected Provider, its Secret and existing Tasks remain untouched. [Retirement and ownership](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#retiring-a-bundle-from-a-target).

### Fixed

- Recovery advice preserves local and AKS selectors and safely quotes the selected context, including status/Orka diagnostics and retired-edit migration hints.
  Teardown is unchanged; advice no longer suggests deleted Make targets. [Recovery commands](docs/command-semantics.md#implemented-entry-point-alignment-unreleased).

- Message text after `--` stays positional data instead of being interpreted as a `--context` or legacy `-context` selector. [Context parsing fix](https://github.com/kaimahi-agents/kaimahi/pull/407).

- Quickstart and native chat check Task-result read access before creating a Task, stopping on denied or malformed responses.
  No Task is created on refusal; earlier setup may remain. [Result-access fix](https://github.com/kaimahi-agents/kaimahi/pull/365).

- `kmx agent show` and bundle-aware `kmx agent status` return a failure when their JSON output cannot be written. [JSON output fix](https://github.com/kaimahi-agents/kaimahi/pull/415).

- Creation and quickstart help distinguish offline rendering, optional bundle persistence, fixed-mode answers and guided readiness/chat.
  Operations, defaults and flag names are unchanged. [Help improvements](https://github.com/kaimahi-agents/kaimahi/pull/416).

- kagent refusal messages identify unsupported chat and the retired AKS installer payload, and point to `kmx targets` for supported operations.
  Explicit bundle lift/status and historical AKS teardown remain available. [Runtime guidance](https://github.com/kaimahi-agents/kaimahi/pull/419).

- `make build` delegates freshness checks to Go so source, embedded-data and build-option changes rebuild the CLI. [Build and verify](docs/development.md#build-and-verify).

- `install.sh --help` prints its complete header instead of truncating instructions or showing script code. [Installer](install.sh).

- Console Prepare can repair the quickstart Kubernetes Tool's scoped policy-reader grant.
  Install and lift refuse missing/ambiguous worker identity or ineffective authorization. [Repair instructions](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/orka-k8s-tool.md#repair-a-missing-policy-reader-grant).

### Breaking

- Model-plane commands/assets, model-overlay/credential administration, `make plane-image`, `CRED`, `ADMIN_PORT`, `OPS_PORT` and AKS `boundary`/`credential`/`plane` phases are removed; setup no longer creates plane credentials or monitoring, status is Orka-only, and version drops the plane revision.
  This deletes **no deployed databases, credentials, caches or resources**; native inference and historical AKS teardown remain. [Removal scope](docs/kmx.md).

- Registry `kmx suite push` requires `--force` to replace a tag's digest; its preflight guard is non-atomic.
  Authenticated push/pull requires HTTPS or loopback HTTP, including token endpoints/redirects; remote plain HTTP auth challenges are refused. [Registry safety](docs/kmx.md#registry-authentication).

- `kmx quickstart-wizard` now refuses: use `kmx quickstart --interactive` (`-i`); bare quickstart is unchanged.
  Before configuration loads, quickstart refuses `-o/--output` with interactive mode, and `--instructions`, `--tools`, `--skills`, `--result-service-account`, `--out`, `--verbose`, `--azure-discovery` or `--inference` without it; interactive `--task` runs a first Task only when explicit. [Quickstart modes](docs/getting-started.md#one-command-and-an-agent-that-answers) ([#336](https://github.com/kaimahi-agents/kaimahi/issues/336)).

- `kmx agent chat <name> [message]` is always interactive; removed `--interactive` fails as an unknown flag, and `--session`/`--json` remain refused.
  A message is the first interactive turn, not a one-shot; use `kmx agent run` for one Task. [Chat migration](docs/interactive-chat.md).

### Upgrading

- Replace `kmx quickstart-wizard` with `kmx quickstart --interactive` and drop `--interactive` from `kmx agent chat` in v0.5.0.
  v0.4.1 and earlier retain the old wizard and require `agent chat --interactive`. [Quickstart migration](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/getting-started.md#create-your-own-agent-then-lift-it-from-chat).

- Use `kmx context show/use` and `kmx local up/down` in v0.5.0; `ctx/up/down` remain compatibility routes.
  v0.4.1 and earlier require the old names. [Command compatibility](docs/command-semantics.md#implemented-entry-point-alignment-unreleased).

- Remove retired plane/admin commands and configuration from automation; configure supported Orka agents with native inference instead.
  Existing plane deployments and data need separate operator-owned retirement; upgrading kmx deletes none of them. [Native inference](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/kmx.md#default-orka-contract), [removal scope](docs/kmx.md).

- Use HTTPS for authenticated remote registry push/pull and add `--force` only when deliberately replacing a tag's digest.
  The tag preflight guard is non-atomic. [Registry safety](docs/kmx.md#registry-authentication).

- Upgrade receipt readers to v0.5.0 before exchanging `llmJudge` receipts.
  Older strict v2 readers reject the added judge metadata. [Judge receipt compatibility](https://github.com/kaimahi-agents/kaimahi/blob/4f91f248a542f379cad7163dec5d3ba4b981295d/docs/agent-lift.md#explicit-llm-judge).

- `CHAT_PORT` and `KMX_CHAT_PORT` are unsupported: Task-backed Orka chat and quickstart fail if fixed loopback port 19180 is occupied. Free that port instead. [Chat port](docs/aks.md#historical-resources-and-concurrent-checks).

## v0.4.1 — 2026-09-30

### Changed

- Interactive `/lift` in chat and the console now deploys an agent's local bundle through `kmx agent lift` after showing its shared plan. It writes the same ownership markers, receipt and remembered target, then connects chat to the destination. Orka and the quickstart Tool can be prepared only through a separate confirmed action; bundle lift itself installs nothing. Select an existing Ready destination Provider; Foundry creation is not part of this route. With no bundle, the clearly labelled live-copy fallback remains available but status/evaluate cannot track it. An invalid bundle refuses instead of copying.

### Upgrading

- If an agent has a local bundle and needs Foundry inference, create a Provider on the destination first, then select it when lifting the bundle. Agents without a bundle still use the untracked live-copy route.

## v0.4.0 — 2026-09-30

### Added

- The getting-started journey now leads through creating a custom agent with `kmx quickstart-wizard`, proving a local chat answer, lifting it with `/lift`, and sending a new message to verify the destination. It also describes existing Kubernetes and AKS targets and optional Orka/tool preparation (#251).

### Fixed

- `kmx console` refuses a mismatched local bundle before contacting the cluster, reuses the status report's full deployed commit for diffs, and gives a shell-safe command when a diff is truncated. Unreachable targets remain unknown without attempting a diff; the CI Go test step was streamlined (#243).
- On reruns, kmx checks the live Orka controller's image digest as well as the Helm release's pinned values. A patched, missing, unreadable or ambiguous controller is refused without changing the cluster; offline schema help now names v0.2.0 as the default (#252).
- Homebrew installation instructions now fail closed if the formula prefix cannot be resolved, and explain which release asset and checksum the formula uses (#241). Stale implementation and CI comments were corrected to describe current behavior without changing the CLI (#250).

### Changed

- Orka v0.2.0's chart AI worker does not grant itself read access to the quickstart Tool's `OutboundAccessPolicy`. kmx now grants only `get` on its named gateway policy in `orka-system` to the chart worker ServiceAccount; the Tool's logical public IP avoids DNS-answer drift during v0.2.0 SSRF validation. A live Tool Task returned the cluster's ConfigMap marker after both changes.
- The plane's Orka egress policy now selects both v0.1.3's legacy controller and the v0.2.0 chart controller in `orka-system`. Without the chart selector, a migrated owner's model call received `502 upstream unreachable` even while the v0.2.0 controller was Ready.
- **Orka installation now pins v0.2.0's Helm chart** (`orka-0.2.0.tgz`, SHA-256 checked before use) instead of applying the v0.1.3 manifest. kmx resolves Helm on PATH or fetches its pinned, checksum-verified toolchain binary, applies CRDs extracted from the verified chart before `helm install`, pins `--kube-context`, and installs release `orka` with `controller.mode=harness-v2` and `fullnameOverride=orka-api`. Orka generates the snapshot encryption Secret `orka-api-agent-execution-snapshot`; kmx never prints its value. There is no `helm upgrade --force` path. A rerun preserves a Ready kmx release and key only when all pinned image overrides match; an existing controller/CRDs from v0.1.3 or a foreign/partial release is refused, **not upgraded**. A pre-created namespace alone is allowed; standalone install also provisions the Task result reader. Offline create now defaults to the v0.2.0 CRDs; `--schema-target v0.1.3|main` remains available explicitly for old fixtures and does not install an old runtime. See [Orka setup and replacement](docs/orka.md#limits-stated) and upstream [v0.2.0 installation](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/installation.md) / [unsupported version upgrades](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/upgrading.md) (#244).

### Breaking

- Orka v0.1.3 installations are not upgraded in place. Local kind replacement requires `kmx down` followed by `kmx up` and **deletes the cluster's Tasks, Secrets, PVC-backed data, model data and plane ledger**; export anything needed before deletion. On AKS, back up existing volumes/PVCs and Orka resources/Secrets, including any existing snapshot key; after a fresh v0.2.0 installation, back up its new chart-managed snapshot key with its controller volume **without printing the key**. Neither an untested Helm upgrade nor restoring an old SQLite volume into v0.2.0 is a supported migration. Do not delete a production cluster until the recovery plan is verified.

### Upgrading

- Install the v0.4.0 CLI, then replace any existing Orka v0.1.3 cluster rather than rerunning `kmx up` against it. For a local kind cluster, export what you need first: `kmx down` deletes the entire cluster, including Tasks, Secrets, PVC-backed data, model data and the plane ledger; `kmx up` creates a fresh Orka v0.2.0 installation.
- On AKS, plan a fresh Orka v0.2.0 installation, not a Helm upgrade. Back up existing Orka resources and volumes/PVCs together with their Secrets, including the agent-execution snapshot key if one exists. Back up the new chart-managed `orka-api-agent-execution-snapshot` Secret with its controller volume and resources after installation. Never print or log the key; verify recovery before deleting production resources.

## v0.3.0 — 2026-09-29

### Added

- Homebrew installation through the official `kaimahi-agents/tap` tap: `brew install kaimahi-agents/tap/kmx`. The formula installs the existing checksummed release binaries on macOS and Linux, on Intel and ARM; no second binary build or bottle is introduced. Stable releases publish a generated `kmx.rb` asset for a reviewed tap PR, while dry runs and prereleases do not update the stable formula.
- `kmx agent evaluate <bundle-dir>` runs the bundle's `eval/*.yaml` cases (strictly decoded `id`, `input`, `expectContains`) as one Orka Task each against the deployed revision, and only when the live Agent carries the bundle's current portable digest. Each case is pass, fail or unknown and is never retried; answers are printed, and `receipts/eval-<target>.json` records the portable digest, a case-set digest, the target and per-case Task identity and answer SHA-256, never answer text. It exits non-zero unless every case passed. The Orka lifecycle adapter now implements `Evaluate`.
- `kmx agent status` shows each target's evaluation result for the current digest and case set: `pass`, `fail`, `unknown` or `none`.
- `kmx agent create` scaffolds `eval/example.yaml` in a new bundle, and bundle reruns accept an `eval/` directory.
- The quickstart `k8s-get-resources` tool now says why a workload is unhealthy: pods return per-container `ready`, `restartCount` and waiting/terminated/last-terminated reasons (`ImagePullBackOff`, `CrashLoopBackOff`, `OOMKilled`) plus their `Ready` condition and, when unscheduled, `PodScheduled`; Deployments, StatefulSets, ReplicaSets and DaemonSets return ready/updated/available counts (zero stated, not omitted); workloads and Jobs return conditions as type, status and reason. No messages, images, env or annotations are returned, and a reason that is not a Kubernetes reason word is dropped. Existing fields are unchanged. Re-run the quickstart to reinstall the tool's code.
- `kmx console` compares the selected agent with its local bundle: `b`, or "Compare with local bundle" in the actions menu. The pane reports the same state `kmx agent status` does for that row's own context and namespace. For a target that is behind, it shows what a lift would change as a diff of `agent.yaml` from the deployed commit to the working copy, read from Git rather than the cluster. A drifted target shows field paths only, as status does. Bundles are found under `agents/<name>` or `--bundles <dir>`, and a missing bundle is reported rather than guessed. The pane reads and never writes.
- `kmx agent status <bundle-dir>` compares each target's live revision, current-generation readiness and rendered-field drift with the portable definition in Git, with table and JSON output. A recorded cluster UID mismatch blocks object reads; an explicit context can be inspected without a receipt.
- `kmx agent lift <bundle-dir>` deploys an existing portable bundle to a prepared Orka target with an explicit or locally remembered context and `provider:<name>` inference. It checks prerequisites without installing them, supports a read-only `--plan` using the same reconciliation inspection as deployment, reports per-resource outcomes and update diffs, and writes per-target receipts with Git provenance after success. `/lift` remains the interactive live-agent path.
- Orka lifecycle Deploy can opt into bundle-owned reconciliation: create absent resources, reuse matching owned resources, update drift with a resourceVersion precondition, or mark an identical unowned resource as adopted. Conflicting or terminating names are refused; a completed deployment returns per-resource outcomes and a receipt with digests and target identities. Reconcile never runs Tasks.

### Changed

- `kmx agent create` (and the wizards built on it) can be rerun after a failed deploy. It keeps an existing artifact that is byte-identical to what it renders and refuses, naming the file, one that differs; it reconciles the Provider and Agent like `kmx agent lift`, reusing or updating resources carrying the bundle's markers, adopting identical unmarked ones and refusing anything else. The optional Task stays create-only after both are Ready, so a `--task` rerun still needs a new `--out`. A failed create's error says that rerunning is safe; a clean first create prints what it did before. `--dry-run` and offline creates keep create-only semantics.
- Bundle reconcile refreshes stale digest markers on identically rendered owned resources with a resourceVersion precondition, reporting reused while making comment-only revision lifts observable. Lift's uncommitted warning now names the failing Git condition.
- `kmx agent create` and the quickstart wizard now write `agents/<name>/agent.yaml` and `bindings.yaml` as the portable bundle and creation-target bindings (#227).

### Fixed

- The v0.2.0 known issue in [#217](https://github.com/kaimahi-agents/kaimahi/issues/217) is resolved: Orka can now reach the quickstart Kubernetes Tool through a same-namespace gateway route. KMX installs and waits for the exact `OutboundAccessPolicy` that permits the Tool's private Service; a missing or invalid policy fails closed. Re-run `kmx quickstart-wizard` to install the working Tool. The non-interactive `kmx quickstart` answer does not depend on it.
- Bundle status and lift find the deployed commit when the local bundle directory is reached through a symlink (#239).
- App-owned state respects `KMX_HOME` (#230).

## v0.2.0 — 2026-09-27

This is the first Orka-capable release. The entries below include the sequence
of changes since v0.1.0; where an intermediate entry describes a command that
was subsequently retired, follow the final **Breaking** and **Upgrading**
sections, not the intermediate instruction.

### Changed

- **Nothing in this tree supports the legacy kagent runtime any more, and a
  checker keeps it that way.** The removal had already taken the installer,
  the steps, the manifests, the chart, the CLI pin and the model presets; what
  it left behind was CLAIMS. A guard banner naming a namespace nothing wrote
  to. Four probe scripts defaulting to a namespace nothing created. A
  committed NetworkPolicy admitting model traffic from that namespace, which
  read as the plane's model door and admitted nobody. Two dead validation
  paths, a dead test fixture and a dead scaffolder helper whose only caller
  had been removed. Documentation rows pointing at guides for a platform an
  operator could not install. Each one reads as support, and the cost of a
  false claim of support is paid by a reader months later who believes it.

  `scripts/check-legacy-runtime.py` now refuses all of it, on every tracked
  file, in CI and in `make docs-check`. Hard identifiers — the API group and
  its kinds, the images, the version pin, the three command spellings — are
  refused EVERYWHERE, workflows included, because a workflow is where an
  install would come back. The bare name is refused from the surfaces a
  reader takes as current, one approved line at a time in
  `scripts/legacy-runtime-allowlist.json`, because prose can refuse a runtime
  or advertise it and no pattern tells those apart. Each surviving line is
  named there with a category and a reason: the production refusals that must
  spell what an operator typed, the historical teardown sentinel a
  pre-retirement lift record decodes to, the bounded negative tests, and the
  CI tripwires' own negative controls. The historical record — this file, the
  coordination board and the dated reviews — keeps its past tense.

  The scanner fails closed on every way it could quietly stop working: an
  empty enumeration, files enumerated and none read, a count below the floor,
  an exemption that matches nothing, a rule that stops matching its own
  example or starts matching its counterexample. It cannot be disabled by
  declaring a current documentation directory historical — a named floor
  refuses exactly that. Sixteen deliberate breakages in its mutation
  specification each make it fail; four of them found real holes in its own
  self-test before they were closed.

  **Upgrading:** the plane's committed ingress allowance is gone. It admitted
  the retired runtime's namespace, which nothing creates; a migrated adopter
  is admitted by the per-namespace `kaimahi-proxy-ingress-<ns>` policy
  `kmx migrate` applies, as before. `SECRET_NAMESPACE` on the seam, spend-race
  and replica-kill probes and `CONTROL`/`PROBE_NS` on the upstream-boundary
  probe are now REQUIRED rather than defaulted — every caller already passed
  them, and a default pointing at a namespace that does not exist turned a
  missing Secret into what looked like a cluster fault. The guard banner's
  namespace list is `kaimahi, ollama, orka-system` in all four places that
  print it, pinned together by a test. `docs/tools.md`,
  `docs/tool-governance.md`, `docs/govern-your-agent.md`, `docs/isolation.md`
  and the `spikes/` conversion experiment with its workflow are deleted; git
  retains their history.

- **The front door is Orka.** `kmx quickstart` now goes kind → keyless Ollama
  → the pinned Orka release → a fixed `hello-world-agent` Provider/Agent
  bundle → a fresh Task with a readable answer. Nothing on that path installs,
  reads or waits on the legacy kagent runtime, and nothing on it uses Helm:
  the Orka runtime is a pinned, digest-checked manifest, not a chart. The
  command is also deterministic and non-interactive — the host-Ollama picker
  is gone from it, so the same command on the same machine produces the same
  cluster and fixed Agent bundle, which is what lets an unattended caller
  rerun it and compare. It does not read or replace the `local` Provider,
  even after a host-Ollama `kmx up`: it only shares that Provider's keyless
  placeholder Secret. `kmx up --step orka` still refuses to replace a
  different `local` endpoint and offers an explicit command that replaces
  the host route. Choosing your own model and authoring your own agent is
  `kmx quickstart-wizard` and `kmx agent create`.
- **A bare `kmx up` is the runtime and deploys no agent.** It now runs
  `cluster`, `ollama`, `model`, `orka` and stops. `kmx quickstart` is the
  command that ends with an agent answering; `kmx agent create` is the one
  that authors your own. The legacy `kagent`, `agent` and `tools-agent` steps
  remain individually addressable with `kmx up --step <name>` so the slices
  that retire them can each be green on their own. **There is no flag that
  puts them back into a bare run** — deliberately, so no configuration can
  keep the old default alive past its retirement.
- **`kmx up --step orka` owns the Task result identity.** It provisions the
  `orka-result-reader` ServiceAccount and its Role/RoleBinding, whose entire
  grant is `get` on `tasks.core.orka.ai` in `orka-system`. `kmx agent create`
  still only NAMES an existing account and creates no RBAC: minting an
  identity as a side effect of authoring an agent would hide a grant inside a
  command nobody reads as a grant. Orka v0.1.3 authenticates result reads but
  does not enforce Task-read RBAC, so this Role is the ceiling kmx can state,
  not one the server enforces — which is why it is kept this small.
- **`kmx quickstart` reruns by exact match.** A live Provider or Agent whose
  spec differs from the one quickstart would write is somebody's deliberate
  change — an edited endpoint, a different model, a hand-applied bundle — so
  the command now stops rather than overwrite it, and says `different
  configuration`. A half-finished run resumes: whichever of the two already
  matches is kept and the other is created. Every run creates a **fresh**
  Task; reporting an existing completed Task's answer would turn "the agent
  answered" into "the agent answered once, some time ago".
- **A Provider pointed away from the in-cluster endpoint is no longer refused
  for an unrelated Service.** `kmx orka install --model-url` only checks for
  the in-cluster `ollama` Service when the URL *is* the in-cluster default. A
  host Ollama reached over the kind gateway is the caller's own endpoint, and
  `kmx up` has already verified it is reachable from the cluster.

### Fixed

- **`kmx plane` no longer closes by telling you to run a command that does not
  exist.** Its "Next" actions and its upgrade note both named `kmx govern`,
  which was retired with the legacy runtime — worse than an unknown command,
  because a command that had just succeeded was the thing recommending it.
  Both now name `kmx migrate <deployment> --namespace <ns>`, which is the
  route this cluster can actually take. A package-wide test now fails if any
  production code suggests a retired command through `operationCommand`.

### Breaking

- **The legacy kagent installer, its manifests and its status report are
  removed.** This is the last slice of the retirement: what earlier slices
  left addressable is now gone rather than hidden.

  **Commands.** `kmx up --step kagent`, `--step agent` and `--step tools-agent`
  are no longer steps. `kmx up --step` accepts exactly `cluster`, `ollama`,
  `model` and `orka` — the same four a bare run performs — and anything else
  is refused locally as an unknown step, naming the four that exist. There is
  no flag, environment variable or payload that restores the installer.
  **Upgrading:** a script naming one of the three retired steps must drop it.
  A cluster that already runs the legacy runtime is untouched by this release
  and is operated with kubectl.

  **`kmx status` now reports the runtime kmx installs.** It was a report about
  `agents.kagent.dev`, `modelconfigs` and the kagent namespace's pods, with a
  governance envelope counted off them. Nothing in kmx installs, drives or
  deploys any of that, so the report was confidently describing objects this
  project has nothing to do with — which reads as coverage and is not. The
  command keeps its name and now delegates to the same reading as
  `kmx orka status`: running version against the kmx pin, deployments, CRDs
  and Provider readiness. It delegates the Orka portion unchanged, with no
  separate context check before its toolchain preflight. It also reports
  `kaimahi-proxy` Deployment and pod readiness/restarts and the serving seam
  certificate's subject, issuer and expiry, warning when renewal is due.
  An absent plane is distinguished from an unreadable one; these reads use
  the same pinned context. `kmx ctx` explains an incomplete local setup.
  **Upgrading:** the old kagent agent/model counts and governance envelope are
  gone; the independent plane and certificate health lines remain in the table.

  **`kmx credential issue --secret` now requires `--namespace`.** It defaulted
  to the retired runtime's namespace, so an operator who omitted the flag
  minted a one-time token into a namespace nothing here installs any more —
  and that token is shown exactly once. The namespace is now named rather than
  guessed, and a destination that is blank or does not exist is refused before
  the credential is issued and before any Secret is written, so a missing
  namespace can no longer strand a token that cannot be recovered.
  **Upgrading:** add `--namespace <ns>` to any `kmx credential issue --secret`
  invocation that relied on the default. `--discard` is unaffected, and
  `kmx migrate` already took its namespace.

  **`kmx status -o json|yaml` is removed, and refused by name.** The document
  published `governance` — model-seam, credential and plane populations —
  assembled from kagent Agents and ModelConfigs. **Nothing replaces that
  count, deliberately.** `kmx migrate` routes an owner's own Deployments, and
  those workloads have no discovery index: there is no query that lists them,
  so any document kmx published would be a tally of what it happened to be
  told about rather than of what is on the cluster. Emitting the old shape
  filled with zeros would be exactly the false zero that report always refused.
  **Upgrading:** a caller pinned to `-o json` gets an error naming `table` and
  the `kubectl` read that answers the same question. The old `governance`
  shape has no successor.

  **Helm is no longer a dependency or a download.** The legacy chart install
  was its only caller, so `kmx` no longer preflights Helm, no longer fetches a
  pinned copy onto anybody's machine, and no longer names it as a
  prerequisite. kind and kubectl are still fetched and still digest-checked.
  The archive-extraction path in the toolchain stays — no pinned tool uses it
  today, and the rule it encodes (verify what was published, then extract) is
  the durable part.

  **Embedded manifests.** `k8s/kagent-values.yaml`, `k8s/hello-world.yaml`,
  `k8s/tools-agent.yaml` and all nine `k8s/models/` presets are deleted from
  the tree and from the binary. They were kagent v1alpha2 objects and the
  commands that applied them (`kmx use`, `kmx govern`, the retired install
  steps) had already gone. `k8s/` is now thirteen files, all embedded.
  **Upgrading:** a workflow applying a preset with `kubectl` should take it
  from this repository's history, or onboard the endpoint with
  `kmx models add` and route a workload onto it with `kmx migrate`.

  **Pins and configuration.** `KAGENT_VERSION` and its `0.9.12` default are
  removed from the CLI and the Makefile, and `kmx version` now prints the Orka
  pin in place of the kagent one. `make model-secret`, which captured a preset
  key into the kagent namespace, is removed with the presets; provider
  credentials for a governed upstream are `kmx models add`, and the
  plane-side Copilot token is `kmx models credential copilot`.

  **Certificate publication.** `kmx plane` no longer publishes the seam
  authority into the `kagent` namespace on every run. No owner-managed
  workload lives at a namespace kmx can guess, so publication now belongs
  solely to the command that is *told* which namespace — `kmx migrate`, which
  is unchanged and still publishes into the workload's namespace before
  pointing it at the seam.

  **Checkers.** `scripts/check-agent-uid.py` (it read kagent Agent manifests
  out of `k8s/`), `scripts/check-seam-tls.py` (its entire subject was the
  `governed-*` presets) and `scripts/verify-chat.py` (its last caller was its
  own self-test) are deleted with their mutation specifications. The mutation
  harness now proves nine checkers with 135 deliberate breakages, and the
  repository map records the new counts.

- **The `kagent` lift payload is retired: `kmx aks up` lands Orka and nothing
  else.** `--payload kagent` is refused **by name** as retired rather than as
  an unknown value — a script that still names it asked for a platform this
  command installed until the legacy runtime went, and a typo message would
  send its author looking for a spelling instead of a replacement. The `kagent`
  and `agents` phases are gone with it, as is the in-lift preset governance
  they were the last caller of; no phase needs Helm any more. **An existing
  kagent lift is not stranded:** its run record still decodes, so
  `kmx aks down` tears the cluster down and reports what it could not prove is
  its own, exactly as before. What is refused is creating or **resuming** one
  — a resume would run phases that no longer exist — and the refusal says so
  and names the teardown command. A record written before the payload split
  carries no payload, still reads as `kagent`, and is treated the same way.
  **Upgrading:** drop the flag or pass `--payload orka`; tear down any kagent
  lift with `KAIMAHI_CONFIRM=<group> kmx aks down --resource-group <group>
  --cluster <cluster>`.

- **The legacy operational commands are gone: `kmx govern`, `kmx use` and
  `kmx agent edit` are no longer commands at all.** They drove the legacy
  kagent runtime and nothing else, and each now fails locally as an unknown
  command — before configuration is loaded and long before a cluster is
  reached. **Upgrading:** an owner-managed application is put behind the plane
  with `kmx migrate`, a credential is issued into a named destination with
  `kmx credential issue <name> --secret <secret> --namespace <ns>`, a model
  upstream is onboarded with `kmx models add`, and an Orka Agent is edited
  with `kubectl --context <context> -n <ns> edit agents.core.orka.ai <name>`
  and read back with `kmx agent show --namespace <ns> <name>`.
  The old `k8s/models/` presets are removed.

- **`kmx console` drives Orka Agents only.** The dashboard used to list
  `agents.kagent.dev` beside Orka Agents and offer chat, inference editing and
  connector creation on those rows. Every one of those operations was already
  gone by the time the row was drawn — console chat dispatches
  `--runtime kagent`, which is refused by name — so the inventory was
  advertising actions that could not run. The console now reads
  `agents.core.orka.ai` in `--namespace` and nothing else, even on a cluster
  that still serves the legacy kinds. Gone with them: the legacy ModelConfig
  connector creation, its `spec.declarative.modelConfig` patch, its
  `systemMessageFrom` prompt resolution and its dynamic-MCP tool counting,
  which had no other producer. The `--namespace` flag no longer claims a
  second fixed namespace is also read. **Upgrading:** nothing — no console
  action for those objects reached a cluster successfully.

- **`kmx agent chat` is Orka-only, and is a session.** `--runtime` accepts
  `auto` and `orka`; `--runtime kagent` is **refused by name** rather than
  quietly resolved to Orka, because a caller who named the legacy runtime
  asked for a different platform and answering from Orka would answer a
  question nobody put. Auto-detection resolves an Orka Agent or reports that
  it did not — there is nothing left to fall back to. The one-shot transport
  went with it: the kagent CLI download, the controller port-forward, the A2A
  invoke and its retry policy, the resumable `--session`, the raw `--json`
  task and the question re-sampling are all removed, and a chat without
  `--interactive` is refused with the command that works. **Upgrading:**
  `kmx agent chat --interactive [--namespace <ns>] <name>`. A script that
  parsed the raw A2A task has no replacement on this path; create a Task with
  `kmx agent create --task` and read its exact result instead.

- **`kmx agent list` reports Orka Agents only.** A bare list used to report
  the legacy runtime in its fixed namespace; it now reads `orka-system`, the
  namespace the pinned installer uses and the same default `agent chat`
  resolves against. `--namespace` selects another. The kagent
  readiness/acceptance/ModelConfig/tool-wiring columns are gone with the kind
  they described. **Upgrading:** inspect any previously installed objects with
  `kubectl --context <context> -n kagent get agents.kagent.dev`; this release
  no longer provides `kmx up --step agent`.

- **In-chat governance is gone.** `/govern`, `/ungovern`, `/sessions`,
  `/resume`, `/history`, `/new` and `/session` were kagent session and
  preset-switch controls; the Orka session's controls (`/agent`, `/tools`,
  `/lift`, the `/inference*` family, `/retry`, `/verbose-*`, `/help`,
  `/exit`) are now the whole set.

- **`internal/kmx/kagentcli` is deleted**, and with it the pinned kagent CLI
  download and its checksum verification. kmx still fetches kind and kubectl,
  both still digest-checked. Nothing on any supported path downloads a kagent
  binary any more.

  **What deliberately remains, for the slices that retire it:**
  `kmx up --step kagent|agent|tools-agent` still installs the legacy runtime
  and its two demonstration agents, the installer pins and manifests are
  untouched, and `kmx status` still reports the kagent namespace. Existing
  kagent lift records remain readable for teardown, but their retired payload
  cannot be created or resumed. **Nothing in the CLI drives those runtime
  objects**: what the retained local steps leave on a cluster is now operated
  with kubectl.

  **CI:** `kmx-clone-free` keeps its no-checkout premise, its bare `kmx up`,
  its Orka `agent create` Task with an exact expected answer read through the
  account the bring-up provisioned, its module-proxy plane build and the
  revision that plane reports. Its legacy half — the three install steps,
  `kmx govern`, the one-shot `kmx agent chat`, the ledger and budget rows that
  conversation produced, the `kmx use` switch and the backup/restore that
  needed those rows — is **removed rather than translated**: each asserted
  something about a command that no longer exists. Budget, ledger and
  backup/restore keep their coverage in `e2e-models` and `e2e-resilience`,
  against supported paths. The `kmx metrics` step stays and drops its
  `kaimahi_decisions_total` assertion, which had no children left to count.
  A hygiene check now refuses any retired command anywhere in that job.

- **The installer's `--quickstart` refuses an explicit `v0.1.0` selection.**
  That historical release runs the legacy quickstart, not Orka. A plain
  v0.1.0 install still succeeds but recommends the Orka-capable v0.2.0
  release instead. See **Upgrading** for the current install command.

- **`kmx quickstart` no longer deploys the legacy kagent runtime**, and no
  longer reduces, reconciles or preserves a kagent Helm release. Its
  structured output's `agent` key is now `hello-world-agent` (was
  `hello-world`) and `manifest` names the generated Orka bundle in
  `orka-system` rather than the embedded kagent example. Its `next` actions
  are now an Orka chat, `kmx agent create`, `kmx orka status`, `kmx plane` and
  `kmx migrate`. A caller parsing `agent` or `manifest` must be updated;
  there is no longer a legacy-runtime install step.
- **A bare `kmx up` installs the Orka runtime, not the retired kagent runtime
  or its demonstration agents.** Use `kmx quickstart` to create a fixed Agent
  and run a fresh Task, or `kmx agent create` to author your own.

- **The runtime shard is now the hosted-model shard.** `e2e-runtime` installed
  the legacy runtime with a bare `kmx up` and opened with a kagent
  conversation, a model-preset switch and an MCP tool call; the hosted-upstream
  evidence was appended to the end of it. None of that first half is a
  supported path any more, so the job is now `e2e-hosted-models` and keeps only
  the half that was never about kagent: the plane dialing a **public-looking**
  model upstream. It brings up kind, Ollama and the model with `kmx up --step`
  component steps, creates the caller's own `hosted-model-client` namespace,
  deploys the plane, and issues its one credential into that namespace **by
  name** — nothing inherits the old `kagent` default. Its governed caller is a
  direct authenticated TLS call to the plane's own model seam. It installs no
  kagent and fails closed — after bring-up and again at the end — if that
  namespace ever appears, and it is on the hygiene job's list of shards that
  must prove the legacy runtime absent.

  What it asserts is unchanged in substance: a hosted upstream vetted at boot
  with the address it resolved and the authority it verifies against both named
  in the log; an `internet: true` entry resolving *inside* the cluster refused
  at config load with both replicas still serving; the opt-in `make
  egress-hosted` allowance admitting one verified, metered Responses call whose
  ledger row carries the upstream's own token counts and its caller fields; a
  redirect surfaced as 307 rather than followed; DNS rebinding refused as a 502
  whose public body hides dialer detail while the proxy log names the policy
  refusal; and the allowance removed — on a fresh dial after a restart —
  failing closed with an audited 502. The required `e2e-hello-world` aggregate
  names the renamed shard. No product behaviour, interface or default changed
  — this is CI evidence only.

  - **Removed rather than translated:** the `kmx agent chat` turns and their
    `verify-chat.py` checks, the `k8s/models/` preset dry-run against live CRDs
    and the `kmx use` preset switch, the kagent tool-server lockdown posture
    (read-only mode logged, its ServiceAccount denied Secret reads and writes)
    and the MCP tool round-trip that required a real `k8s_get_resources` call
    carrying an unguessable probe ConfigMap name, and the two `kmx status`
    probes — the ungoverned seam count and the no-plane branch. Each asserted
    something about legacy objects, and rewriting them against surviving ones
    would have asserted less while looking the same. The honest cost: **no
    shard verifies an MCP tool round-trip on a live cluster any more**, and
    `verify-chat.py` now runs only against its own fixtures in `hygiene` — it
    is kept because the rule it encodes should outlive the runtime it was
    written for, not because anything still exercises it end to end. The
    tool-server posture and the preset mechanism went with the release that
    provided them. The `kmx status` rules keep their unit coverage in
    `internal/kmx/app/governance_test.go`, and status is rebuilt on defensible
    evidence in its own change.

- **Model-seam evidence now comes from direct authenticated calls, not kagent.**
  The `e2e-models` shard used to install the legacy runtime with a bare
  `kmx up`, repoint a kagent Agent at the seam with `kmx govern`, and produce
  its ledger rows through `kmx agent chat`. It now brings up kind, Ollama and
  the model with `kmx up --step` component steps, creates the model client's
  own namespace, and issues every credential (`model-ci`, `race-cred`,
  `house-agent`) into that namespace **by name** — no command there inherits a
  destination. Every governed turn is a direct TLS call to the plane's own
  model seam under that credential, which is also the only honest way to
  exercise a protocol the committed upstreams do not speak: an agent's OpenAI
  client sends one shape and retries a 429 on its own. The shard installs no
  kagent and fails closed — after bring-up, after the first credential is
  issued, and again at the end — if the legacy `kagent` namespace ever appears.

  What it asserts is unchanged in substance: a metered `free` ollama row
  carrying the upstream's own token counts and its caller fields, eight
  concurrent calls against a one-token cap admitting exactly one across both
  replicas, a cap denial that relays no answer and files no approval request,
  ordinary recovery, the ops-port metrics, and model onboarding end to end —
  dry-run, overlay precedence and stale-apply refusal, refused overlay custody
  of the admin bearer, Responses-API metering, the onboarded endpoint
  reachable only by the proxy, an unmeterable answer refused rather than
  relayed, a protocol contradicting its own path refused at load, and the
  entry surviving the next `kmx plane`. No product behaviour and no default
  changed — this is CI evidence only.

  - **Removed rather than translated:** the shard's combined `kmx status`
    assertions and its raw MCP inventory check, which counted legacy objects,
    and `scripts/ci/status-unknown-probe.sh`, which minted a reader for kagent
    CRDs to prove the cannot-tell branch of `kmx status`. Rewriting either
    against surviving objects would have asserted less while looking the same.
    What is lost with the probe is the real-cluster half: that a genuinely
    RBAC-denied reader reaches that branch at all. The rule it rendered still
    has unit coverage — `TestCredentialsAreUnknownWhenTheSecretsCouldNotBeListed`,
    `TestGovernanceCannotTellIsNotNotInstalled` and
    `TestUnknownPopulationsPublishNoCounts` keep `unknown` distinct from a
    zero, carry kubectl's own reason, and publish no counts — and status is
    rebuilt on defensible evidence in its own change.

- **Spend, expiry and budget evidence now comes from an owner-managed
  application too.** The `e2e-spend` shard used to install the legacy runtime,
  repoint a kagent Agent with `kmx govern` and produce every ledger row through
  `kmx agent chat`. It now runs the same supported path `e2e-resilience` does:
  component bring-up, the pinned Orka, an ungoverned `owner-ci` Deployment in
  its own namespace, the plane, [`kmx migrate`](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/migrate.md), and a patch
  the **owner** applies. It installs no kagent and fails closed — after
  bring-up and again at the end — if that namespace ever appears. This removes
  the last PR-time live `kmx govern` check while the command still ships;
  until its retirement in #218, only `kmx-clone-free` live-tests it (on main
  pushes or manual dispatch), so pull requests no longer catch regressions in
  that command.

  What it asserts, kept apart so a single green tick cannot hide which boundary
  moved: this migration's NetworkPolicy admits only the owner's namespace on
  TCP 8080 and does not admit tool port 8081; other policies can admit other
  traffic. A real model turn writes an
  `unpriced` Orka ledger row with the upstream's own token counts, attributed
  to `none` — a complete answer, and a different word from the `unknown` the
  shard fails closed on; an expired credential earns a 403 that names the
  credential and the command that fixes it, and renewal restores service while
  leaving the mounted Secret's uid, resourceVersion and bytes, and the pod
  holding them, untouched; a credential with no expiry at all still
  authenticates; and an exhausted token budget is a 429 the application itself
  reports, with service restored when the cap is lifted.

  No product behaviour and no default changed — this is CI evidence only.

  - `internal/kmx/admin/ledger_format_test.go` pins the shard's ledger
    expressions against the real renderer, including the two anchored on the
    last column: a `grep` that stops matching fails the shard, but a negative
    assertion that stops matching passes it.

- **Governance evidence now comes from an owner-managed application, not kagent.**
  The `e2e-resilience` shard used to install the legacy runtime, repoint a
  kagent Agent at the seam with `kmx govern`, and produce every ledger row
  through `kmx agent chat`. It now proves the path
  [`kmx migrate`](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/migrate.md) actually supports: a Deployment its own
  owner runs (`owner-ci`, in its own namespace, created before the plane
  exists), repointed by the patch `kmx migrate` writes and the **owner**
  applies. The shard installs no kagent and fails closed — after bring-up and
  again at the end — if that namespace ever appears.

  What it asserts, kept apart so a single green tick cannot hide which
  boundary moved: the migration leaves the owner's Deployment byte-identical
  (uid, generation and whole spec) until the owner applies the patch; a real
  model turn through the TLS seam to Orka returns an answer and writes an
  `unpriced` Orka ledger row with the upstream's own token counts; an
  exhausted token budget is reported by the application itself as a 429 and
  audited as a denied row; and the plane survives a replica killed mid-call —
  the in-flight call drained, the survivor answering 200, exactly two ledger
  rows gained, and both replicas back to 2/2 ready — and a Postgres outage,
  where every replica's readiness drops and returns and no replica's restart
  count changes across it. It separately proves the owner's application
  answers again after a simultaneous restart of both replicas and after a
  backup/wipe/restore.

  It does **not** claim native Orka Agent governance. The pinned Orka Provider
  schema has no field naming a private certificate authority, so an Orka Agent
  cannot be told to trust the plane's seam; the governed caller here is the
  owner's own application. No product behaviour and no default changed — this
  is CI evidence only.

  - `scripts/replica-kill-probe.sh` takes `CLIENT_PATH` (default
    `v1/chat/completions`) so it can speak the `v1/responses` client path the
    committed `orka` upstream declares, and refuses a protocol it has no body
    for rather than posting a mismatched one.
  - `scripts/ci/owner-model-client.{sh,py}` are the owner-managed workload and
    its standard-library model client; `scripts/test_owner_model_client.py`
    pins the properties the shard's conclusions depend on — the credential
    leaves by no route but the bearer header, an upstream refusal keeps its
    status, and the seam's authority is verified with no unverified fallback.

### Added

- **Orka now has its own required pull-request proof.** A new `e2e-orka-runtime`
  CI shard brings up kind, Ollama and the model with component steps only,
  installs the pinned Orka and its keyless Provider, and requires a
  Provider → Agent → Task round trip to return an exact local-model answer. It
  then applies the committed native Orka Kubernetes Tool and proves that
  boundary directly: an allowed ConfigMap listing that contains a ConfigMap
  created seconds earlier, and refusal of Secret reads and pod mutation at both
  the tool's own validation and the cluster's RBAC. The shard installs no kagent
  and creates no Helm release, and fails closed if it ever does. The required
  `e2e-hello-world` aggregate depends on it, so it gates merges.

  It does not claim that the local model chose to call the tool — small-model
  tool selection is a known CI flake class and is left unasserted rather than
  asserted flakily — and it claims nothing about governance: Orka traffic is not
  on the plane seam there. No product default changed.

### Changed

- **The lift's Azure commands go through the same runner as every other
  subprocess.** `liftDiscovery` and `liftAzureWrite` called `os/exec` directly,
  so they inherited the process environment but ignored `Runner.Env` and
  `Runner.Unset`: a caller that removed a variable was not obeyed, and the only
  way to substitute `az` in a test was to shadow `PATH`. They now prepare through
  the runner and then take their own deadline, which is the shape `orkaCapture`
  already uses for kubectl. Deadlines (30s discovery, 10 minutes for writes),
  the 4 MiB output bound and the fixed failure messages are unchanged, and
  Azure's own stderr is still dropped rather than propagated because it names
  subscriptions and resources. These calls are still not echoed: they run
  underneath a loading pane.

### Fixed

- **`kmx agent` no longer reports an agent you just created as missing.**
  `agent create` and `agent show` operate on Orka; interactive `agent chat`
  supports Orka and kagent, while one-shot chat and `agent edit` remain
  kagent-specific. A one-shot chat with an Orka Agent reported the operator's
  own agent as absent and then offered a different agent as the alternative.
  It now points at the interactive Orka command that works.

  - `kmx agent list --namespace <ns>` lists Orka Agents. Without a namespace it
    lists the legacy runtime, as before. An agent created by `kmx agent create`
    could previously not be listed by `kmx agent list`, and `kmx agent show`
    pointed at a `--namespace` flag that did not exist. It exists now, so that
    advice is executable.
  - One-shot `kmx agent chat <orka-agent> ...` names the Agent's Orka namespace
    and prints the working `--interactive --runtime orka` command. It no longer
    offers an unrelated kagent agent as a substitute.
  - `kmx agent chat` on a cluster with no kagent runtime installed says the
    runtime is absent, instead of reporting an unreadable cluster.
  - `kmx agent edit` identifies an Orka bundle before opening an editor and
    prints the corresponding `kubectl edit` and `agent show` commands, rather
    than presenting the existing kagent schema refusal without context.

  No behaviour changes for kagent agents, and no flags were removed.

### Added

- **Podman is now selectable without knowing the `CONTAINER_ENGINE`
  environment variable.** `kmx --container-engine podman quickstart` and
  `kmx quickstart --container-engine podman` both choose Podman before the App
  and its subprocess environment are created. kmx supplies kind's required
  `KIND_EXPERIMENTAL_PROVIDER=podman` automatically; the environment variable
  remains supported for automation, and an explicit flag overrides it. There
  is deliberately no daemon-based fallback: Docker and Podman have separate
  kind inventories and can each own a same-named cluster, so silently switching
  on a transient daemon failure could act on the wrong cluster.

- **`kmx lift --payload orka` lands Orka on the managed cluster it provisions.**
  The lift's cluster half is unchanged — resource group, private registry, an
  AKS cluster with a policy engine, the proven network boundary, the plane and
  Azure monitoring — but what runs agents is now a choice. `orka` installs the
  same pinned Orka that `kmx orka install` puts on a local cluster, so the
  Agent and Provider YAML authored locally is the YAML that runs there.
  `kagent` keeps the legacy runtime and its two demo agents on governed
  Copilot. The `orka` payload deliberately creates **no Provider**: a managed
  cluster has no in-cluster model server and kmx holds no credential for a
  hosted one, so the phase installs the platform and names the step that is
  the operator's ([docs/aks.md](docs/aks.md)).

### Breaking

- **`kmx lift` now requires `--payload`, and has no default.** An existing
  invocation fails with a refusal naming both choices; add `--payload kagent`
  to keep exactly what it did before, or `--payload orka` for the current
  direction. There is no default because this command bills money and installs
  a platform: defaulting would mean a script quietly changing which platform it
  deploys the day the project's direction moved. `--step` is validated against
  the phases the chosen payload actually has, so `--step agents` is refused on
  an `orka` lift and names the phases it does have.


### Added

- **`kmx orka install` installs Orka, from a cluster with nothing on it and
  with no API key anywhere.** Orka assumes a cluster, publishes no CLI binary
  and no GitHub Release, and asks an operator to create a shared bearer token
  by hand before applying its manifest — an install that skips that step comes
  up and never becomes ready. One command now fetches their `deploy/orka.yaml`
  at a pinned tag, **refuses bytes that do not hash to the digest kmx pins**,
  creates that Secret first and never replaces an existing one, applies their
  installer unmodified, waits for both Deployments, and creates a keyless
  `Provider` at the in-cluster model server so their fourth prerequisite is not
  one. `kmx orka status` reports the pinned version, both Deployments, the CRD
  count and the Providers — saying "none — a model call would be refused"
  rather than leaving an empty column, and reporting an unreadable cluster as
  unread rather than as absent. The bytes stay theirs: the manifest is fetched,
  never vendored, and nothing in Orka is modified. **Installing governs
  nothing, and the command says so** — `kmx migrate` is what puts an
  application's model traffic on the seam ([docs/orka.md](docs/orka.md)).
  `kmx orka status` reports the version **running**, read off the controller's
  own image rather than restated from the pin, and names the disagreement when
  a cluster was installed another way. `--no-apply` fetches and verifies the
  installer without writing anything; `--dry-run` asks the API server whether
  it would accept it, which is the only way to find a Pod Security policy or a
  missing admission-policy API before the installer is half-applied.

- **`kmx migrate` puts an application you did not write onto Orka, with
  its model traffic governed and without changing the application.** One
  command reads what the workload reads today (including variables that
  arrive through `envFrom` a ConfigMap), refuses a model Orka has no
  ready `Provider` for before it writes anything, creates the identity
  the seam presents to Orka and the seam's allowance for your namespace,
  mints both credentials into Secrets through a pipe, and writes the four
  environment variables and one mounted file as a patch it deliberately
  **does not apply** — it creates objects it owns in a namespace it was
  named, and does not mutate a Deployment this project does not own.
  Afterwards the application's model calls are authenticated (it holds a
  `kmh_` credential for the plane and no model key at all), Provider-scoped
  (it names a model, never a URL) and recorded, refusals included. Two new
  committed model upstreams reach the same Orka endpoint: `orka` sends
  `X-Orka-Tools: disabled`, which is the only way off Orka's server-side
  coordinator loop and a header an application configured by environment
  variables cannot send; `orka-coordinator` does not, so what that default
  costs can be read off the ledger instead of argued about. See
  [docs/migrate.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/migrate.md).

- **The model seam accepts the Responses API on an endpoint that serves
  chat completions.** An upstream may now declare `client_path` — the one
  path a client may POST — beside `path`, the one forwarded on, and the
  plane translates the request and the answer in both directions. Only
  that pairing is implemented and every other combination is refused when
  the config loads. A field the translation cannot honour is a `400`
  naming the field rather than a field dropped in transit, a streamed
  request on such an upstream is refused rather than half-translated, and
  metering is untouched: token counts are still read out of the
  endpoint's own body under the endpoint's own protocol, so a translation
  bug can produce a wrong answer but not a wrong row.

- **The model seam speaks the OpenAI Responses API, and a call it cannot
  meter is refused rather than counted as zero.** An upstream now
  declares a `protocol` — `chat_completions` (`usage.prompt_tokens` /
  `usage.completion_tokens`) or `responses` (`usage.input_tokens` /
  `usage.output_tokens`) — and the meter reads the fields that protocol
  names. Both are OpenAI-compatible surfaces, which is why "point it at
  an OpenAI-compatible endpoint" was never sufficient: one current agent
  framework's model client *is* the Responses client and offers no
  switch, and against a table that knew only the other shape its calls
  were first refused on the path and then, once a path was added,
  **ledgered `0 in / 0 out`** — a token budget over that upstream could
  never have been exhausted. The field is optional only where the path
  already names it, so every existing table keeps working unedited; a
  declaration that disagrees with its own path, or one this plane cannot
  meter, is refused at load rather than resolved. A **success carrying no
  usage the declared protocol can read is now refused (502) and the
  answer is discarded**, with a ledger row whose new `cost_source` is
  `unmetered` — the one case that cannot be refused is a stream already
  flushed, which is relayed, logged at ERROR and ledgered `unmetered` all
  the same. Migration `00012`
  ([docs/spend.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/spend.md#protocols-and-missing-usage)).

- **`kmx models add` — onboard your own model endpoint.** The model seam
  had no onboarding path at all: the only route edited
  `k8s/plane/upstreams.yaml`, which the next `kmx plane` re-applies and
  discards. It now works the way the tool seam already did — the operator
  overlay ConfigMap accepts an `upstreams` block, under the same custody
  rule (`credential_file`, `credential_header`, `internet`, `ca_file`,
  `extra_headers` are refused by the plane, so an overlay entry is
  in-cluster and keyless) plus one: `prices` is refused too, because a
  price is what a cents budget is measured with and is the one number in
  the table the plane cannot check. One `--url` carrying the path a
  client posts to becomes the entry's base URL, its single forwarded
  path and its protocol; `--classification` is required and has no
  default. Three reviewable documents, not the tool seam's four: a model
  has no `RemoteMCPServer` equivalent that a runtime without kagent could
  apply, so the seam's base URL is printed instead. The command also
  states what an operator arriving from `kmx tools add` would otherwise
  assume — **the model seam has no allowlist**, so a new upstream is
  reachable by every credential the plane has issued, bounded only by
  their budgets. Admin contract 2
  ([docs/kmx.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/kmx.md#kmx-models-add),
  [docs/spend.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/spend.md#adding-a-model-upstream)).

- **The audit row says who called.** Nothing in a governed row
  distinguished an agent the plane deployed from a shell script holding
  the same token — not the client name in the MCP handshake, which the
  gateway relays without reading, not the user agent, not the source
  address. That invisibility is why an overclaim in the attribution
  column went unnoticed for months. The spend ledger and the tool audit
  now carry two columns, kept apart because they are worth different
  amounts: `caller (claimed)`, the client's own `User-Agent` recorded as
  `ua:<name>` — self-reported, unverified, and named so it can never be
  misread as something the plane checked — and `from (observed)`, the
  peer address the plane saw at its own socket. Both appear in
  `make ledger`, `make tool-audit`, `kmx ledger`, `kmx audit tool` and
  `kmx flow`. **This decides nothing**: no call is admitted, refused,
  attributed or priced differently because of it. `acted_for` and its
  vocabulary are unchanged. Migration `00011`; rows written before it
  say `legacy`, which means *no record of who called* and is a different
  word from `none`, *the caller offered no name*
  ([docs/identity.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/identity.md#who-called)).

- **The known imprecision in `acted for` is now written down where a
  reader of the trail meets it.** For a client the plane did not deploy,
  `none` — "there is no person" — claims more than the plane can know,
  because absence-of-run is read as an operator-driven turn. That is
  accepted rather than fixed, on the grounds that no supported
  configuration reaches it: kagent and the inbound bridge are the only
  doors. It is bounded and reversible, and
  [docs/identity.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/identity.md#correlation-is-a-window-not-caller-identity)
  says plainly that if a foreign runtime becomes supported the position
  is void.
- **The plane's two data seams serve TLS.** The model seam (8080) carries the
  full text of what an agent was asked and what it answered; the tool seam
  (8081) carries the body a tool returned. Neither is written to any artifact
  the plane keeps — the ledger records identifiers, token counts, cost and
  status, and the tool audit records the decision plus a summary of *declared*
  argument fields capped at 240 bytes. That content therefore exists in no
  other record, which is what encrypting the hop buys.

  `kmx plane` mints the material and there is no new component: a certificate
  authority created once in Secret `kaimahi-plane-authority` (its private key
  mounted into no pod at all), a serving certificate in
  `kaimahi-plane-seam-tls` mounted into the proxy, and the authority's
  certificate alone in `kaimahi-plane-ca` in the agent namespace, which
  `ModelConfig.spec.tls` and `RemoteMCPServer.spec.tls` name. kagent's
  controller mounts that Secret into the agent pods it generates, so no
  pod-spec edit is needed. Verification is never disabled: chain *and*
  hostname are checked, on the agent's model calls, on kagent's own tool
  discovery, and on the plane's two loopback self-calls.

  The admin and ops ports are unchanged — they are on no Service — and the
  inbound bridge's one public route still terminates TLS at an edge.
- **The seam certificate's expiry is reported before it bites**, in three
  places: a `kmx status` line naming subject, issuer and days remaining; the
  `kaimahi_seam_certificate_expires_in_seconds` gauge on the ops port; and the
  proxy's startup log. `kmx plane` re-signs inside the last 30 days of a
  398-day life, and `kmx plane --step certificate` does it on demand. Renewal
  re-signs under the *unchanged* authority, so nothing on the agent side has
  to be redistributed and there is no window where one side has rolled over
  and the other has not.
- **`scripts/check-seam-tls.py`** — kagent admits both of the mistakes this
  change could make, and refuses neither. An `https://` seam URL with no
  `spec.tls` fails every call against a trust store that has never heard of
  the plane; a `spec.tls` block beside an `http://` baseUrl is accepted,
  inert, and reads as configured while the seam is plaintext. The checker
  covers both halves, plus `disableVerify`, and proves it can still catch each
  one before its verdict on the tree is trusted.

- **`kmx migrate` measured on AKS, with the application deployed BEFORE
  Orka.** [docs/migrate.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/migrate.md) §9 is the same migration on a
  managed cluster — `Standard_D8s_v5`, Cilium, Kubernetes 1.35.7 — against
  the same Orka `v0.1.3` and the same application, exercising four phases of
  `kmx lift` on the way, with wall clock per step, the ledger's rows, the
  cost of a second run, a table saying which differences are Orka's, which
  are ours and which are just AKS, and a proved teardown (≈ US$0.35,
  computed from published rates). The first live run of the observability
  phase on any cluster: it works, and Managed Prometheus is shown scraping
  the plane rather than merely configured to.

  **Corrected**, because it was true only of kind: the seam token's lifetime
  is the cluster's decision, not ours — AKS granted **24 hours** against the
  same 720 h request kind granted in full, so a migration stops working
  after a day there unless re-run.

  **Newly found and recorded**, not previously known on either cluster: a
  tool-calling turn through the translating seam **fails part of the time**
  on `previous_response_id`, measured at 5 failures in 11 identical turns,
  with the mechanism traced line by line and the refusal shown to be
  correct — whether kind was also affected is left open rather than assumed.
  And four defects in `kmx lift`, all confirmed on the cluster and none
  fixed here, including a plane image that reports `version="unknown"`
  because the lift's `az acr build` passes no `--build-arg VERSION`, which
  no test asserts.


### Fixed

- **A committed extra header on the model seam can no longer displace the
  credential the proxy injects.** The model seam sets `extra_headers`
  *after* the credential — the opposite of the gateway's ordering — and
  had no load-time check for a header naming the credential slot, while
  the gateway's copy of that check has been there since it was written.
  A table that carried one is now refused at load, as the gateway's
  already was.

- **`kmx lift`'s observability phase could not run at all.** The check
  for what a cluster already had built a `kubectl` command with no verb
  in it — `kubectl … -n kaimahi networkpolicy <name>` — which kubectl
  reads as an attempt to run a plugin and refuses outright. It is
  upstream of everything else in the phase and of the only line that
  records the prior state, so no run could pass it and no resumed run
  could skip it: **every** lift, on a cluster it created and on one it
  did not, failed to wire Azure-managed monitoring. It failed closed, so
  nothing was created or deleted wrongly; there was simply no dashboard.
  Two tests now stand where nothing did: one drives the check through a
  `kubectl` that refuses what the real one refuses, and one reads the
  whole package's source and fails any `kubectl` call that reaches a noun
  where a verb belongs — or that hands the command line off to a caller,
  which is what hid this one.

- **A tool name could forge a line in the audit table.** `tool_audit`'s
  `tool` and `method` come out of caller-controlled JSON and were
  recorded verbatim, and every renderer prints them unescaped into a
  fixed-width table. A client holding a governed credential could put a
  newline in a `tools/call` name and produce what read as a separate
  audit row. **The upstream name is the same hole and looks safer than it
  is**: it is a URL path segment, Go's mux unescapes path values, and the
  unknown-upstream refusal is audited before any table lookup — so
  `/upstream/x%0A…/mcp` reached a row without naming a real upstream.
  The same held for the approvals trail, where a denied call files a
  request carrying the tool's name.
  Every free-text audit column on all four trails — spend, tool,
  approvals, inbound — is now bounded at the write, and a value that is
  not already one clean printable line is stored in Go quoted form.
  **Quoted rather than stripped**, because stripping creates a collision
  that is worse than the mess it tidies: mapping `openai\t` to a space
  and padding it into a fixed-width column renders exactly like the real
  `openai`, so the trail would show what reads as a denial against an
  upstream nobody asked for. Both audit renderers apply a one-line rule
  again on the way out, for rows they did not write today. Real tool and
  upstream names are untouched — no quoting, no escaping, no change.

- Contribution status validation catches duplicate or contradictory completion
  records and incorrect merged pull-request references, helping contributors
  avoid repeating work that has already shipped.

- **`scripts/check-repository-map.py`** — `docs/repository-map.md` asserted
  several dozen facts about this tree and nothing checked any of them. CI now
  runs the map against the tree on every pull request: the file counts, the
  paths, the membership lists, the callers, and — the assertion that catches
  the most — that every tracked file under `cmd/`, `internal/`, `k8s/`,
  `scripts/`, `docs/`, `brand/` and the repository root is named by exactly
  one list in the map. Adding a file fails the check until somebody has said
  what it is.

  Nothing here checks the *classification*. Whether a manifest is product or
  demonstration is a judgement, and a checker enforcing one would freeze an
  opinion the tree is allowed to change; what is enforced is that a
  classification exists. The three cases the map calls genuinely unclear stay
  a standing question by design — the check requires the section to survive,
  to say how many cases it holds, and to name paths that are still there.

  Every list is derived rather than copied: the embedded manifests come out of
  `embed.go`'s own `go:embed` patterns, the manifests that must not ride along
  out of the Go test that names them, every count out of `git ls-files`.

### Changed

- **The plane's scrape job is a `PodMonitor`, and the cluster-wide scrape
  ConfigMap is no longer touched.** `ama-metrics-prometheus-config` in
  `kube-system` is singular: every custom scrape job on the cluster shares
  that one document. Writing it meant either overwriting jobs this project
  did not make or stopping to ask an operator to merge ours by hand, and
  removing it at teardown meant deleting jobs it never made. The job is now
  a `PodMonitor` (`azmonitoring.coreos.com/v1`) named `kaimahi-plane` in the
  `kaimahi` namespace — a namespaced object owned by whoever created it —
  so an adopter adds their own pods by writing their own `PodMonitor` in
  their own namespace, touching nothing of ours. `kmx lift` neither reads,
  writes nor deletes that ConfigMap, and a test asserts it. **Nothing about
  the boundary changed**: the ops port is still on no Service, custom
  resources are still scraped by the same `ama-metrics` replica pods, and
  the one NetworkPolicy allowance is unchanged. On a cluster whose metrics
  add-on has no `PodMonitor` CRD the phase carries on and prints the job in
  ConfigMap form for you to merge by hand; `verify` then reports that the
  metrics half is not arriving. Teardown deletes the `PodMonitor` only if
  this run actually applied it — not because the prior-state read, which
  happens before the add-on installs the CRD, once said none was there.
  **Upgrading:** nothing. A lift run by an earlier build never reached this
  step — the verb defect below blocked the observability phase in every build
  that shipped it — so there is no ConfigMap of ours on any cluster to clean
  up.
- **Teardown names the scrape jobs the add-on will take with it.** The
  `PodMonitor` kind belongs to Azure's metrics add-on, so disabling the
  add-on removes the custom resource definition and every object of that
  kind on the cluster, whoever wrote them — measured on a live cluster, not
  inferred. `kmx lift down --byo` cannot avoid that (turning off an add-on
  this run turned on is what teardown is for), but it no longer does it
  quietly: it lists the PodMonitors it did not create, before it acts.
- **The lift says what its dashboard does not cover.** The view is what
  crossed the governance plane — allowed, refused, approved, spent. It is
  not what happened inside an agent: no spans, no per-step timings. The
  next-steps output and `docs/aks.md` now say so, and say that
  OpenTelemetry is the answer to that half and is neither replaced nor
  conflicted with.


- **`kmx plane` has a fourth step, `certificate`**, between `secrets` and
  `deploy`. `make plane-certificate` delegates to it on every target.
- **The seam URLs this repository writes are `https`**, in the two governed
  ModelConfig presets, the six committed RemoteMCPServers, the scaffolder, and
  the manifests `kmx tools add` and `kmx agent create --image` generate. A BYO
  agent (`--image`) also gets the authority mounted and `SSL_CERT_FILE` set,
  because kagent's controller mounts nothing for a BYO pod.
- Interactive chat now opens with a compact agent/context/model/tools view on
  capable terminals. `/help` provides grouped commands on demand; turn spacing,
  exit reasons, connection/wait feedback, and static native decision callouts
  make conversation state distinct from operational state. Plain transcripts
  retain the status report, and raw one-shot chat remains unchanged.
- `kmx` now renders destination-aware rich status, agent, and admin reports,
  actions, and guard callouts. Redirected admin columns/truncation and structured
  artifacts remain compatible. `NO_COLOR` removes ANSI while retaining static
  rich layout; `TERM=dumb` selects plain presentation.

- **The prerequisite list is one item: a container engine.** It was five (Go,
  Docker or Podman, kind, kubectl, Helm) plus make and curl. Go is now needed
  only by the two commands that build the plane's image: `kmx plane`, and
  then only when it is run from outside a checkout, and `kmx lift`, which
  demands it whenever its plane phase runs.
- Measured on a clean machine (no tooling, no checkout), time from one command
  to an agent's answer: **246s → 178s**. Against the same measurement of `kmx
  up` from this branch's parent, 217s → 178s; the first-answer kagent profile
  is 33s where the full one is 64s.
- `kmx` now uses one Cobra command tree for nested commands, flags,
  command-specific help, validation, and Bash/Zsh/Fish completion. Operational
  behavior, context guards, Make delegation, and machine-readable stdout remain
  in the existing application layer.
- Documentation and code comments describe behavior, constraints and rationale
  directly, while retaining useful links to capability documentation.
- **`kmx agent create` and the blueprint parser refuse a wider set of
  credential shapes**, and both now read one shared list rather than each
  keeping its own — so a shape added in one place is refused in all of them,
  including the repository-wide scan CI runs on every change. Neither refuses
  less than it did before. The list gained Slack's app-level token, which is
  not an `xox` shape and so was covered nowhere, and an Azure DevOps personal
  access token.

### Fixed

- **`kmx quickstart` is safe after `kmx up`, not only safe after itself.** It
  now inspects the existing kagent Helm release before mutating it, marks and
  reconciles only the reduced first-answer profile it owns, and preserves every
  unmarked, full, or custom installation. A
  failed or malformed Helm read is an error rather than permission to replace
  unknown state. A valid empty release list is the only state that permits a
  minimal `helm install`; failed, pending, non-deployed, and concurrently
  created releases are not overwritten. Explicit status flags support Helm 3
  and 4. New quickstart installs and full-profile `up` now wait for the
  workloads and jobs in their own Helm release instead of `kubectl wait pods
  --all` snapshotting unrelated or deliberately deleted pods in the namespace.
  Interactive quickstart output also shows its six-step plan and clearer phase
  boundaries; redirected and JSON output are unchanged.

- Contribution status validation now fails closed on malformed references and
  checks that retired records cite the same merged pull request as their status
  entries. Completed work is no longer presented as available to start.
  Self-tests use independent fixtures and remain valid with no open findings,
  including checks that resolved findings disappear and unrecorded ones remain.
- Chat aborts enhanced input on resize without submitting the current message or
  approval. Session list shapes/empty states, active-renderer history boundaries,
  and duplicate tool-event display are corrected. Broad one-shot transport
  retries remain unchanged and can repeat effects after ambiguous disconnects.
- `workflow show` returns typed binding errors for invalid values or unknown
  keys, including mixed missing/invalid input; missing-only exploratory help
  still succeeds. Successful show formatting is unchanged.

- **CLI audit safety semantics:** unknown conditions/read failures no longer
  imply absence or readiness; flow counts model refusals from `cost_source`.
  Quickstart requires a completed answer and retains its JSON key set:
  `governed: false` means this invocation did not enable governance, not that
  existing cluster governance is absent. Plain reports intentionally reflect
  these corrections too.
- **Mutation and recovery safety:** target/option-preserving, shell-quoted
  remediation; kind/context mismatch refusals; bounds and Secret wiring checks
  before issuance; tool-only ungovern patches; matching workflow request/grant/
  audit digests without claiming downstream completion. Backup uses unique 0600
  temporary files; restore reports recovery failures; lift confirms before any
  deletion and retains records for incomplete cleanup without blanket billing
  or telemetry claims.
- **Chat format and decision safety:** `--interactive --json` is refused before
  application loading. Wrapped/native prompts, grapheme editing, transient
  clearing, terminal restoration, and session retention on stream failure are
  corrected. Native HITL fails closed on incomplete/oversized requests and
  incomplete batch decisions; question answers preserve free text and validate
  offered choices. Scanner fallback remains supported.

- **Eight claims in `docs/repository-map.md` were wrong on the day it merged**,
  found by writing the checker above. `scripts/` holds 67 tracked files and
  not 65; there are ten `check-*` scripts and not nine, so the checker bucket
  is 13 and the summary table's scaffolding column was 40 where the tree said
  42; ten mutation specifications and not nine, and ten checkers the mutation
  harness proves rather than nine; twelve files under `scripts/` name a
  `k8s/` path and not eleven. Two sentences were also unfalsifiable as
  written and are now precise: `docs/reviews/` is referenced once *outside the
  map* rather than once in the repository, and `verify-chat.py`'s occurrences
  in Go are comments except one, which is a test's own failure message.

- **`kmx credential capture <upstream> <repository|organization>`** — the last
  step of the journey that still needed a checkout. Installing kmx, standing a
  cluster up, deploying the plane and governing an agent all work with no
  clone; handing the thing a token was `make release-secret` /
  `make ado-secret` / `make github-secret`, which are make targets in a
  repository. The credential is now typed at a prompt, checked against the
  upstream, and written straight into the Secret the gateway reads.

  **This is the one path on which kmx accepts credential material, and it is
  fenced.** The value is read from a terminal or not at all: there is no flag,
  no environment variable, no file, and a pipe or a redirect is refused rather
  than read, because a credential that can arrive through a pipe can arrive
  from a shell history or a CI log. The terminal's echo is off while it is
  typed. It travels to exactly two places: the upstream, in an
  `Authorization` header, because a capture that stored an unchecked value
  would be worse than the make target it replaced; and the Secret. Not argv
  (`--from-literal` would put it in the process table, so the Secret is
  rendered in memory and piped to `kubectl apply -f -`), not a temporary file,
  not a log, not this command's own output — and the buffers holding it are
  cleared when the write returns. The context guard
  runs first, so the cluster about to hold the credential is named before
  anything is typed. Everything else in kmx keeps refusing credential
  material: the agent wizard still screens its input against credential
  shapes, and a blueprint carrying a credential-shaped key is still refused
  before the document is decoded.

  The checks the shell scripts made are kept, and so is their honesty about
  what cannot be checked. GitHub: refused unless the token is fine-grained,
  reads the repository named, announces no OAuth scopes, and GitHub's answer
  is about that repository; the expiry is REPORTED, and whether the token
  reaches only one repository is not proven at all, because GitHub exposes no
  endpoint that says so. Azure DevOps: refused unless it is an access token
  with an expiry claim that has not passed and the hosted server accepts it on
  a real handshake; the audience is REPORTED, because Entra writes it two ways
  for the same request and refusing on it refused correct tokens. A capture
  that is refused stores nothing.

  An upstream that already holds a credential is not overwritten silently:
  the capture stops and names `--replace`.

- **`kmx workflow` and blueprints** — one declarative file says what a
  governed workflow reaches, which of its calls need no human, which need one,
  and in what order; `kmx workflow govern` applies the governance and
  `kmx workflow run` executes the steps. A blueprint NAMES seams that already
  exist in the plane's table and asserts the `policy_fields` it depends on —
  it cannot create a hosted or keyed upstream, because the overlay refuses
  the custody fields that would make one — and it carries no credential in
  any form. kmx carries `blueprints/release.yaml`, which reproduces the
  release agent's governance exactly: the same tool allowlist `make
  release-allow` sets and the same standing constraints
  `scripts/release-bind.sh` writes, proven by a test that runs those and
  diffs the result. Steps can be conditional (`when: <parameter>`), so one
  blueprint covers a release that builds on GitHub Actions, on Azure DevOps,
  or on both; `kmx workflow show` and `kmx workflow run` describe the same
  run for the same `--set`, and a guard on a parameter that carries a default
  is refused rather than silently always-on. See
  [docs/workflows.md](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/workflows.md).
- **The release agent** — the first thing in this repository that is not a
  demonstration. One command reads what merged since the last release,
  drafts the notes, and proposes the release branch and the builds. Cutting
  the branch and publishing are denied by default, filed naming the version
  and the repository, approved by a person, and admitted under a grant
  welded to that call — approving "cut release/v1.2.3" cannot be spent on
  the next one. Build dispatch runs under a standing constraint bounded to
  named pipelines instead, because a human approving every build is a human
  who stops reading. It reaches two hosted seams: a write-scoped GitHub
  credential, and Microsoft's hosted Azure DevOps server, which is
  authenticated by Microsoft Entra rather than by a key. The agent never
  carries a byte — the workflows and pipelines it dispatches do the
  building. The one exception is the final publish, where no CI system is
  in both networks: the DECISION is governed like every other consequential
  call, but the transfer runs on the operator's machine under their own
  credentials, which is weaker than the rest of the path and is written
  down rather than glossed. See
  [docs/release-agent.md](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/release-agent.md).

- **`kmx lift`** — the same agent you have been running locally, on AKS, in
  one command. It creates the cluster (or acts on one you already have with
  `--byo`, which it never creates, deletes or adopts), builds the plane's
  image in a private registry, renders the manifest for it, deploys the
  plane and the agents, and wires Azure-managed Prometheus, Container
  Insights and a workbook. `--plan` prints what it would create and stops;
  `--step` runs one phase, so a run that stopped can be resumed where it
  stopped rather than from the beginning. `kmx lift down` removes what the
  lift created — and on a cluster you brought, only that.

  Two refusals worth knowing before you plan around them. **A cluster with
  no NetworkPolicy engine is refused on both paths**, before the boundary
  phase writes anything: AKS accepts every NetworkPolicy on such a cluster
  and enforces none, so the boundary would be present and inert, which
  reads as protection and is worse than having none. On a cluster the lift
  creates, the creation itself already refused anything but an enforcing
  engine; on one you bring, the control plane is asked and an unreadable
  answer is refused too, rather than assumed either way. An engine being
  present is still not enforcement, so the boundary phase then runs the
  existing negative probe against the live boundary. **The second refusal
  is `--byo`-only**: a cluster whose identity cannot pull from the registry
  is refused with the `az aks update --attach-acr` for its owner to run,
  because granting that role means writing a role assignment on your
  subscription, which a demo has no business doing quietly. And the model credential is yours: a managed cluster runs a
  hosted model, a provider token is not one of the three upstream
  credentials `kmx credential capture` can check a value against, and the
  lift stops and names `make plane-copilot-secret` rather than storing
  something it cannot vet. That is the one step on the managed path that
  still needs a checkout.

- **`kmx tools add <name>`** — point Kaimahi at an MCP server this
  repository did not write. It reads the server's own Service to derive the
  pod selector and the resolved container port, scaffolds four reviewable
  documents (the gateway's table entry as an overlay fragment, the proxy's
  egress to that server, that server's ingress from the proxy alone, and
  the `RemoteMCPServer` whose URL is the gateway), sends the candidate
  table to the running plane to be parsed by the same code the proxy booted
  with, and applies it behind the context guard. A tool named without a
  `policy_fields` declaration is refused rather than defaulted, and the
  weakest declaration announces itself in the file. The committed table is
  never edited: onboarded upstreams live in a separate ConfigMap merged
  over it at boot, and an overlay that would redefine a committed entry is
  refused rather than resolved by precedence.

- **`kmx flow [credential]`** — the ledger, the tool audit, the approvals
  trail and the inbound trail merged into one timeline, so "what happened
  in that run" is one command instead of four reads and a mental join.

- **`kmx tools sandbox`** — installs a WASM runtime class for tool
  execution, and `kmx tools sandbox status` reports whether it is installed
  and what uses it. Read the banner before running it: this writes a
  containerd shim onto every Linux node with a privileged, hostPID,
  host-root-mounted DaemonSet, which nothing else in Kaimahi asks for.

- **Bring-your-own agents.** `kmx agent create --image` scaffolds an agent
  that runs your container serving A2A on :8080 instead of a declarative
  one, carrying the governed seams across as environment (the proxy's
  base URL, the gateway's endpoint, the credential reference) where the
  cluster has a governance plane — and saying plainly that it cannot verify
  your image honours them; the ledger is where that becomes proven.
  `--isolation virtual-node` places it on an ACI virtual node, and is
  refused without `--image`, because a declarative agent cannot be
  VM-isolated. `--run-as-user` states the UID the image runs as, or `root`
  to say it needs one, rather than kmx guessing a UID that is not yours.

- **`kmx agent chat --interactive`** keeps one streamed session open
  instead of one question per process, `--session` resumes a kagent session
  by id. Separately, one-shot `--json` forces the raw A2A task at a terminal.
  One-shot terminal chat prints the reply, tools and token cost; piped one-shot
  output remains the raw task byte for byte. Interactive chat is a human
  transcript, including when its input uses the scanner fallback.

- **`kmx status` counts how much of the system is actually governed** —
  model seams, tool seams and credentials, as "1 of 2 governed, 1 direct",
  read from the cluster objects with no plane, credential or internet
  needed. It never invents a zero: a count it could not take reads
  `unknown`, which is a different word from `none`. `-o json|yaml` gives
  the same document for automation.

- **The plane reports its version, and kmx refuses to guess across a
  gap.** `/admin/version` returns the plane's version and an integer admin
  contract; kmx checks it once per session and refuses **per operation** —
  a read that the older plane can still serve is served, and only the
  operation that needs the newer contract is refused, naming both versions.
  A plane too old to answer at all is named as such rather than assumed
  current.

- **`kmx quickstart`** — one command from a machine that has a container
  engine to an agent that has answered a question. It runs `kmx up`'s steps in
  `kmx up`'s order, but preserves existing deployed kagent releases. On a fresh
  install it defers everything a first question cannot reach: kagent's console,
  bundled tool server, MCP controller, second agent and governance plane.
  `--output json` retains the keys `ok`, `context`, `cluster`, `agent`, `manifest`,
  `question`, `answer`, `governed`, `tools`, `elapsed_seconds`, and `next` for
  automation; `governed: false` is invocation-scoped, not a cluster assertion.
- **`install.sh`** — `curl -fsSL .../install.sh | sh` downloads the release
  binary for the platform, verifies its published sha256 before installing it,
  and puts it in `~/.local/bin` without sudo. `--quickstart` carries on into
  `kmx quickstart`, so one command ends with a working agent.
- **kmx fetches kind, kubectl and Helm** when the machine does not have them,
  pinned and checksum-verified into `~/.config/kmx`, exactly as it has always
  fetched the pinned kagent CLI — the digest is re-verified on every use, not
  only at download. A copy already on PATH is always preferred and never
  shadowed. `KMX_TOOLCHAIN=off` restores the previous behaviour, where a
  missing tool is an error naming its install page.


- **A stale `bin/kmx` could apply the previous version of twelve embedded
  files.** The manifests, blueprints and scripts kmx carries are inside the
  binary, so editing one has to relink it — but the Makefile's list of those
  prerequisites had drifted twelve files behind `embed.go`. Editing the WASM
  runtime, the release blueprint, any observability manifest or any of the
  five scripts the managed path ships left a binary built before the edit,
  which `make sandbox`, `make lift` or a blueprint run then applied. CI never
  saw it, because a fresh runner builds once. The test guarding this asserted
  two hard-coded filenames and stayed green throughout; it now derives the
  list from `embed.go`'s own directives, so the two cannot drift again.
  Affects checkouts only — an installed kmx was never stale.

- **`make ap-ask` downloaded a kagent CLI it did not use.** It delegated to
  `make chat`, which is kmx, which fetches its own pinned copy. The
  prerequisite is gone, and the comment that explained the duplicate away —
  it claimed a checkout hands kmx the make-side binary — has been corrected
  to say what actually happens.

- **`kmx` no longer acts silently on a cluster nobody chose.** Context
  resolution used to fall through to `kind-kaimahi-p1`, label the result as
  coming from a `KIND_CLUSTER` variable that did not exist, and never print
  where the choice came from. It cost a real cluster: `kmx down` announced
  "context not created yet" and then deleted one that existed. Two things
  changed. The banner now names the **source** of the context it chose, on
  every command. And the guard **refuses** the specific case that caused
  that loss: a context reached only by falling through to the default, on a
  kubeconfig that has contexts, whose current-context is something else.
  The fallback itself is still there and still right for the fresh-machine
  case — an empty kubeconfig gets `kind-kaimahi-p1`, which is what `kmx up`
  is about to create — so this is a narrowed refusal plus an honest label,
  not the removal of a default.
- **A stale `Accepted` condition is reported as `unknown`, not as a pass.**
  A Kubernetes condition records when a verdict last *changed*, not when it
  was last checked, so after a credential is replaced the old pass stands
  until kagent looks again — measured at 49 seconds on a live cluster, and
  forever if the new credential is good, because nothing changed. kmx waits
  90 seconds for a verdict reached after the write. That wait can confirm a
  REJECTION and can never confirm a pass, which is the right way round: the
  case worth blocking on is the broken credential, and a timeout is
  reported as `unknown` rather than as success.
- **The clone-free path waits the Go module proxy out instead of failing.**
  A freshly pushed revision is not immediately servable by proxy.golang.org,
  and `kmx plane` on a just-merged commit failed rather than retrying.
- **The agent pods are hardened like the rest of the tree.** The pods that
  actually execute model output were the only workload setting no security
  context at all, while the proxy and the fixture ERP set `runAsNonRoot`,
  no privilege escalation, dropped capabilities and a read-only root
  filesystem. Five of the six committed agents and the `kmx` scaffolder now
  carry the same posture, so an agent an adopter creates is hardened too —
  the scaffolder is the half that matters, because without it every agent
  an adopter creates is unhardened. `k8s/release-agent.yaml` landed after
  this change with no security context at all — the agent holding the
  write-scoped GitHub credential — and now carries the same posture as the
  other five. Two
  things this took a cluster to learn are written into the manifests:
  `runAsNonRoot` alone fails when the image names its user by name rather
  than by number (kagent's image says `python`, so the numeric `1001` has
  to be stated alongside it), and a read-only root filesystem needs
  somewhere to write, which is an `emptyDir` on `/tmp`.

- A one-shot `kmx agent chat` no longer reports nothing when the agent asks a
  question instead of answering. kagent's runtime gives every agent a built-in
  `ask_user` tool that no manifest declares and none can remove; a small model
  occasionally calls it, and the task then ends `input-required` with an empty
  reply that nothing in a script can answer. kmx now re-asks — at most twice,
  only when the agent did nothing but ask, never in a resumed session, and
  saying so on stderr every time. A pending human APPROVAL is the same
  `input-required` state and is never re-asked: that decision is a person's.
  Neither case can become a success — `scripts/verify-chat.py` still fails
  closed on both, and now names which one it was.
- `kmx workflow govern` no longer writes the standing-bounds fragment and
  restarts the proxy before discovering that the credential its governance is
  written for does not exist. The credential is checked first, and a missing
  one is refused with the command that creates it rather than
  `tool-allow failed (HTTP 404): no such credential` after the cluster has
  already been changed.
- The documented by-hand install used `sha256sum --ignore-missing`, a GNU
  coreutils flag that macOS (no `sha256sum`) and BusyBox (Alpine, slim images)
  both reject — so the verification step failed on two of the platforms the
  release publishes for. `docs/releases.md` now shows a portable comparison,
  and `install.sh` uses whichever of `sha256sum`, `shasum` or `openssl` exists.
- `kmx workflow run --dry-run` no longer writes to the cluster. It printed
  "Nothing was created" and then re-minted every seam credential the workflow
  declares a `refresh:` for, applying a Secret into plane custody — a turn step
  names no upstream, so the refresh ran on the FIRST step of any run, including
  one whose opening step only reads and drafts. A dry run now rides whatever
  credential is already in custody.

  **What an operator gets in exchange for that:** a dry run whose stored
  credential has since expired will fail where it previously refreshed itself,
  and the symptom is the agent reporting the seam's tools missing from its
  toolset rather than anything naming a credential. So the run now says, before
  it starts, which seams it is deliberately not refreshing and what an expired
  one will look like. A live run is unchanged and still refreshes.

- **`kmx down` checks cluster existence independently of the kubeconfig.**
  `kind delete cluster` deletes by container name, so a missing context does
  not prove the cluster is absent. Teardown checks the container engine first:
  a failed check is an error, and a confirmed absent cluster deletes nothing.
  A confirmed local kind cluster keeps the existing banner-only path;
  an unreadable kubeconfig refuses teardown. Other cases identify the
  uncertainty and require
  `KAIMAHI_CONFIRM=<context>` or a typed context confirmation, refusing deletion
  without a matching confirmation. This also supports cleanup after `kmx up`
  fails before writing its kubeconfig entry. Deletion destroys all cluster
  data; export anything needed first.
- **`kmx workflow run` and `kmx credential renew` go through the context
  guard.** The documentation said every mutating command did; these two did
  not. `kmx workflow run` is the one that matters: it files approval
  requests, drives turns that cut branches and dispatch builds, and executes
  whatever an `ungoverned:` step names, and it could do all of that on a
  cluster nobody had looked at. It is guarded **once**, before the
  port-forward and before the first request is filed, because every
  consequential step already stops for a human to approve the exact call —
  and each of those now names the cluster next to the call, so the one
  banner scrolling away does not take the target with it.
- **Every agent manifest's `runAsUser` is checked against the image kagent
  ships.** Five manifests said the number was "pinned by a CI assertion".
  There was no such assertion. There is now
  (`scripts/check-agent-uid.py`): it resolves the agent image from the
  pinned chart — this repository's values file layered over the chart's
  defaults, so a moved registry moves what is tested — reads the uid by
  running `id -u` inside it, and fails if any manifest disagrees, names no
  uid at all, or if it found no agent manifests to check. Keyless: the
  chart and the image are both public.

### Breaking

- **The two data seams no longer answer plain HTTP.** A `ModelConfig` or
  `RemoteMCPServer` written before this release still names `http://` and
  carries no certificate authority, so its calls fail closed from the moment
  `kmx plane` finishes until the seam is re-applied. Nothing degrades to
  plaintext — the listener answers "Client sent an HTTP request to an HTTPS
  server" — which is the right direction, but it is a real gap and it is not
  self-healing. See **Upgrading** below.

  A non-kagent client of either seam is affected the same way and needs the
  authority's certificate; `docs/foreign-runtime.md` says how to read it out,
  and why skipping verification instead is worse than the plaintext it
  replaces.

- **`kmx status -o json` is no longer a `kubectl apply` document.** The
  top-level kubectl `apiVersion` and `kind` are gone, because the output is
  now kmx's own envelope — `context`, `contextSource`, a `governance` block,
  and the kubectl objects verbatim under `items`. Anything piping this into
  `kubectl apply` breaks; `jq '.items[]'` is unchanged.

- **A credential can no longer be piped into the capture.** `az account
  get-access-token … | make ado-secret ADO_ORG=<organization>` used to work
  and now refuses: the value is read from a terminal only. Run the `az`
  command, then paste its output at the prompt. The release driver's automatic
  refresh is unaffected — it mints and stores the token without a human and
  never goes through this path. `make github-secret`, `make release-secret`
  and `make ado-secret` still exist and run the new command; re-capturing an
  upstream that already has a credential now needs `--replace`.

- Subcommand `--help` now succeeds and prints command-specific help instead of
  the former inconsistent `flag.FlagSet` error path. Unknown and extra
  arguments use Cobra’s standard errors rather than being ignored by a few
  commands. Bare command groups such as `kmx agent` now print their hierarchical
  help and exit successfully instead of returning a one-line legacy usage error.

### Upgrading

Install matching CLI and plane revisions, and back up the plane before rollout.
For installations with the old gateway/tool or inbound integrations, first
follow the [gateway retirement review](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/operations.md#upgrading-after-gateway-retirement):
stop or replace owner-managed tool integrations and review their configuration,
network access and Secrets. The retired tool listener has no supported
reapplication command; `kmx migrate` handles model traffic only. Do not expect
`kmx plane` to restore tool calls.

```sh
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.2.0
kmx backup plane-before-upgrade.sql
kmx plane
kmx status
```

The checksum-verified `install.sh` also installs v0.2.0 by default once it is
the latest published tag. Verify every plane replica runs the new build before
declaring retirement effective: old replicas can still consume stored grants,
and rollback to an approval-capable build can reactivate historical grants.
Existing credentials and ledger rows survive, but the retired APIs do not.
See [the plane retirement upgrade procedure](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/operations.md#upgrading-after-approval-retirement).

Scripts using `kmx up --step kagent|agent|tools-agent`, `kmx govern`, `kmx use`
or `kmx agent edit` must move to the supported Orka and model-seam commands:
`kmx up` (runtime only), `kmx quickstart` (fixed Agent and fresh Task),
`kmx agent create --namespace <ns>` (own Agent), `kmx migrate <deployment>
--namespace <ns> --model <provider>/<model>` (owner model traffic), and
`kubectl --context <context> -n <ns> edit agents.core.orka.ai <name>`
(Agent edits). Add `--namespace <ns>` to `kmx credential issue --secret` calls. Use the
`kmx status` table instead of its retired `-o json|yaml` output, with targeted
`kubectl --context <context>` reads where machine-readable output is needed.
Interactive Orka chat is `kmx agent chat --interactive [--namespace <ns>] <name>`;
there is no legacy session or raw JSON chat replacement.

### Known issues

- Orka v0.1.3 refuses the quickstart Kubernetes tool's in-cluster Service URL
  (`tool URL resolves to private/loopback IP`), so the tool never becomes
  Available to Orka-executed Tasks. Tracked in [#217](https://github.com/kaimahi-agents/kaimahi/issues/217).

## v0.1.0 — 2026-09-03

The first tagged release: everything the project has built since the first
hello-world agent, at a version you can name. Most of what is below is
release plumbing — the product can now be installed and upgraded without a
commit hash — plus the last capability to land before the tag was cut.

### Added

- **An agent's calls record who it acted for, and credentials expire.** A run
  opened at the inbound door (where a Slack signature has already proved who
  typed the message) is what joins a governed call to a person, so the ledger
  and the tool audit carry an actor the plane itself observed — never a claim
  made by the thing being governed. Every credential issued from now on has a
  deadline (default 30 days, no way to ask for "never"), enforced at the LLM
  proxy, the MCP gateway and the inbound door, failing closed and audited.
  `make credentials` shows what is expiring; `kmx credential renew` moves the
  date without touching the token, so custody is unchanged
  ([docs/identity.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/identity.md)).
- **Tagged releases, built by CI from the tag.** `kmx` binaries for
  linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64, with a
  `checksums.txt` published beside them. The job refuses to publish a build
  that does not report its own tag, and refuses a tag with no entry in this
  file.
- **`go install …/cmd/kmx@latest`** as the install line — no sha, no
  package-manager namespace claimed, and no new tooling to trust: the module
  proxy and the Go checksum database already stand behind it.
- **`kmx version` reports the build's own version**, not only the versions it
  installs. A release binary reports its tag; a `go install` reports the
  version you asked for; a checkout build says it is a checkout build.
- **A released binary deploys the plane at its tag.** `kmx plane` used to
  depend entirely on VCS stamping surviving the build; the tag is now the
  first source it reads, and the release refuses to publish unless the
  plane module's matching `plane/vX.Y.Z` tag exists at the same commit.
- **A documented upgrade path** ([docs/releases.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/releases.md)),
  including what happens when a migration fails halfway (the plane does not
  start), and a CI job that upgrades a plane across a real schema gap with
  live data in it and proves the data survives.

### Upgrading

- From an untagged `go install …@<sha>` build: install `@latest` and run
  `kmx version`. There is no state in `kmx` itself to migrate.
- For the **plane**, see [docs/releases.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/releases.md#upgrading-the-plane).
  Migrations are additive and run at startup under a lock. Two behaviours are
  worth knowing before you upgrade, and both follow the same rule — an
  upgrade never silently widens or voids what an operator already had:
  - past `00008`: tool grants minted before argument binding existed keep
    their old verb-level meaning, and no new one of that kind can be created.
  - past `00010`: credentials that already exist keep a NULL expiry and keep
    working. Expiring a running estate at migration time would be an outage,
    not a control. `kaimahi_credentials_without_expiry` is the gauge for
    shrinking that set; re-issue or renew at your own pace.

### Not in this release

- **No container image is published.** `kmx plane` still builds the plane's
  image locally from the Go module proxy at kmx's own revision, so Go remains
  a prerequisite for the governed half even if you installed a binary. The
  reasoning is in [docs/releases.md](https://github.com/kaimahi-agents/kaimahi/blob/df3ead4a6a4d6eee9d1112895daab48c29600150/docs/releases.md#why-no-published-image-yet).
- **No package-manager namespace is claimed** — no Homebrew tap, no npm, no
  crates, no PyPI. The name is provisional and no trademark opinion has been
  obtained; claiming namespaces would raise the cost of a rename that may
  still happen. See [docs/NAMING.md](docs/NAMING.md).
