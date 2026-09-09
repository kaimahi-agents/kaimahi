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

### Added

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
  [docs/migrate.md](docs/migrate.md).

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
  ([docs/spend.md](docs/spend.md#the-two-protocols)).

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
  ([docs/kmx.md](docs/kmx.md#kmx-models-add),
  [docs/spend.md](docs/spend.md#adding-a-model-upstream)).

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
  ([docs/identity.md](docs/identity.md#who-called)).

- **The known imprecision in `acted for` is now written down where a
  reader of the trail meets it.** For a client the plane did not deploy,
  `none` — "there is no person" — claims more than the plane can know,
  because absence-of-run is read as an operator-driven turn. That is
  accepted rather than fixed, on the grounds that no supported
  configuration reaches it: kagent and the inbound bridge are the only
  doors. It is bounded and reversible, and
  [docs/identity.md](docs/identity.md#where-none-is-stretched-and-why-that-is-accepted)
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

- **`scripts/check-board.py`** — the coordination board's lane table said a
  lane was unassigned when it had already shipped five times in six days, and
  twice carried two rows for one lane that contradicted each other. Each time
  it was found by eye and hand-patched. CI now runs the board against itself
  on every pull request: no two rows for one lane, no row that says both
  unassigned and merged, no ready-to-paste worker prompt for a lane the table
  says shipped, no finished lane's delta sheet above a row saying nobody has
  started, and every pull request the board calls merged is one that merged.

  This is not cosmetic. A stale row invites a worker to rebuild something that
  exists — one said unassigned for the lane that had shipped `kmx tools add`.

  Whether a lane is *done* is a judgement and nothing here asks it. Whether the
  board contradicts itself is not, and that is all this checks. One claim
  reaches outside the document, to `git log`: a row claiming nothing has
  shipped, for work a merge already describes in that row's own words. Pull
  request titles carry no lane numbers, so the match is on the words the row
  itself uses; on the board the day it was written it matched 17 rows to the
  exact pull request each cites and none to a different one. It is keyless,
  needs a full checkout, and names the claims it skipped when it does not have
  one.

  The board has one writer, so this lane did not edit it. What it found is
  recorded in `scripts/board-open-drift.json` with what each item costs to
  close — including the stale row that was live on main while this was being
  written. An entry there that the board no longer earns fails the check too,
  so the record cannot outlive the debt.

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
- Documentation and code comments now say what a thing does rather than
  citing the planning identifier that tracked it. A lane or decision number
  is a coordination artifact nobody reading the code can resolve, so each
  one is replaced by the mechanism or the rule it stood for — and where the
  reference was carrying the argument, the reason is written out instead of
  cited. Trailing pointers to a capability document are unchanged; the
  planning board keeps its own identifiers.
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

- **The board checker's self-test only worked while the board was broken.**
  Three of its cases needed a finding standing open in
  `scripts/board-open-drift.json`, so striking the last one off — the day its
  design succeeds — turned the self-test red with nothing wrong. Two of the
  three failed loudly. The third, that a claim skipped for want of a merge
  ledger does not strike its open findings off, passed on an empty ledger
  having compared nothing, and one deliberate breakage went unnoticed behind
  it. All three now build their own contradictory board. The ledger is
  asserted in both directions by name — the finding it recorded is gone, and a
  second one it did not record is still reported — because recording *some*
  problem is not enough: an entry the board no longer earns is itself a
  problem. A fourth case, added here, is the only shape that shows the ledger
  reads the lane and not just the claim: two lanes failing one claim, with one
  of them recorded.

- **A worker prompt whose identifier was not a number was invisible to the
  board checker**, and the one on this board invited a fresh session to rename
  `tomte` to `kaimahi` in a repository renamed six weeks earlier. Both reasons
  are closed for prompts: the identifier pattern used where a row or a heading
  *opens* now admits a worded lane, and a heading that says it is a prompt
  whose lane cannot be read stops the run rather than being filed under
  nothing. A pasteable prompt with no row was also silently exempt from the
  shipped-lane claim; `every_prompt_has_a_row` owns that case, and the claim
  that declines to answer it now says so. On the *row* side the class stays
  open by choice — seventeen of sixty-three rows name no lane the checker can
  read, and reporting them would name seventeen rows the board is not wrong
  about. `Row`'s docstring says so, and says why.

- **A retired prompt now has to cite the pull request its own row cites.**
  Retiring a prompt writes a merge number into its heading by hand, once per
  lane, and sixteen of those numbers are older than the merge ledger's first —
  so the ledger cannot check them, and the document checking itself is the
  only thing that can.

- **The board's whole open drift is closed.** Thirty-eight prompt headings
  invited a paste into a fresh CLI session; thirty-six of them sat under rows
  saying the lane had merged, and each of those now reads `(RUN — merged as
  #N; kept as the record of what the lane was asked for)`, keeping the prompt
  text — which is what makes a lane's delta sheet checkable — and dropping the
  invitation together with the sequencing conditions written beside it in the
  same parenthesis. The two left pasteable, W42 and W43, are for lanes whose
  rows say unassigned and mean it. W41's row, which said `unassigned` for work
  that merged as #139 the same day, says so.
  `scripts/board-open-drift.json` carries no open findings and records how
  each was closed.
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
  [docs/workflows.md](docs/workflows.md).
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
  [docs/release-agent.md](docs/release-agent.md).

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

- **`kmx down` no longer deletes a cluster under a banner saying it does
  not exist.** `kind delete cluster` deletes by **container** name and never
  opens the kubeconfig, so a kubeconfig that has never heard of the context
  is not evidence there is nothing to delete — but the guard's "context not
  created yet" allowance, which exists so `kmx up` can name the cluster it
  is about to create, was accepted here too. A stale or re-pointed
  `KUBECONFIG` plus `kmx down` therefore destroyed a real cluster, with no
  confirmation, under a banner saying it had never been created. It cost a
  lane a cluster.

  `kmx down` now asks the container engine first. No cluster by that name:
  it says so and deletes nothing. A cluster the kubeconfig describes: the
  banner and no question, exactly as before, so CI and every ordinary
  teardown are unchanged. A cluster the kubeconfig cannot vouch for: the
  banner says so, and it takes `KAIMAHI_CONFIRM=<context>` or a typed
  confirmation — which is also how a half-created cluster is removed after
  a `kmx up` that died before writing its kubeconfig entry.
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

```sh
kmx plane                # mints the certificate; the seams become TLS here
kmx govern <credential>  # re-applies the model seam with https + spec.tls
kmx tools govern         # the same for the tool seam
kmx status               # the certificate, and which seams are governed
```

`kmx plane` prints those commands when it finishes, for the same reason they
are written out here: between the first and the rest, calls fail.

A fresh cluster needs nothing extra — `kmx up` is untouched, and `kmx plane`
followed by `kmx govern` is the order it already documented.

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
  ([docs/identity.md](docs/identity.md)).
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
- **A documented upgrade path** ([docs/releases.md](docs/releases.md)),
  including what happens when a migration fails halfway (the plane does not
  start), and a CI job that upgrades a plane across a real schema gap with
  live data in it and proves the data survives.

### Upgrading

- From an untagged `go install …@<sha>` build: install `@latest` and run
  `kmx version`. There is no state in `kmx` itself to migrate.
- For the **plane**, see [docs/releases.md](docs/releases.md#upgrading-the-plane).
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
  reasoning is in [docs/releases.md](docs/releases.md#why-no-published-image-yet).
- **No package-manager namespace is claimed** — no Homebrew tap, no npm, no
  crates, no PyPI. The name is provisional and no trademark opinion has been
  obtained; claiming namespaces would raise the cost of a rename that may
  still happen. See [docs/NAMING.md](docs/NAMING.md).
