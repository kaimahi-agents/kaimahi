# Repository guidance

Read [CONTRIBUTING.md](CONTRIBUTING.md), the
[development invariants](docs/development.md#invariants-to-preserve), and the
[repository map](docs/repository-map.md) before changing command behavior.

## Command consistency

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
  deployment is digest `kmx lift`; cloud and runtime setup
  remain `kmx aks up/down` and `kmx orka install/status`.
  The AKS group is a hidden compatibility route, not a public root domain.
  Guided quickstart is `kmx quickstart --interactive` (`-i`); the old wizard
  command is a retirement stub. `kmx chat` is always interactive and no
  longer accepts `--interactive`. `kmx targets` is the read-only capability view.
  Plane administration, model overlays, migration and their demo are removed;
  do not restore or regroup their commands.
- Named destination selectors live in versioned workspace-root `kmx.yaml`, not
  source `workspace.yaml` or published content. `eval/lift --to NAME` resolves
  concrete context/namespace/runtime expectation/inference-or-bindings/registry
  values once and passes those copies onward; never reread config during an
  operation. Explicit flags override saved fields. `evalTarget` is evaluation-only;
  Lift never guesses a destination. Explicit-reference Lift without `--to`
  remains workspace-independent. Runtime stays package-owned. `ctx` is an
  offline list; optional checks are local context-name/anonymous registry reads,
  not runtime/inference/Secret probes. `targets` remains runtime capabilities.
  Reference Lift still requires a digest and evidence. No-reference `lift --to NAME`
  composes missing Orka evaluation/publication/Lift from one frozen workspace and
  selector plan. Require saved publishRepository and destination evaluationTarget;
  automatic disclosed promotion, one scoped confirmation, four default stage lines
  with verbose/JSON details. Exact-version private completion observations authorize
  GET-only skips, never Apply/reuse/adoption/repair. Keep kagent out of composed
  Lift until its candidate-scoped placement and ownership-safe cleanup are qualified.
- Release Lift stamps only newly created primaries with versioned non-secret
  `kaimahi.dev/lift-record` JSON: exact consumed canonical receipt identity,
  source/destination, actual gate, attempt time and expected specs. Actor is omitted
  unless known. Exact Orka reuse and composed skips remain no-write; never repair
  or infer missing companion provenance. Separate bounded private `release-lifts`
  intent/outcome records retain canonical receipts and acknowledged partial effects;
  they grant no evaluation, ownership, skip or replay authority. Keep provenance out
  of plan digests, suite ownership markers, portable schemas and candidate eval.
  Root destination status reads these annotations without private-state enrichment;
  it never infers missing fields from companions or local history. See
  [cluster-visible Lift provenance](docs/suite-workspace.md#cluster-visible-lift-provenance).
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
  support it; root eval requires explicit --development-host FILE for literal
  loopback, unauthenticated operator-observed chat. It loads no cluster settings,
  sends exact config once per fresh Session, requires a complete five-record
  qualified journal and full ordered pass, then exports private runtime-specific
  proof. Exact-S --evaluation push accepts that proof offline. Cluster selectors,
  mixed-runtime proof, tools, lift/chat/status and live create refuse.
  Operator declarations are not authenticated host/build attestation; cleartext
  journal retention is disclosed. Session replay cannot create candidate proof.
  Optional --runtime-version must match that commit; --kagent-runtime refuses.
  Runtime is member-owned KMX-private metadata, not a public AgentSuite field.
  Root eval supports the packaged kagent profile with author-owned --bindings;
  evaluated push consumes matching runtime-specific full-passing suite evidence.
  Legacy status readers and retirement remain unavailable; digest lift uses the common release
  seam with runtime-matching evidence and author-owned kagent bindings. Kagent
  lift is create-only, refusing occupied primaries/children even with matching
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
  or loads cluster settings. Workspace evaluation is root `eval`; deploy-only release lift is root
  `lift <ref>@<digest>` with explicit private `--evidence`; no-reference workspace
  `lift --to NAME` separately composes the missing stages.
  User `agent.yaml` readers/writers/receipts, `bundle` commands,
  `create --live` including previews/wizard, live-only flags, root run bundle
  mode and `--bundles` flags are removed outright. Unknown routes/flags refuse
  before config. Old Orka/kagent/AgentSessions bundle receipts are unreadable;
  no migration or saved-file/resource deletion. Legacy status readers and retirement remain
  unavailable. Quickstart uses native source → full eval → exact local publication
  → reviewed Lift Apply/Verify, then a separate fresh answer Task. Console Ready
  is placement only. Chat `/lift` and console Lift print only
  `Lift moved: kmx lift <ref>@<digest> --to-context … --evidence …`.
  No release GUI/live-copy fallback. Wizard tools/host modes and live tools/inference
  edits are retired; host Foundry/Copilot chat controls remain independent.
  Preserve public AgentSuite/ToolProvider implementation-bundle graph/build/pull,
  extracted-directory transfer and alpha `pkg/kmx` interfaces. PortableAgent typed
  scaffolding used by native preparation is not a persistent old reader.
  Native suite-owned resources refuse legacy invocation/adoption/copying.
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
  No lift Tasks/model calls, fake Provider or destination evaluation is implied.
  ApplyReleaseLift
  is guarded and single-use, rechecks artifact/evidence/target/owned proof, preflights
  both creates after the guard, and only conditionally creates or exactly reuses.
  Partial outcomes retain acknowledged UIDs and readiness separately; no update,
  adoption, retry, rollback or cleanup. VerifyReleaseLiftPlacement rechecks an opaque
  success before caller invocation. Created/recreated UIDs require fresh evaluation
  before later receipt-bound reuse. Root lift supports registry/local --from-layout,
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
  still abort. AgentSessions development evaluation shares this repeat policy.
  Private repeated rows retain ordinal/policy and actual outcomes; default
  one-run encoding stays unchanged. Field names/defaults/validation follow
  [AgentSuite §8.2.2](https://github.com/agent-primitives/suite/blob/main/spec.md#822-case-documents-and-assertions):
  minPasses requires runs and both are positive integers. KMX additionally caps
  runs at 100 under the spec's consumer resource-limit allowance.
  Root push requires latest completed private exact-S evidence by default;
  `--evaluation FILE` explicitly overrides lookup without fallback, and
  `--unevaluated` explicitly retains ordinary publication. Explicit REPO:TAG
  remains unchanged; bare push requires saved publishRepository and derives only
  its tag as `s-<full-S-hex>`. Root eval makes --out optional and registers managed
  proof only after evaluation, Orka cleanup, final input checks and evaluator/output
  closes. Managed 0700/0600 per-workspace/per-S immutable completion records embed
  existing canonical receipts outside workspace/cache. Per-S sequences select latest;
  corruption refuses. Capacity preflight blocks new eval registration, not existing
  proof lookup; staging is ineligible. Visible complete records remain eligible after
  post-link persistence errors with explicit uncertainty. OS-held workspace flock
  excludes concurrent root eval/push without a stale-lock blockade. Private convenience
  evaluators and extracted-directory suite push retain their old contracts. Unsigned receipts do not prove authentication
  or drift absence.
  Any presence of a native suite ownership annotation reserves desired/live
  resources against legacy adoption, copying, bundle authorization and retirement.
  Registry workspace publication shares unsigned suite provenance defaults/flags and records Git source only from
  fingerprint-bound authored inputs matching HEAD; local layouts get no referrers.
- Single-member `schemaVersion: 3` is offline profiled packaging only: explicit
  `push --runtime-profile ID [--platform OS/ARCH] --unevaluated` transfers all
  profiles/compositions under unchanged S. Legacy push rejects these selectors.
  Unselected Load/LoadArtifact and both inventory loaders refuse profiled input,
  even with one profile. Selected views keep a private immutable graph marker;
  receipt-only legacy declaration projection must preserve it and cannot qualify
  public implementation pins or manufacture observed runtime/host facts.
  `suite build` resolves exact profile tuples but AgentKit refuses profiled plans.
  All profiled execution/private proof creation or export/evaluated publication/
  native public evidence export/Lift remain unsupported; no image adapter,
  association, registry/auth or target-runtime qualification is implied.
  V3 policy is shared declaration only, never Sessions enforcement.
- Root `eval --development-host FILE` separately supports the packaged
  AgentSessions chat inventory under the development boundary above; every
  cluster/inference/bindings/result selector (including explicit defaults) refuses.
  Missing development selection never falls back. Sessions proof is refused by
  lift and receipt diff; there is no registry or caller-auth integration.
- Root `eval` evaluates the full packaged Orka/kagent workspace inventory with
  a destination context (explicit `--to-context`, named `--to` or saved evalTarget)
  and optional new private `--out`; Orka uses --inference
  provider:NAME, kagent uses author-owned --bindings FILE, never both. Namespace
  must match kagent bindings and kagent refuses --result-port. It uses local
  flags and a nonpersistent operational hook. Unsupported/budget/output checks
  run before config; one fresh outer guard covers pair placement and all Tasks.
  Fail/unknown/drift/cancel/output errors stop later sends with no partial export.
  No model/runtime override, subset/content mode or standalone package
  reference is accepted. Authored llmJudge is supported for Orka/kagent and explicit AgentSessions development hosts with
  explicit --judge-base-url and optional --judge-key-env (environment name only).
  The fixed openai-chat-completions-v1 request style uses max_completion_tokens
  (1024), stream=false and no temperature override, including the pinned AIKit
  judge. No request-style flag, model inference or error fallback. Two independent
  scores must agree on the threshold side;
  unavailable/malformed/split results stay unknown. Judgment shares the existing
  per-run deadline and attribution is rechecked afterward. Root eval discards
  reasoning and omits judge endpoints from recovery commands. Private passing
  rows bind authored model/version, request style, threshold, policy, endpoint
  configuration hash, both scores and aligned modelMatches observations. Returned
  model identity matches only the exact authored model or model+"-"+version,
  never prefixes/substrings/case folding/whitespace normalization. Old judge rows missing the style require
  fresh evaluation. Text-only receipt wire shape is unchanged; older readers
  refuse judge rows. AgentSessions uses the same judge transport and evidence validation.
  Offline `eval diff` reads strict full-passing native Orka/kagent suite
  evidence only. Old normalized receipts are unreadable. `eval from-sessions`
  remains independent private reference extraction/export; RecordedExecution,
  legacy bundle replay/evaluation/judge wiring and their consumers are removed, with no native
  manifest consumer. Preserve private filesystem/current identity ResourceVersion
  and Secret metadata bracketing. Hermetic tests are not live qualification.
  Runtime-core assertions and llmJudge remain retained with their test groups;
  native judge wiring does not enable recorded candidate execution.
  The checkout-only native qualifier runs in the existing exclusively owned
  Orka shard, after legacy model proof, with private captures and metadata-only
  artifacts. Its second exclusively owned kind cluster has one pinned Orka in
  orka-system and its own inference Provider/Secret; it shares the source AIKit
  endpoint through a fixture-only private-network NodePort, never a host mapping
  or another model workload. Registry --promote qualifies GET-only planning,
  absent-pair CLI creation, independent destination readback and one fresh Task;
  model mismatch refuses with zero admission/write/Task attempts. Source evidence
  is not destination evaluation. Keep the shard's existing timeout budget.
  A passing run qualifies that fixed workspace scenario; hermetic
  tests alone do not establish runtime/model execution.
- Keep **Create → Prove → Lift** as the local-to-remote story. **Lift** is the
  chosen agent lifecycle verb: retain digest `kmx lift`,
  and use Lift in corresponding help rather than renaming the
  operation to deploy. Deployment remains a technical description of its effect.
  Root `lift` is deploy-only packaged-suite lift, not an AKS provisioning alias.
  Use `kmx aks up/down` for AKS.
- Prefer singular resource/domain groups and explicit actions. Avoid introducing
  another synonym for an existing operation. CLI, chat and console should use
  the same operation vocabulary where their scopes match.
- Reuse the existing operation handlers. Keep Cobra syntax, terminal rendering,
  runtime capabilities, target mechanics and infrastructure ownership separate.
  A renamed command must not silently change runtime, target, ownership or
  supported operations.
- Update command registration, help, completion, relevant chat/console labels,
  generated next/recovery commands, primary docs and CHANGELOG in the same slice.
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
  Root eval/workspace push (even unevaluated)/composed Lift preflight the descriptor
  before state, output or operational selection; verified multi artifacts cannot
  reach reference-Lift target observations. No binding, approval, invocation or
  evaluation semantics, mixed-runtime execution or runtime qualification is implied.

## Optional member policy boundary

Orka member YAML accepts optional member-relative `policy: policy.json` containing
public `agentsuite.policy/v2` IPv4/TCP/port JSON. Freeze the original selected
source; package authored JCS bytes at `policies/<id>.json` through `Agent.policy`
with an ordinary descriptor digest, never an effective-policy framed digest.
Preserve authored array order/duplicates and policy-free bytes/fingerprints.
Offline loaders and explicitly unevaluated transfer retain policy. Native eval,
private preparation, composed/digest Lift, evaluated publication and AgentKit
sandbox export refuse declared policies until qualified enforcement is supported.
kagent/AgentSessions source declarations refuse. Existing reads refuse static
symlinks/FIFOs; concurrent hostile workspace mutation is outside their guarantee.
Do not infer enforcement from authoring, transfer, readiness, labels or annotations.

## AgentSuite evaluation storage

- `eval verify --suite LAYOUT --evidence FILE --expect FILE` is the state-free
  offline CI consumer of shared VerifyEvaluation. Digest form requires explicit
  `--from-layout DIR`; never resolve a registry, prepare a workspace or search
  caches/private proof. ExpectedEvaluation is a complete independently reviewed
  policy file committed beside the suite, never copied from evidence claims.
  Inputs are bounded public regular JSON files; strict required presence includes
  `judges: []` and empty platform.variant. Success-only text/JSON summary exits0;
  typed safe refusal codes go to stderr and exit1 without untrusted causes/paths.
  No configuration, KMX state, execution or private push/Lift authority involved.
  Require suite v0.3.1 or later for nonblocking, no-follow OCI special-file refusal;
  do not replace the shared loader fix with a KMX preflight or timeout workaround.

- `eval export --member ID --out FILE` is offline portable-evidence publication,
  not evaluation or a private push/Lift gate. Use shared suite v0.3.1 evidence
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
  implementation documentation, issues, or changelog. Import the public module
  explicitly as `agentsuite "github.com/agent-primitives/suite"`; use `kmxsuite`
  for the internal builder package and `suiteoras` for its ORAS adapter.
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
  Trusted-proxy refuses before admission/send. Digest Lift creates absent objects
  only with runtime-matching evidence; occupied primaries/children refuse.
  No Python/RemoteMCP execution, standalone chat/run, installer/discovery or
  retirement is implied. Root destination status is GET-only placement inspection. Preserve Orka defaults and workspace missing-runtime behavior.
- CI retains exact v0.10.3 bootstrap, raw upstream Tool smoke/direct single A2A
  MCP proof, separately tool-free packaged two-case eval/evaluated push and
  source-withheld digest Lift with occupied-repeat refusal. No legacy receipt,
  status or judge claim. Final build/live execution are needed for qualification.

## Candidate evaluation and explicit cleanup

Public Orka workspace eval uses fresh S/run candidates, never adoption/update/reuse.
Full passing evidence exports only after UID/RV-safe cleanup, exact owner-UID child
closure and final source/shared-input checks. KMX-created Provider/Agent/Task carry
full run annotations; Orka descendants do not inherit them. Startup reports only;
unknown reporting never blocks a fresh eval. `eval cleanup [run-id]` lists without
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
Command changes normally require `go test ./cmd/kmx`, affected operation-package
tests, documentation links, repository-map and legacy-runtime checks. These
inventory checkers use tracked files, so include intended additions in the index
when checking the complete change. Update AGENTS.md when implemented conventions
change so subsequent work follows the same rules.
