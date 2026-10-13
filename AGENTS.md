# Repository guidance

Read [CONTRIBUTING.md](CONTRIBUTING.md), the
[entry-point principles](docs/entry-point-principles.md), and the
[repository map](docs/repository-map.md) before changing command behavior.

## Command consistency

- Follow the [command conventions](docs/command-conventions.md), the implemented
  interface in [docs/kmx.md](docs/kmx.md) and the
  [command alignment record](docs/command-semantics.md). Track outstanding
  renames and decisions in [#301](https://github.com/kaimahi-agents/kaimahi/issues/301).
  Proposed spellings are not implemented capabilities.
- Canonical context commands are `kmx context show` and
  `kmx context use <context>`; canonical local setup/teardown commands are
  `kmx local up/down`. Root `ctx`, `up`, `down`, `lift` and `lift down`
  are removed outright, not compatibility routes or retirement stubs. Current
  bundle deployment remains `kmx bundle lift`; current cloud and runtime setup
  remain `kmx aks up/down` and `kmx orka install/status`.
  The AKS group is a hidden compatibility route, not a public root domain.
  Guided quickstart is `kmx quickstart --interactive` (`-i`); the old wizard
  command is a retirement stub. `kmx chat` is always interactive and no
  longer accepts `--interactive`. `kmx targets` is the read-only capability view.
  Plane administration, model overlays, migration and their demo are removed;
  do not restore or regroup their commands.
- Everyday operations are root `create/chat/run`; live inspection is `orka show/list`.
  Legacy lifecycle operations are `bundle evaluate/lift/retire/verify/status`.
  `kmx agent` is removed entirely, without an alias or retirement stub.
  Root `status` and `orka status` retain the same table-only Orka runtime report;
  do not add root bundle status or a suite observer.
- Root `create <name>` authors one tool-free native suite member offline
  by default: Orka unless `--runtime kagent` selects exact v0.10.3 Go/linux-amd64.
  Runtime is member-owned KMX-private metadata, not a public AgentSuite field.
  kagent author/prepare/load/push enables no suite placement/evaluation/status/gates;
  `eval` and evaluated push explicitly refuse its verified runtime profile.
  Orka-only consumers refuse that profile. RemoteMCP/Python remain unsupported.
  Missing runtime in old source stays Orka only. Create and `push [--workspace DIR] REF` share nearest-ancestor
  `workspace.yaml` discovery; `--workspace DIR` selects an exact root without
  fallback and create initializes a missing marker there without prompting.
  Without a workspace, terminal create asks `Start one here? [Y/n]`; nonterminal
  or redirected use refuses with a `--workspace` hint. Invalid nearest markers
  fail closed. Authoring requires exact `--model` and file-valued `--instructions`;
  a second member refuses without changing authored files. Help/completion is inert.
  Workspace push names its absolute root and prepared OCI manifest digest on
  stderr, then copies that exact graph and authored cases.
  `suite push DIRECTORY REFERENCE` remains extracted-directory transfer without
  discovery and no longer accepts `--workspace`. The native member image
  requirement is empty. Neither offline command evaluates, deploys, builds images
  or loads cluster settings. Workspace evaluation is root `eval`; release lift
  remains unimplemented.
  `create --live` explicitly selects unchanged resource/bundle creation for both
  Orka and kagent, including `--no-apply`, `--dry-run` and `--out -` previews;
  no-name `create --live` is the live Orka wizard. `--runtime kagent` alone authors
  the offline v0.10.3 Go member; live kagent create stays exact v0.10.2. Live-only flags without `--live` refuse before
  configuration or writes; `--workspace` conflicts with `--live`. Quickstart,
  console and direct App.CreateAgent operations remain live. Preserve
  PortableAgent formats and old extracted-directory suite transfer; do not treat public graph validation
  as native execution-profile validation. Private packaged-native preparation and
  ownership-safe Provider/Agent placement feed full-workspace root evaluation;
  the existing Orka CI shard runs a bounded two-case native qualifier against
  the pinned runtime/model, distinct from hermetic command tests.
  Private native passing receipts retain only full ordered passing metadata, export
  once without replacement outside workspace/cache trees, and bind the private
  receipt-file push consumer to exact S plus offline packaged coherence. There is
  a full-workspace native evaluation CLI with one fresh never-retried Task per
  ordered case. Root `push --evaluation FILE` uses that private file consumer;
  ordinary push stays unevaluated. Unsigned receipts do not prove authentication
  or drift absence.
  Any presence of a native suite ownership annotation reserves desired/live
  resources against legacy adoption, copying, bundle authorization and retirement.
  Registry workspace publication shares unsigned suite provenance defaults/flags and records Git source only from
  fingerprint-bound authored inputs matching HEAD; local layouts get no referrers.
- Root `eval` evaluates the full native Orka workspace inventory with explicit
  `--to-context`, `--inference provider:NAME` and new private `--out`, using local
  flags and a nonpersistent operational hook. Unsupported/budget/output checks
  run before config; one fresh outer guard covers pair placement and all Tasks.
  Fail/unknown/drift/cancel/output errors stop later sends with no partial export.
  No model/runtime override, judge/subset/content mode or standalone package
  reference is accepted. Offline `eval diff` and recorded-session preparation
  via `eval from-sessions` retain their cluster-independent hooks; recorded
  execution stays `bundle evaluate --from-sessions`. Root digest lift is not
  implemented. The checkout-only native qualifier runs in the existing exclusively owned
  Orka shard, after legacy model proof, with private captures and metadata-only
  artifacts. A passing run qualifies that fixed workspace scenario; hermetic
  tests alone do not establish runtime/model execution.
  Keep reference manifests private, require named candidate/judge confirmation before recorded-content transfer,
  and suppress answer/reasoning output unless `--show-content` is explicit.
  Expired sources are unknown, never a skip or export. Offline eval diff keeps
  missing Sessions case identity row-local unknown/nonzero; observed identity
  conflicts or no complete observed case identity in either receipt stay incompatible.
- Keep **Create → Prove → Lift** as the local-to-remote story. **Lift** is the
  chosen agent lifecycle verb: retain `kmx bundle lift` and interactive `/lift`,
  and use Lift in corresponding help and menu labels rather than renaming the
  operation to deploy. Deployment remains a technical description of its effect.
  Root `lift` is reserved for a future suite lift workflow, not implemented.
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
- Preserve historical release records and explicit compatibility tests when
  updating current examples. Do not bulk-rename historical text or serialized
  state. Generated recovery commands must preserve selectors and shell quoting.

## kagent version boundary

- Preserve Orka defaults and exact-v0.10.2 kagent create. Bundle lift/status
  requires explicit `--runtime kagent --runtime-version v0.10.3`, `--to-context`
  and author-owned `KagentBindings`. Only Go deployment/status/evaluation is
  qualified; Python remains render-only. Explicit `kmx bundle evaluate --runtime
  kagent --runtime-version v0.10.3` uses the saved placement and desired bindings;
  unsecure only, trusted-proxy refused
  before any send. One fresh blocking native A2A Task per case, never retried,
  with attribution rechecked before judging. Ambiguity/drift stops later sends.
  Dedicated CI qualifies Go lift/status, native MCP and product evaluation;
  deployment receipts still prove readiness, not execution.
  Lift creates new objects or reuses a complete
  matching UID-bound receipt without adoption, update or retry of ambiguous
  writes. Readiness is not execution proof. Offline `eval diff` reads
  strict v2 kagent Go receipts with native A2A identities; valid kagent evidence
  is excluded from Orka gate/status selection. This does not enable promotion gates or Python. Tool assertions stay unknown;
  structured upstream events are not qualified tool evidence. Do not infer
  installation, discovery, standalone chat/run, retirement or console support
  from receipt reading, evaluation or placement.

## Verification

Run checks appropriate to the changed paths as described in CONTRIBUTING.md.
Command changes normally require `go test ./cmd/kmx`, affected operation-package
tests, documentation links, repository-map and legacy-runtime checks. These
inventory checkers use tracked files, so include intended additions in the index
when checking the complete change. Update AGENTS.md when implemented conventions
change so subsequent work follows the same rules.
