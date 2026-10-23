# Repository guidance

Read [CONTRIBUTING.md](CONTRIBUTING.md), the
[development invariants](docs/development.md#invariants-to-preserve), and the
[repository map](docs/repository-map.md) before changing command behavior.
Edit the map only when adding, removing or moving a top-level directory (a root
area or an immediate child of `cmd/`, `internal/` or `pkg/`); put behavior
changes in the package's Go doc comment.

## Command consistency

- `model list [-o table|json]` defaults to config-free bounded local discovery.
  Explicit `--provider copilot` lists available Chat-compatible routes without
  starting sign-in. `model login` uses GitHub device flow; `--gh` explicitly
  copies a bounded private gh token into kmx's OS keychain. Runtime credentials
  prefer explicit `COPILOT_GITHUB_TOKEN`, then kmx's keychain item; only an
  unavailable keychain permits bounded `gh auth token --hostname github.com`
  capture without persistence. Invalid explicit tokens, missing owned items and
  service refusals do not switch credentials. No token file fallback; exchange
  bearers stay in memory. Login explains runtime options if the keychain is
  unavailable. Logout clears only kmx's item/memory, not gh or environment auth.
  `internal/kmx/modelaccess/copilot` returns a selected-route RoundTripper for
  direct JSON/SSE Chat Completions, not a proxy or CLI-chat replacement.
  File CLI Run/Test consume it with explicit `--provider copilot`; unbound MCP
  defaults non-fake authored models to Copilot when no startup route is selected.
  Both use the exact authored model and existing available credentials. Explicit
  routes, authored fake and legacy root-bound MCP behavior remain unchanged.
  Default only the pinned Copilot endpoint; refuse other endpoints before credential access.
  Never inject tokens in the runner. Keep known Copilot errors static and safe,
  stop later sends on them (including judging), and preserve cancellation.
  Establish auth through the catalog; exchange only after credential refusal,
  never subscription/policy/quota refusal. Never replay inference or substitute
  a model. Keep modelaccess independent of App and Unix-only helpers. Foundry
  endpoint comes only from runtime status; no fixed port, catalog download,
  service start or silent model selection. Native model-access package tests
  remain separate from Windows CLI qualification.

- `internal/kmx/fileaccess` is a package-independent checked filesystem
  foundation: existing-object reads, security/identity inspection, private first
  creation, process-held locks and no-replace file/directory-flush publication.
  Its handles stay opaque; it repairs/adopts nothing, retries no ambiguous effect
  and remains unwired from operation adapters. `internal/kmx/config` alone uses
  it for explicit creating/non-creating state-root resolution; ordinary
  `StateDir` remains location-only. Native Windows package tests qualify these
  local-NTFS primitives only, not the guarded CLI, native arm64 execution, an
  operation, release artifact or user support. App/OCI/workspace adapters remain
  inert until later phases.

- Follow the [command conventions](docs/command-conventions.md), the implemented
  interface in [docs/kmx.md](docs/kmx.md) and the
  [command alignment record](docs/command-semantics.md). Track outstanding
  renames and decisions in [#301](https://github.com/kaimahi-agents/kaimahi/issues/301).
  Proposed spellings are not implemented capabilities.
- Canonical context commands are `kmx context show` and
  `kmx context use <context>`; canonical local setup/teardown commands are
  `kmx local up/down`. Root `up`, `down`, the AKS `lift` alias and `lift down`
  are removed outright, not compatibility routes or retirement stubs. The old
  context-switching `ctx` alias remains removed; new `kmx ctx [--check]` only
  lists named workspace destinations, never selects or remembers one. Current
  deployment is digest `kmx deploy`; cloud and runtime setup
  remain `kmx aks up/down` and `kmx orka install/status`.
  The AKS group is a hidden compatibility route, not a public root domain.
  Guided quickstart is `kmx quickstart --interactive` (`-i`); the old wizard
  command is a retirement stub. `kmx chat` is always interactive and no
  longer accepts `--interactive`. `kmx targets` is the read-only capability view.
  Plane administration, model overlays, migration and their demo are removed;
  do not restore or regroup their commands.
- Named destination selectors live in versioned workspace-root `kmx.yaml`, not
  source `workspace.yaml` or published content. `test/deploy --to NAME` resolves
  concrete context/namespace/runtime expectation/inference-or-bindings/registry
  values once and passes those copies onward; never reread config during an
  operation. Explicit flags override saved fields. `evalTarget` is evaluation-only;
  Deploy never guesses a destination. Explicit-reference Deploy without `--to`
  remains workspace-independent. Runtime stays package-owned. `ctx` is an
  offline list; optional checks are local context-name/anonymous registry reads,
  not runtime/inference/Secret probes. `targets` remains runtime capabilities.
  Reference Deploy still requires a digest and evidence. No-reference `deploy --to NAME`
  composes missing Orka evaluation/publication/Deploy from one frozen workspace and
  selector plan. Require saved publishRepository and destination evaluationTarget;
  automatic disclosed promotion, one scoped confirmation, four default stage lines
  plus compact non-pass evaluation details, with verbose/JSON stage diagnostics. Exact-version private completion observations authorize
  GET-only skips, never Apply/reuse/adoption/repair. Keep kagent out of composed
  Deploy until its candidate-scoped placement and ownership-safe cleanup are qualified.
  AgentSessions workspaces and --harness-image also refuse composed Deploy before
  evaluation or publication; their Deploy is the explicit-reference plan-only render.
- Create-only deployment stamps only newly created primaries with versioned non-secret
  `kaimahi.dev/lift-record` JSON: exact consumed canonical receipt identity,
  source/destination, actual gate, attempt time and expected specs. Actor is omitted
  unless known. Exact Orka reuse and composed skips remain no-write; never repair
  or infer missing companion provenance. Separate bounded private `release-lifts`
  intent/outcome records retain canonical receipts and acknowledged partial effects;
  observations alone grant no evaluation, ownership, skip or replay authority.
  Complete new Orka creates additionally retain acknowledged versioned target
  ownership/dependency proof tied to a shared release record; only that new proof
  authorizes opt-in public Orka update/rollback through the private backend. Older or mixed placements never
  enroll. Keep provenance out
  of plan digests, suite ownership markers, portable schemas and candidate eval.
  Root destination status reads these annotations without private-state enrichment;
  it never infers missing fields from companions or local history. See
  [cluster-visible Deploy provenance](docs/suite-workspace.md#cluster-visible-lift-provenance).
- Everyday operations are root `create/chat/run`; live inspection is `orka show/list`.
  Legacy `bundle` operations are removed outright without aliases/stubs.
  `kmx agent` is removed entirely, without an alias or retirement stub.
  Root `status --to NAME [--workspace DIR] [-o table|json]` is a separate GET-only
  destination inventory, not runtime setup or a suite-source observer. Require a
  saved namespace and freeze concrete selectors once; explicit global context wins.
  Preserve readable partial rows, candidate-root segregation and the distinct
  `provenance not recorded`, `unknown`, `not kmx-owned` states. No local history,
  Secrets, admission, repair, Tasks or tool provisioning. Exit1 unknown/unhealthy/
  drift overrides2 pending;0 means complete ready/empty. Original UID continuity
  remains unverified. `orka status` retains the table-only runtime report; bare
  root status refuses with both usage hints. See [destination status](docs/destination-status.md).
- Root `create <name>` authors one tool-free native suite member offline
  by default: Orka unless `--runtime kagent` selects exact v0.10.3 Go/linux-amd64.
  `--runtime agentsessions` separately authors a built-in chat
  profile pinned to ca85f47a2ec17ca6e16029cfa69419cfe29442b3 (engine chat), with
  empty required images and complete ordered cases. Prepare/load/ordinary push
  support it; root test requires explicit --development-host FILE for literal
  loopback, unauthenticated operator-observed chat. It loads no cluster settings,
  sends exact config once per fresh Session, requires a complete five-record
  qualified journal and full ordered pass, then exports private runtime-specific
  proof. Exact-S --evaluation push accepts that proof offline. Cluster selectors,
  mixed-runtime proof, tools, chat/status and live create refuse.
  AgentSessions eval also requires --bindings FILE: a closed AgentSessionsBinding
  (runtime package; target, substrate placement, atespace, sandbox, snapshot,
  required cpu/memory resources, workerSelector, model name/provider/baseURL and
  a typed credential reference: secretRef or host-owned hostRef only). The
  suite pins the model; the binding's model.name must equal it exactly (eval and
  deploy refuse a mismatch; a new model is a new suite version and evaluation).
  Evidence records the binding's JCS digest once for the whole run set, including
  judged and repeated runs; the host endpoint is not attested.
  AgentSessions passing receipts are v2 (kmx.agentsessions-suite.passing/v2),
  which records bindingDigest. v1 receipts are unreadable and need fresh
  evaluation; a managed v1 proof is ineligible (latest refuses, no fallback).
  Root deploy with --harness-image IMAGE@sha256 --bindings FILE --evidence --plan
  renders a substrate ActorTemplate for substrate main at the commit pinned in
  render.go (SubstrateAPICommit; check with scripts/validate-agentsessions-render.sh)
  (<suite>-<member>-<hash8> over renderer version, full image reference and
  binding digest, harnessnode chat, no model credentials) and a substrate
  HarnessRegistration offline. It loads no cluster settings and applies nothing;
  apply and --update refuse. Substrate apply needs its own seam, not the kubectl
  release loop.
  Operator declarations are not authenticated host/build attestation; cleartext
  journal retention is disclosed. Session replay cannot create candidate proof.
  Optional create --runtime-version must match the AgentSessions pin above;
  --kagent-runtime refuses.
  Runtime is member-owned KMX-private metadata, not a public AgentSuite field.
  Root test supports the packaged kagent profile with author-owned --bindings;
  evaluated push consumes matching runtime-specific full-passing suite evidence.
  Legacy status readers and retirement remain unavailable; digest deploy uses the common release
  seam with runtime-matching evidence and author-owned kagent bindings. Kagent
  deploy is create-only, refusing occupied primaries/children even with matching
  evaluated UIDs; evidence never crosses runtimes.
  Private runtime-specific kagent prepare/place/observe helpers are separate from
  W250's common release-lift/dispatch/evidence seam. They create new ModelConfig
  and Agent objects or reuse a complete caller-retained observation read-only;
  occupied unrecorded/changed targets refuse, never adopt/update/rollback.
  Observations are in-memory placement proofs, not persisted/evaluation receipts.
  Placement observes the literal auth mode without native sends; evaluation
  requires unsecure before admission and each send, trusted-proxy refused.
  Orka-only consumers refuse that profile. RemoteMCP/Python remain unsupported.
  Missing runtime in old source stays Orka only. Create and `push [--workspace DIR] REF` share nearest-ancestor
  `workspace.yaml` discovery; `--workspace DIR` selects an exact root without
  fallback and create initializes a missing marker there without workspace confirmation.
  Without a workspace, terminal create asks `Start one here? [Y/n]`; nonterminal
  or hidden-diagnostics use refuses with a `--workspace` hint. Invalid nearest markers
  fail closed. CLI create prompts for missing exact model and instructions-file inputs
  only with visible stdin/stderr terminals; nonterminal use requires both flags.
  Explicit blank flags refuse, paths are literal, and no model is inferred.
  Core authoring still requires complete validated inputs; a second member refuses
  without changing authored files. Help/completion is inert.
  Workspace push names its absolute root and prepared OCI manifest digest on
  stderr, then copies that exact graph and authored cases.
  `suite push DIRECTORY REFERENCE` remains extracted-directory transfer without
  discovery and no longer accepts `--workspace`. The native member image
  requirement is empty. Neither offline command evaluates, deploys, builds images
  or loads cluster settings. Workspace evaluation is root `test`; deploy-only packaged deployment is root
  `deploy <ref>@<digest>` with explicit private `--evidence`; no-reference workspace
  `deploy --to NAME` separately composes the missing stages.
  User `agent.yaml` readers/writers/receipts, `bundle` commands,
  `create --live` including previews/wizard, live-only flags, root run bundle
  mode and `--bundles` flags are removed outright. Unknown routes/flags refuse
  before config. Old Orka/kagent/AgentSessions bundle receipts are unreadable;
  no migration or saved-file/resource deletion. Legacy status readers and retirement remain
  unavailable. Quickstart uses native source → full eval → exact local publication
  → reviewed Deploy Apply/Verify, then a separate fresh answer Task. Console Ready
  is placement only. Chat `/lift` and console Deploy print only
  `Deploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …`.
  No release GUI/live-copy fallback. Wizard tools/host modes and live tools/inference
  edits are retired; host Foundry/Copilot chat controls remain independent.
  Preserve public AgentSuite/ToolProvider implementation-bundle graph/build/pull,
  extracted-directory transfer and alpha `pkg/kmx` interfaces. PortableAgent typed
  scaffolding used by native preparation is not a persistent old reader.
  Root `run --agent` and supported Orka chat target live Agents, including
  suite-owned Agents, by context/namespace/name. Neither is workspace-local
  execution or fresh suite evaluation, nor authority to adopt, update, copy or
  retire resources. Preserve the native suite ownership fences below.
  Private packaged-native preparation and ownership-safe placement feed full-workspace
  evaluation. The existing Orka CI shard configures three cases against the pinned
  runtime/model: two text cases and one open-ended llmJudge case using the same
  AIKit workload through an owned keyless loopback forward. Private metadata
  independently checks judge identity/configuration and both scores. Job/case/outer
  timeouts are unchanged; the 660s outer budget bounds three sequential cases,
  not three complete 5m waits. Hermetic runner tests alone are not live judge proof.
  Internal PrepareReleaseLift plans an exact digest from a registry or read-only
  OCI layout, with bounded ingestion before validation and no source fingerprint.
  It requires explicit private full-passing native evidence, default exact observed
  subject equality and GET-only absent-or-receipt-bound owned-resource inspection.
  Internal explicit promotion requires identical packaged identity/runtime and a
  Ready openai Provider declaring the same model; destination resources must be
  absent. Source evidence remains distinct, not destination evaluation or signer
  authorization. Planning makes no admission, writes, Tasks or placement records;
  summaries contain identities and cannot mutate retained inputs. Kagent uses
  --bindings, never --inference, and derives namespace from the anchored bindings.
  Explicit promotion projects only approved physical identities/refs/installation
  fields; package/member/model/profile/provider kind/AuthMode and the full
  controller plus Go image digest/platform inventories remain exact. Unknown
  fields compare by default. Go workload image proof is observed after creation;
  mismatch leaves partial resources with no successful placement authority.
  No deploy Tasks/model calls, fake Provider or destination evaluation is implied.
  ApplyReleaseLift is guarded and single-use, rechecks artifact/evidence/target/owned
  proof, and in plain mode only conditionally creates or exactly reuses after both
  create preflights. Partial outcomes retain acknowledged UIDs and readiness
  separately; no adoption, automatic retry, rollback or cleanup.
  Explicit public --update is Orka-only and uses the private updater with shared
  reservation and acknowledged new ownership/target-dependency proof. Arrange a
  quiet window; no draining or zero-downtime guarantee. Fresh per-patch
  identity/spec/markers and exact prepared inputs remain mandatory; only
  between-revision inference-Provider RV churn may pass unchanged authoritative-field
  reproof. Rollback consumes original passing evidence without re-evaluation.
  VerifyReleaseLiftPlacement rechecks an opaque
  success before caller invocation. Created/recreated UIDs require fresh evaluation
  before later receipt-bound reuse. Root deploy supports registry/local --from-layout,
  --plan and text/JSON, with explicit selectors or named --to and private --evidence; --promote
  discloses source-only evidence. Runtime/model are packaged, never overridden.
  Help/completion is inert. General suite transfer is unchanged.
  Native inference is openai-type Provider only, not host Foundry/Copilot execution.
  Private native passing receipts retain only full ordered passing metadata, export
  once without replacement outside workspace/cache trees, and bind the private
  receipt-file push consumer to exact S plus offline packaged coherence. There is
  a full-workspace native evaluation CLI with one fresh never-retried Task per
  declared run of each ordered case. Optional KMX YAML runs/minPasses defaults
  to one run/all passes, bounded to 100 runs plus the offline receipt budget.
  Each run receives its own case-timeout budget, including judging/attribution;
  repeat preflight states the summed run-time budget excluding placement.
  Any unknown vetoes the threshold. Only
  completed attributable assertion failures are countable; execution errors
  still abort. Orka abort reports count executed runs from returned nonempty
  TaskUIDs, not assertion completion; total remains planned. Root test's local
  -o text|json defaults to text for every runtime; JSON is Orka-only and emits
  one payload-free outcome document with diagnostics on stderr. Composed Deploy
  adds the same run-local counts, omitting them for GET-only cached skips.
  AgentSessions development evaluation shares this repeat policy.
  Private repeated rows retain ordinal/policy and actual outcomes; default
  one-run encoding stays unchanged. Field names/defaults/validation follow
  [AgentSuite §8.2.2](https://github.com/agent-primitives/suite/blob/main/spec.md#822-case-documents-and-assertions):
  minPasses requires runs and both are positive integers. KMX additionally caps
  runs at 100 under the spec's consumer resource-limit allowance.
  Root push requires latest completed private exact-S evidence by default;
  `--evaluation FILE` explicitly overrides lookup without fallback, and
  `--unevaluated` explicitly retains ordinary publication. Explicit REPO:TAG
  remains unchanged; bare push requires saved publishRepository and derives only
  its tag as `s-<full-S-hex>`. Root test makes --out optional and registers managed
  proof only after evaluation, Orka cleanup, final input checks and evaluator/output
  closes. Managed 0700/0600 per-workspace/per-S immutable completion records embed
  existing canonical receipts outside workspace/cache. Per-S sequences select latest;
  corruption refuses. Capacity preflight blocks new eval registration, not existing
  proof lookup; staging is ineligible. Visible complete records remain eligible after
  post-link persistence errors with explicit uncertainty. OS-held workspace flock
  excludes concurrent root test/push without a stale-lock blockade. Private convenience
  evaluators and extracted-directory suite push retain their old contracts. Unsigned receipts do not prove authentication
  or drift absence.
  Any presence of a native suite ownership annotation reserves desired/live
  resources against legacy adoption, copying, bundle authorization and retirement.
  Registry workspace publication shares unsigned suite provenance defaults/flags and records Git source only from
  fingerprint-bound authored inputs matching HEAD; local layouts get no referrers.
- `pkg/suiteview` is a concrete read-only public projection over verified
  local AgentSuite OCI layouts and bounded uncompressed layout archives. It
  returns exact artifact/graph/member/instruction identities, copied instructions
  and the complete public model requirement for one explicit member. It grants no
  runtime/profile/composition selection, policy enforcement, cases, evidence,
  configuration, registry, execution, publication or Deploy authority. Keep public
  format ownership in `github.com/agent-primitives/suite`; KMX native loaders call
  the public projection on their frozen snapshots before applying stricter private
  runtime checks.
- Single-member `schemaVersion: 3` is offline profiled packaging only: explicit
  `push --runtime-profile ID [--platform OS/ARCH] --unevaluated` transfers all
  profiles/compositions under unchanged S. Legacy push rejects these selectors.
  Unselected Load/LoadArtifact and both inventory loaders refuse profiled input,
  even with one profile. Selected views keep a private immutable graph marker;
  receipt-only legacy declaration projection must preserve it and cannot qualify
  public implementation pins or manufacture observed runtime/host facts.
  `suite build` resolves exact profile tuples but AgentKit refuses profiled plans.
  All profiled execution/private proof creation or export/evaluated publication/
  native public evidence export/Deploy remain unsupported. Internal detached
  SessionsAssociationV1 creation/checking binds frozen selected declarations
  without altering S; imageVerification is always declared-unverified. No image
  byte/platform verification, implementation/ABI qualification, execution or Apply
  authority is implied; no CLI association consumer, image adapter or registry/auth
  support. Instruction credential lint returns a fixed redacted warning before
  the existing hard refusal in source/package preflight. Instructions are
  non-confidential and must never contain secrets; clean lint is not a guarantee.
  V3 policy is shared declaration only, never Sessions enforcement.
- Root `test --development-host FILE` separately supports the packaged
  AgentSessions chat inventory under the development boundary above with a
  required AgentSessionsBinding (--bindings); every cluster/inference/result
  selector (including explicit defaults) refuses. Missing development selection
  never falls back. Sessions proof feeds only the plan-only deploy render and is
  refused by receipt diff; there is no registry or caller-auth integration.
- Root `test` evaluates the full packaged Orka/kagent workspace inventory with
  a destination context (explicit `--to-context`, named `--to` or saved evalTarget)
  and optional new private `--out`; Orka uses --inference
  provider:NAME, kagent uses author-owned --bindings FILE, never both. Namespace
  must match kagent bindings and kagent refuses --result-port. It uses local
  flags and a nonpersistent operational hook. Unsupported/budget/output checks
  run before config; one fresh outer guard covers pair placement and all Tasks.
  Fail/unknown/drift/cancel/output errors stop later sends with no partial export.
  No model/runtime override, subset mode or standalone package
  reference is accepted. Authored llmJudge is supported for Orka/kagent and explicit AgentSessions development hosts with
  explicit --judge-base-url and optional --judge-key-env (environment name only).
  The fixed openai-chat-completions-v1 request style uses max_completion_tokens
  (1024), stream=false and no temperature override, including the pinned AIKit
  judge. No request-style flag, model inference or error fallback. Two independent
  scores must agree on the threshold side;
  unavailable/malformed/split results stay unknown. Judgment shares the existing
  per-run deadline and attribution is rechecked afterward. Root test and composed
  Deploy report non-pass case/run, assertion IDs/types and both judge scores/threshold;
  --show-content alone opts into sanitized bounded expected values, rationale and
  answer excerpts on stderr. Deploy --verbose does not opt into content. Default
  Orka JSON remains payload-free; cached Deploy skips invent no diagnostics.
  Diagnostic content is retained separately under KMX_HOME/eval-failures, never
  evidence, a receipt field/reference, a signature or a portable export. The
  per-canonical-workspace partition keeps 10 non-pass runs across revisions with
  64 KiB answers and 128 KiB serialized records, owner-only 0700 directories/0600 files and
  a separate persistent nonblocking flock. Validate UID, single links, bounded
  regular files, schema and privacy before offline reading or targeted deletion;
  refuse unsafe state rather than chmod/repair/RemoveAll. Retain before output
  even on abort; retention errors warn without changing verdicts or causing
  another send. Offline test explain lists/shows local records without config,
  Prepare, runtime, registry or model calls; --show-content opts into bounded
  stdout content, --clear deletes one ID or all only with explicit --workspace.
  Clearing never reaches passing/candidate state or upstream history. A bounded
  owned .pending file is never read/displayed; only explicit whole-workspace
  clearing removes it. It must not hide healthy records or cause pre-pruning on
  blocked saves. Pre-write eviction preserves the hard bound but may lose the
  oldest record on a subsequent write failure. Keep observable IDs on uncertain
  post-publication errors; retentionUnavailable is not proof of absent files. Post-judge attribution failure withholds content;
  known credential shapes/keys and header-like content are withheld before
  truncation. This is not a guarantee that arbitrary secrets are detected.
  The clarified local-storage policy sentence preserves the historical v2
  evidence fingerprint and existing receipts. Recovery omits judge endpoints. Private passing
  rows bind authored model/version, request style, threshold, policy, endpoint
  configuration hash, both scores and aligned modelMatches observations. Returned
  model identity matches only the exact authored model or model+"-"+version,
  never prefixes/substrings/case folding/whitespace normalization. Old judge rows missing the style require
  fresh evaluation. Text-only receipt wire shape is unchanged; older readers
  refuse judge rows. AgentSessions uses the same judge transport and evidence validation.
  Offline `test diff` reads strict full-passing native Orka/kagent suite
  evidence only. Old normalized receipts are unreadable. `test from-sessions`
  remains independent private reference extraction/export; RecordedExecution,
  legacy bundle replay/evaluation/judge wiring and their consumers are removed, with no native
  manifest consumer. Preserve private filesystem/current identity ResourceVersion
  and Secret metadata bracketing. Hermetic tests are not live qualification.
  Runtime-core assertions and llmJudge remain retained with their test groups;
  native judge wiring does not enable recorded candidate execution.
  The checkout-only native qualifier runs in the full read-only reusable Orka
  worker after raw model proof, with private captures and metadata-only artifacts.
  Relevant/unknown PRs, manual qualification, daily main nightly and every release
  retain that proof. Only explicitly safe PRs and routine main pushes skip it.
  Keep required `hygiene` and `e2e-hello-world` checks intact and fail closed on
  unintended skips/failures. No path/docs shortcuts belong inside the full worker.
  Its second exclusively owned kind cluster has one pinned Orka in
  orka-system and its own inference Provider/Secret; it shares the source AIKit
  endpoint through a fixture-only private-network NodePort, never a host mapping
  or another model workload. Registry --promote qualifies GET-only planning,
  absent-pair CLI creation, independent destination readback and one fresh Task;
  model mismatch refuses with zero admission/write/Task attempts. Source evidence
  is not destination evaluation. Keep the shard's existing timeout budget.
  A passing run qualifies that fixed workspace scenario; hermetic
  tests alone do not establish runtime/model execution.
- Lead with **Run / Test / Publish** as developer vocabulary: `run --agent`
  invokes an existing live Orka Agent, not workspace-local execution; `test`
  evaluates workspace cases; Publish is current registry `push`, not numbered
  local versions or a new command. Deploy remains real-target placement.
  Canonical `kmx test` is unreleased; v0.8.1 uses `kmx eval`. Keep hidden `eval`
  and all six helpers callable for at least one released version, with exactly
  one stderr notice: `kmx eval is deprecated; use kmx test (same arguments).`
  Preserve arguments, flags, stdout, exit codes, state and existing stderr labels
  apart from the notice. Keep `eval/`/`evals/`, `evalTarget`/`evaluationTarget`,
  schemas, APIs and machine JSON unchanged. See
  [Test compatibility](docs/kmx.md#test-compatibility-unreleased).
  Canonical `kmx deploy` is also unreleased; v0.8.1 uses `kmx lift` with the same arguments. Keep deprecated
  `kmx lift` hidden but callable for at least one released version, with exactly
  one stderr notice per legacy invocation:
  `kmx lift is deprecated; use kmx deploy (same arguments).`
  Preserve legacy stdout, stderr stage labels, arguments, flags and exit codes;
  help/completion remain inert. Canonical human help/output uses Deploy, while
  JSON keys/values (including `targets` operation `"lift"` and stage/result keys),
  public Go/API/internal `Lift` identifiers, evidence, storage and annotations stay
  unchanged. No data migration. Removal requires a later announced breaking
  release, not the optional pre-1.0 immediate-removal shortcut.
  Keep old guide fragments resolving with explicit anchors before renamed headings;
  preserve tagged release records and design history. Plain Deploy remains
  create-only; only explicit eligible retained Orka `--update` permits updates or
  rollback. Kagent refuses updates. Root `deploy` never provisions AKS;
  use `kmx aks up/down` for AKS.
- Prefer singular resource/domain groups and explicit actions. Avoid introducing
  another synonym for an existing operation. CLI, chat and console should use
  the same operation vocabulary where their scopes match.
- Reuse the existing operation handlers. Keep Cobra syntax, terminal rendering,
  runtime capabilities, target mechanics and infrastructure ownership separate.
  Bind operational Apps through `operationApplication(cmd)`: it preserves the
  invocation's App identity and copies its Runner before attaching `cmd.Context()`.
  Never rebind a shared Runner in place; preserve explicit child and cleanup
  contexts and signal ownership.
  A renamed command must not silently change runtime, target, ownership or
  supported operations.
- Update command registration, help, completion, relevant chat/console labels,
  generated next/recovery commands, primary docs and a `changes/` release-note
  fragment in the same slice.
  Record changed defaults and compatibility/removal policy. Keep old routes
  callable until an announced transition unless an outright removal is explicitly
  approved; record that removal and its replacements. Notices belong on stderr.
- Preserve report/artifact/answer stdout and exit contracts. Test meaningful
  selection, refusal and compatibility behavior, not just matching help strings.
  Help and completion must not perform operational mutations.
- A context supplies access. A target includes the selected runtime, cluster
  identity and namespace. Inference is independent. Unknown is not absent;
  ready is not answered or evaluated; retry is not result retrieval.
- Orka result forwards default to kubectl-selected free IPv4 loopback ports,
  retained continuously by the owned forward. Explicit `--result-port` is honored
  without fallback. Keep port-startup errors payload-free and distinct from unknown
  execution; port numbers are not evidence identities.
- Experimental `pkg/kmx.AgentEnvironment` is setup-only and unwired:
  `Up(EnvironmentSpec)` establishes a logical `Environment`, not a context,
  exact target, runtime selection, inference binding or teardown authority.
  Bare `kmx local up` is only its current conceptual mapping. Do not claim CLI
  integration, inspection, persistence, recovery or down semantics until those
  capabilities have production implementations and evidence.
- Preserve historical release records and explicit compatibility tests when
  updating current examples. Do not bulk-rename historical text or serialized
  state. Generated recovery commands must preserve the invoking executable,
  selectors and shell quoting.

## Agent-file preview boundary

Canonical `run FILE` / `test FILE` accept explicit `--tool-bindings FILE` as
host-only input; no discovery or inbound-MCP binding selection. The production
runner installs localtools.NewMCPHost: explicit env snapshot, no constructor
network, lazy read-owned Streamable-HTTP session and one fixed GitHub search.
Require exact repository_url and explicit merged_at, complete search and <=20
bounded number/title rows. Protocol2025-11-25 is pinned; no server/discover,
OAuth/callbacks/standalone SSE/redirects/proxy credentials/retries/input-required
resends. Read context bounds SDK-detached cleanup. Exact credential echoes
refuse in protocol data; known-shape checks also cover every model/journal-bound
projection. Discard remote metadata/descriptions and SDK logs. Test bridges stay
in _test.go; scripted-model hosted smoke is not real-LLM qualification.
Trusted embedding can still supply a frozen binding/host executor. Declared tools with either a Copilot agent or judge refuse
before credentials or journaling; tool-free Copilot is unchanged and future
Copilot tools depend on #662. No silent tool dropping.

Canonical `run FILE` / `test FILE` use the parser and `agentpreview.Runner`
interface without cluster/workspace configuration. CLI/MCP fake/local routes
use embedded Sessions: optional ad-hoc input first, selected ordered examples,
one invocation-owned private journal, one fresh Session per turn, and the shared
two-sample judge. Execution/deadline errors abort later sends; journal close
failure vetoes completion. When the agent OR judge uses Copilot, retain the direct
`preview runner (no journal)` and report NoJournal for the whole invocation,
with one judge call per llmJudge. Fully journaled Copilot is follow-up work until
provider-owned content preflight is available; never expose tokens to the runner.
No retries or fallback. Scripted test fakes remain `_test.go` only, not real-model
qualification. Copilot selection sends instructions/inputs and any judging
answer/rubric to the hosted service; never imply SDK or Sessions execution.
`app.FilePreviewResult` v1 is the safe shared projection, not a
native evidence receipt. Subset is always partial, execution/journal failure is
unknown, and only an explicit caller override changes the judge route. Keep
live `run --agent`, native workspace Test/Explain and legacy Eval unchanged.
File Run/Test `--verbose` is content-free stderr diagnostics only, never implicit
`--show-content`. Keep transport errors classified without raw URLs/bodies;
only validated local origins and standard endpoint paths may be shown. Failed
diagnostic writes stop later sends and refuse a full pass.
See [file preview](docs/kmx.md#agent-file-local-preview-unreleased-integration).

## Agent-file MCP boundary

`kmx mcp` is stdio-only and config-free at startup. It accepts agent paths in any
directory; relative paths resolve against the server's working directory. Prefer
absolute WSL paths from a Windows host. Read/write only `*.agent.md` and
`.squad/agents/<member>/charter.md`, checking both the requested and resolved
path. Ordinary folder names and links to agent files are supported; never use an
agent-looking link to access a non-agent file. Keep descriptor-bound file IO and
revision-checked edits. Optional `--root DIRECTORY` remains only for legacy
plugins and the separately configured native Squad flow. Expose exactly
Create/Edit/Run/Test; call canonical parsing and `App.PreviewAgentSnapshot`,
never the path wrapper or shell CLI strings. Optional native v2 targets use
explicit `App.PreviewSquadSnapshot` and revalidated native state, not auto-detection.
Provider/endpoint selection comes from trusted startup configuration or the
fixed unbound Copilot default, never from tool/charter endpoint fields; the exact
model comes from the parsed agent file. Keep complete safe App results on errors;
App owns full-path projection and credential redaction; MCP must not restore raw
paths after it returns. Fixture runners are not production execution. Edit preserves mode bits, not
ownership/ACLs/xattrs; normal directory inheritance is the day-0 own-file contract.
No new runner, Explain tool or MCP network listener. The read-only
`kmx://agent/{path}` resource projects the requested supported agent file;
it is not a fifth tool or generic file reader. No folder confirmation is needed
for charter content. After successful Create, open its returned file.path in the
existing canvas. Give one plain notice before the first hosted-model send;
ordinary agent-file creation stays confirmation-free, without path-character
restrictions. Native Squad retains its existing proposal/setup-effects approval.
Safe model excerpts retain the existing showContent opt-in.
Latest preview state is process-local, revision-bound and never inferred on read.
The separate native Copilot view uses the host SDK and a private loopback asset
bridge, without mutations or direct agent-file reads.
The repository-installable WSL plugin lives in `plugins/copilot-wsl`, including
shared skills and canvas runtime assets. Legacy native packaging copies these
same sources; do not maintain a second canvas/skill implementation. Keep its MCP
command exactly `wsl.exe -e /usr/local/bin/kmx mcp`, without a bundled engine,
root/model configuration or installation helper. Node tests stay outside the
installable tree under `plugins/copilot`.
See [MCP](docs/kmx.md#local-stdio-mcp).

## Internal native Squad authoring boundary

`mcp.OpenSquad` remains initialized-only cast/enrich. Optional trusted startup
`--squad-program ABS --squad-release 1.1.0` binds `OpenSquadSetup` to closed native
v2 branches of the existing four tools; absent configuration retains v1 discovery
and behavior. Startup never initializes. Approved native Create validates the
whole proposal before conditional fixed `init --no-workflows --no-vscode-default`,
then `cast --name ID --role ROLE`. Require a separately operator-qualified
standalone program/runtime; release/platform strings are declarations, not
authentication. Closed stdin, bounded drained output, personal lookup disabled,
selected launcher-derived SQUAD_STANDALONE_HOME and fixed root-bound Git overrides;
this is not a sandbox. New setup refuses prior MCP/skill config, aliases and
worktree pointers; initialized roots validate/reuse without re-init. No downloader,
force, shell string, adoption or automatic retry/recovery. Refuse redirected,
legacy/personal/global roots and existing identities/partial member directories.
Native setup owns metadata; kmx changes only the observed new charter via
Preferred-value splice and appended task/examples, with source revision and
file/parent identity checks under existing cooperative OS locks. Serialize this
creator's calls and arrange a quiet project: external native casts/editors remain
outside the lock contract, and cwd is not child-process containment. Preserve
registered-unenriched/unknown partial effects and acknowledged publication even
on cancellation; never roll back metadata. Retained-file observations are not an
exhaustive child event log, registration is not dispatch, and enrichment is not
execution/package-digest evidence. Native member Edit/resource/Run/Test reconstruct
mapping from managed casting registry/history/commit plus active exact ID/name/role,
roster and enriched charter on each operation/restart; never persist kmx mapping.
Failed validation shows **not set up by kmx**, never origin guessing or canonical
fallback. Valid mapping is not authenticated origin, restored draft or prior pass.
Edit replaces only Preferred/Task Instructions/Examples with source/parent CAS.
Native preview is revision-bound and tool-free; fake consumes task-only directives,
real routes retain native+task instructions excluding Examples. No package digest,
team runtime or native Squad canvas actions. See [native authoring](docs/agent-file.md#internal-native-cast-and-enrich).

## Single-file charter boundary

`internal/kmx/agentfile` keeps `Parse`/`Load` canonical as described in
[agent-file.md](docs/agent-file.md), offline, with optional `## Tools` directly before required final Examples. Both are
excluded from instructions; absent Tools preserves instruction bytes and all
revisions bind exact original source. Only `- github_list_merged_prs` is accepted;
parsing a name grants no execution or endpoint/credential authority. Separate explicit `ParseSquad` accepts
qualified Squad 1.1.0 native starters/enrichment with an approved cast mapping,
original bytes and source spans. It returns a distinct `SquadCharter`, not a
runnable `Agent`; ordinary file CLI/v1 MCP remain canonical-only. Explicit native
v2 preview projects a revalidated charter into the shared runner in memory only.
Never auto-detect this profile, invent missing native identity fields or treat
its release selector as cast-origin/registration proof. Reuse runtime assertion semantics;
refuse tools and unsupported declarations. Parse/Load grant no execution,
trusted route, source-write, Squad registration or native suite-loader authority.
`Inspect` shares the scanner for read-only minimal titled-agent display while
`Parse` remains the execution validator; inspection grants no Run/Test acceptance.
Keep payload-bearing parsed values out of default logs/results. Explicit offline
`run-profile export FILE` instead emits content-bearing deterministic JSON on
stdout, preserving authored models and ordered examples/hashes with every target
status `not_run`. It is not evaluation evidence, a package, an AgentSuite runtime
profile or deployment authority; never resolve routes or credentials for export.
Agent-file access is not a hostile concurrent filesystem sandbox; preserve
credentials and restrict MCP access to agent files rather than adding folder or
symlink refusals.

## Local preview boundary

`internal/kmx/localpreview` runs one tool-free agent file on embedded Sessions
(in-memory gRPC, upstream Service/Placer/`chat` harness, private SQLite journal
under `config.LocalPreviewJournalDir()`), labeled **local preview**. See
[local-preview.md](docs/local-preview.md). Its `agentpreview.Runner` adapter
backs `run/test FILE` and MCP for fake/local routes; the direct Copilot exception
above remains explicitly unjournaled. `Model.Preferred` is exact: `fake` is the in-memory deterministic
route; other models need an explicit OpenAI-compatible endpoint and optional
key environment variable name. No fallback, retries, tool execution, daemon,
subprocess, cluster or config probing. Refuse missing routes, tool assertions
and unwired judges before any file or network effect; refuse journal-bound
instructions, examples, ad-hoc input and identities containing a resolved agent
or judge credential before the journal opens, including returned assertion IDs,
types and authored judge model/version metadata; refuse
tool-call/malformed/duplicate-key/oversized/non-200 replies, missing or mismatched
response model identities, and answers
containing the endpoint credential (exact value, any length) with fixed text,
because Sessions journals content at turn start and model errors verbatim.
Credentials never reach files, journals, results or errors. Consume only parsed
agent-file fields; Examples YAML comments never reach model or judge requests,
journals or results. `llmJudge` reuses the native judge through
`app.LocalPreviewJudge`, defaults to the agent route (fake judge for fake), and
only explicit `JudgeEndpoint` overrides it. Journals keep content in cleartext,
bounded to 20 runs at each start, owner-only, clearable by run or all;
executing runs hold an OS lock and are never pruned or cleared (on Windows this
rests on unverified share-mode behavior), and overlapping starts can leave
extra finished runs until the next start or clear. Journals stay under
`config.StateDir` on every OS (`%LOCALAPPDATA%` on Windows). Phase-3 state-root
qualification does not wire local preview on Windows; that remains later
integration work under
[#578](https://github.com/kaimahi-agents/kaimahi/issues/578). The additive
`kmx-local-tools-v1` host-bound harness has a separate bounded
converter and complete-invocation replay proof, never a relaxed suite/chat check.
Its optional single no-args read is host-enforced; at most two agent model calls,
with unchanged separate tool-free judging inside the case deadline. Safe call
counts do not make tool assertions or native proof; actual-call qualification
must independently show a correlated read. Bindings and credentials never enter
harness config. Host content checks cover decoded model IDs/text before journal
append as well as authored inputs and bounded projected results. Incomplete
traces cannot pass. Inspection replays with zero model/executor/judge calls,
without bindings or credentials, and has no verdicts. Preview
and replay never produce suite evidence, receipts, publication or Deploy
authority.

## Offline multi-member workspace boundary

- Provisional private `workspace.yaml` schemaVersion 2 uses `members` paths under
  one suite root, with member-scoped `eval/ID/` cases and the shared pinned build
  profile. Keep representation behind workspace loading pending authoring decisions
  in [#526](https://github.com/kaimahi-agents/kaimahi/issues/526). SchemaVersion 1
  source/create prompts are unchanged. Both formats use public `evals/ID/` indexes
  and cases through `DecodePackagedCase`; no private case list or migration reader.
  Preserve merged one-member packaging/receipt rules. File budgets are 4 shared +
  4 per policy-free member + 128 total cases (136 at one member, 196 at sixteen).
  Each optional policy adds one file; shared source-free readers admit at most 212.
- Multi-member `Prepare`/`LoadInventory` is offline only. Preserve complete closed
  graph/case coverage, canonical member ordering and bounded confined reads. Existing
  execution `Load`/`LoadArtifact` refuse rather than choosing a first member.
  Root test/workspace push (even unevaluated)/composed Deploy preflight the descriptor
  before state, output or operational selection; verified multi artifacts cannot
  reach reference-Deploy target observations. No binding, approval, invocation or
  evaluation semantics, mixed-runtime execution or runtime qualification is implied.

- Experimental `create NAME --workspace DIR --members FILE --flow FILE` imports strict JSON member rows and the public critical `kmx.flow.v1alpha1` document, without prompts, runtime selectors or operational configuration. Member model strings use the existing fixed openai-compatible protocol; relative input paths are confined to the members-file directory. All inputs and complete graph closure validate before destination writes. Source schemaVersion 2 alone permits an optional workspace-relative `flow` path. Explicit PrepareFlow/flow inventory and read-only suiteview flow APIs preserve all members, original S/F and copied recipes; default loaders and all current execution/transfer/registry paths remain strict. The flow file alone gets 4 MiB, retaining 1 MiB other-file/8 MiB total/212-file/256-entry ceilings and 128 combined member+flow cases. This is offline authoring/storage, not qualified execution, approval enforcement or whole-suite evidence.

Experimental flow destination preflight is internal only: explicit `kmx.suite-binding/v1alpha1` inputs select existing named targets with exact context/namespace and `provider:NAME` references. Independently observed non-secret suite binding B excludes RV/scale churn while private snapshots retain freshness. Flow candidate tuples include S/member/case/run/step/operation, retain the old run marker for fail-closed older readers, and reserve malformed flow markers. Existing cleanup may inspect and explicitly remove validated flow candidate roots through its UID/RV-conditioned path; legacy passing receipts refuse flow candidates. No new CLI flags, evaluation, dispatch, publication or Deploy support follows from these private seams.

## Optional member policy boundary

Orka member YAML accepts optional member-relative `policy: policy.json` containing
public `agentsuite.policy/v2` IPv4/TCP/port JSON. Freeze the original selected
source; package authored JCS bytes at `policies/<id>.json` through `Agent.policy`
with an ordinary descriptor digest, never an effective-policy framed digest.
Preserve authored array order/duplicates and policy-free bytes/fingerprints.
Offline loaders and explicitly unevaluated transfer retain policy. Native eval,
private preparation, composed/digest Deploy, evaluated publication and AgentKit
sandbox export refuse declared policies until qualified enforcement is supported.
kagent/AgentSessions source declarations refuse. Existing reads refuse static
symlinks/FIFOs; concurrent hostile workspace mutation is outside their guarantee.
Do not infer enforcement from authoring, transfer, readiness, labels or annotations.

## AgentSuite evaluation storage

- Loose-directory packaging uses default non-executable regular files and exact
  declared ToolProvider inventory modes, never host execute bits. Keep Git-mode
  provenance separate. Prepared cache records require the current packing-policy
  marker; absent/unknown markers refuse with move-aside guidance, never repair or
  delete old entries. Keep that safe remedy through redacted eval/public-export
  boundaries. Cache policy is not authored fingerprint or OCI S: unchanged native
  S retains exact-S proof, while changed S requires fresh evaluation. Original
  source-free OCI artifacts retain their tar metadata and existing receipt rules.

- `test verify --suite LAYOUT --evidence FILE --expect FILE` is the state-free
  offline CI consumer of shared VerifyEvaluation. Digest form requires explicit
  `--from-layout DIR`; never resolve a registry, prepare a workspace or search
  caches/private proof. ExpectedEvaluation is a complete independently reviewed
  policy file committed beside the suite, never copied from evidence claims.
  Inputs are bounded public regular JSON files; strict required presence includes
  `judges: []` and empty platform.variant. Success-only text/JSON summary exits0;
  typed safe refusal codes go to stderr and exit1 without untrusted causes/paths.
  No configuration, KMX state, execution or private push/Deploy authority involved.
  Require suite v0.3.1 or later for nonblocking, no-follow OCI special-file refusal;
  do not replace the shared loader fix with a KMX preflight or timeout workaround.

- `test export --member ID --out FILE` is offline portable-evidence publication,
  not evaluation or a private push/Deploy gate. Use shared suite v0.3.1 evidence
  types/VerifyEvaluation against the exact prepared OCI layout and full ordered
  case/run/assertion inventory. Never serialize or redact private receipts.
  Fresh managed evaluations capture Go build info on every platform: tagged
  module version plus module checksum, OR full VCS revision plus explicit
  unmodified status. Modified producers always refuse; unstamped development
  builds and pseudo-versions without a clean revision also refuse. Optional
  reliable Linux binary hashes are private extras, never
  eligibility or source-identity substitutes. Bound separate companions to the
  committed sequence/workspace/S/canonical receipt digest; leave private receipt
  and envelope formats unchanged, no older fallback or orphan authority.
  Public config hashes use documented non-secret projections, never existing
  private endpoint/Secret/binding/config hashes. Public output uses --out, stays
  outside workspace/cache/managed state, and never replaces a file. Help and
  completion are inert; no operational configuration, runtime or registry calls.

- The portable format, validator and schemas are owned by
  [AgentSuite](https://github.com/agent-primitives/suite/blob/main/spec.md).
  Keep KMX details, migration notes, and design history in this repository's
  implementation documentation, issues, or changelog. Import focused public
  packages as `suitecore` (`github.com/agent-primitives/suite/core`), `suiteoci`
  (`github.com/agent-primitives/suite/oci`), `suiteevidence`
  (`github.com/agent-primitives/suite/evidence`) and `suiteflow`
  (`github.com/agent-primitives/suite/experimental/flow`). Do not import the
  deprecated module root. Keep `kmxsuite` for the internal builder package and
  `suiteoras` for its ORAS adapter.
  Keep byte-identical consumer fixtures under `testdata/agentsuite`; refresh
  their provenance manifest only with reviewed upstream changes and run
  `python3 scripts/check-agentsuite-fixtures.py`.
- Public embedded evaluations are optional (`SHOULD` for producers), rooted at
  `evals/<agent>/index.json` and bound through the agent's `evals` reference.
  Indexes bind ordered JSON cases with JCS digests; no evals remains conformant.
  Follow [AgentSuite §8.2](https://github.com/agent-primitives/suite/blob/main/spec.md#82-agent-evaluations)
  and its closed schemas. The public validator
  checks storage and identity only, never executes assertions or claims a pass.
- Preserve KMX's authored assertion `id`/`type`/operand spellings when mapping
  cases; public JSON identities are not private YAML/framed receipt identities.
  Results remain external to the evaluation tree and bind the exact OCI suite
  manifest.

## kagent version boundary

- Exact v0.10.3 Go/linux-amd64 packaged profiles use author-owned bindings,
  unsecure auth only, one fresh blocking A2A Task per declared case run, never retried.
  Trusted-proxy refuses before admission/send. Digest Deploy creates absent objects
  only with runtime-matching evidence; occupied primaries/children refuse.
  No Python/RemoteMCP execution, standalone chat/run, installer/discovery or
  retirement is implied. Root destination status is GET-only placement inspection. Preserve Orka defaults and workspace missing-runtime behavior.
- CI retains exact v0.10.3 bootstrap, raw upstream Tool smoke/direct single A2A
  MCP proof, separately tool-free packaged two-case eval/evaluated push and
  source-withheld digest Deploy with occupied-repeat refusal. No legacy receipt,
  status or judge claim. Final build/live execution are needed for qualification.
- Every PR/main push gets hermetic kagent contract/fixture and focused offline Go
  checks, labelled not live qualification. Full live A2A, packaged evaluation,
  evaluated push, digest lift and helpers run nightly/manual and on the exact
  release commit. Preserve the full worker's pins, commands, retries and budget.
  `e2e-hello-world` must always evaluate fast success and intentional full skips;
  failed/cancelled/missing proof is never an intentional skip. Tom monitors nightly
  CI; no new issue-reporting authority is implied.

## Candidate evaluation and explicit cleanup

Public Orka workspace eval uses fresh S/run candidates, never adoption/update/reuse.
Full passing evidence exports only after UID/RV-safe cleanup, exact owner-UID child
closure and final source/shared-input checks. KMX-created Provider/Agent/Task carry
full run annotations; Orka descendants do not inherit them. Startup reports only;
unknown reporting never blocks a fresh eval. `test cleanup [run-id]` lists without
a run, otherwise previews frozen UIDs and requires confirmation even on local kind.
Unanchored or unverifiable orphans refuse deletion/completeness claims. No journal,
lease, automatic sweep or publication blockade. Private Quickstart/console stay
canonical; kagent/Sessions/judge behavior and all existing timeouts are unchanged.

## Native suite case packaging

- Native workspaces author `eval/*.yaml`; preparation converts each case to a
  public evaluation case at `evals/<member>/cases/<id>.json`, listed in file-name
  order by `evals/<member>/index.json` and bound through the member's `evals`
  reference. Legacy `expectContains` entries keep `expectContains.N` IDs so
  assertion definition digests are unchanged. Private native metadata carries no
  case inventory. Packaged readers convert cases back through one decoder,
  carry `runs`/`minPasses`, and refuse `critical: false` and extension assertion
  types rather than ignoring them. Receipts record packaged case paths and digests of the
  packaged JSON bytes. Receipts with `metadata/kmx/eval/` case paths refuse with
  a fixed rerun remedy; do not add a migration reader.

## Verification

The browser documentation preview renders the root README and `docs/*.md`
directly; AgentSuite specification links lead to its standalone repository.
Keep Markdown
authoritative: navigation and theme code may link to maintained content but must
not copy it. Keep VitePress output,
caches, and `node_modules` untracked. Documentation changes retain the existing
link, README-front-door and repository-map checks and run
`npm --prefix docs run docs:check` when they affect the rendered site. The
current exact alpha framework pin is a draft choice, not approval to publish;
custom-domain and versioned-site changes require a separate review.

Run checks appropriate to the changed paths as described in CONTRIBUTING.md.
App CI runs all four process-isolated shards on Linux and macOS; both platforms
gate hygiene and artifact names include OS/shard. The App test harness resolves
the macOS temporary root before fixtures; retain production symlink refusals.
Command changes normally require `go test ./cmd/kmx`, affected operation-package
tests, documentation links, repository-map and legacy-runtime checks. These
checkers use tracked files, so include intended additions in the index
when checking the complete change. Update AGENTS.md when implemented conventions
change so subsequent work follows the same rules.
