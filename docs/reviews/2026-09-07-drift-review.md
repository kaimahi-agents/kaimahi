# Drift review, 2026-09-07

A read-only review of this repository at main `742ac64`, after ten lanes
merged in one week and a repository-wide comment sweep. The question asked
of every comment, document, test and checker was the same: *is this still
true, and how do we know?*

**Method.** Eight parallel read-only investigators, one per scope area, each
bound to the same rule: a finding is CONFIRMED only when a command was run
and its output read (a `--help` page, a `go test -run`, a grep against the
tree, a dry-run, a mutated copy of a script under its own self-test); a
finding from reading alone is SUSPECTED. The lead re-ran every headline
item independently before including it here. Line numbers are as of
`742ac64`, before the mechanical commit in this PR.

**What was fixed here, and what was not.** One commit in this PR carries
the mechanical corrections (listed at the end). Everything with judgement
in it is a finding below and was deliberately left alone. No behaviour was
changed. `go test ./...` in both modules, `go vet`, `gofmt` and every
checker pass exactly as they did before.

**Two incidents during the review, disclosed.** One investigator ran
`kmx down` with an empty `KUBECONFIG` and stdin from `/dev/null`, expecting
the guard to refuse. It proceeded and ran `kind delete cluster --name
kaimahi-p1` (14:17:42 to 14:18:12 UTC). No cluster existed: `kind get
clusters` was empty before and after, no kind node container existed or
had exited, and `docker events` for the window shows nothing. That
non-refusal is finding A9. Separately, an investigator ran `kmx ctx
kind-zz-nope`, which wrote `~/.config/kmx/context` on this workstation;
that file was not removed by the review and should be deleted by hand.

---

## A. Claims that are no longer true (highest value)

Ordered by how badly a reader relying on the sentence would be misled.

### A1. Three documents say kmx never accepts a credential. `kmx credential capture` does.

CONFIRMED. `docs/getting-started.md:222-223` ("`kmx` never accepts a
credential in any form"), `docs/operations.md:18-20` ("a captured key that
`kmx` deliberately has no way to accept"), `docs/entry-point-principles.md:89-93`
("Credential capture stays in dedicated scripts that read from stdin"),
`docs/development.md:38,233-236`, `docs/tools.md:148,354` ("Secret captured
stdin-only"). The same falsehood is printed at runtime by
`internal/kmx/app/lift_steps.go:163-165` ("kmx accepts credential material
on no path that exists today") and stated in `internal/kmx/app/views.go:73-75`.

Evidence: `./bin/kmx credential capture --help` says "read from the
terminal … typed at a prompt with the echo off"; `echo x | ./bin/kmx
--context nosuch credential capture github o/n` is refused ("this is not
one"), so the stdin claim is doubly wrong. The three scripts those docs name
(`github-secret.sh`, `release-secret.sh`, `ado-secret.sh`) were deleted in
the commit that added the command. `model-secret`, `slack-secret`,
`inbound-secret` and `copilot-secret` are still stdin scripts, so the rule
is split and no document says so.

Fix: one sentence per site. "`kmx agent create` refuses anything key-shaped;
the one path that accepts a credential is `kmx credential capture`,
terminal-only, and the model, Slack and inbound keys are still stdin
scripts."

### A2. `docs/isolation.md` says only option A is built. The WASM tool sandbox ships.

CONFIRMED. `docs/isolation.md:3-5` ("option A is built … B–E are not") and
its option table list a WASM sandbox around tool execution as unbuilt.
`kmx tools sandbox` (`internal/kmx/app/sandbox.go`) applies
`k8s/wasm/runtime.yaml`: namespace `kaimahi-wasm`, RuntimeClass
`wasmtime-spin-v2`, and a privileged, hostPID, host-root-mounted DaemonSet.
No document under `docs/` or the README mentions the command at all
(`grep -rn "tools sandbox" docs README.md` outside the board is empty), and
`docs/kmx.md`'s command table has no row for it or for the whole
`kmx workflow` family.

Fix: an "L3 today" section in isolation.md naming the RuntimeClass, the
privileged installer and the no-prompt-on-kind behaviour; rows in kmx.md
for `tools sandbox [status]` and `workflow list|show|govern|run`.

### A3. `docs/kmx.md:247` says every mutating command is guarded. Two are not.

CONFIRMED. `kmx workflow run` (files requests, drives turns that cut
branches and dispatch builds, runs `ungoverned:` scripts) goes preflight to
`admin.Open` with no `a.Guard` call; its only "guard" reference is a
blueprint step guard. `kmx credential renew` is deliberately unguarded
(`views.go:73`). `grep -rln 'Guard(' internal/kmx/app/*.go` lists sixteen
files; neither is among them.

Fix: guard both, or name them as the exceptions.

### A4. The README overclaims twice on the same page

CONFIRMED.
- `README.md:226-227` "Internet-facing tool upstreams remain unbuilt",
  while line 213 says GitHub's hosted MCP server runs and
  `k8s/egress-hosted.yaml` plus the hardened dialer exist. Three hosted
  upstreams are in the committed table.
- `README.md:258` "Every control is one make target, and each is asserted
  in CI". `grep -nE "make (slack-secret|slack-mcp|govern-slack|release-secret|govern-release|release|github-secret|govern-github)( |$)" .github/workflows/ci.yml`
  returns nothing; CI applies the Slack manifests with kubectl directly and
  never runs the release agent (`k8s/release-agent.yaml:28-29` says so).
- `README.md:224-226` "the Slack pod is the one thing allowed out, on 443
  only": the policy also allows DNS on 53, and the proxy gains 443 when a
  hosted upstream or Copilot is opted in. `docs/egress.md:29-31` has it
  right.

### A5. `docs/getting-started.md`, `docs/spend.md` and `docs/entry-point-principles.md` describe a kmx that no longer exists

CONFIRMED. `getting-started.md:139-142` and `spend.md:113-114` say budgets,
approvals, secret capture and AKS "are still the Makefile's";
`entry-point-principles.md:175-207,311-318` says milestone 1 is "in flight",
milestone 3 "not scheduled", installation is `go install …@<sha>`. Root
`--help` lists `budget`, `approvals`, `approve`, `deny`, `request`,
`credential`, `lift`, `backup`, `restore`, `workflow`; v0.1.0 is tagged and
`install.sh` exists.

### A6. CHANGELOG's Unreleased section omits most of the week

CONFIRMED. Missing: `kmx tools add`, chat `--interactive/--session/--json`,
`kmx tools sandbox`, `kmx status` governed counts and `-o json|yaml`,
`kmx flow`, BYO `agent create --image/--isolation/--run-as-user`,
`kmx lift`/`lift down`, the version handshake and `kmx ctx` refusing an
unchosen context, the Go-proxy wait fix, agent-pod hardening. The
`kmx status -o json` envelope change belongs under **Breaking**:
`docs/kmx.md:210` itself says it "can no longer be piped into
`kubectl apply`", and it post-dates v0.1.0.

### A7. `docs/workflows.md:28-30` says "Nothing here needs a checkout". The release sequence does.

CONFIRMED. `kmx govern` issues `hello-world`; `kmx workflow govern release`
refuses without credential `release-agent` (`internal/kmx/app/workflow.go:437`),
and that Agent and its RemoteMCPServers are applied only by
`make govern-release` from a checkout (kmx embeds `blueprints/` only,
`embed.go:76`).

### A8. `docs/ap-demo.md:202` tells the reader to run `make use AGENT=ap-agent PRESET=governed-copilot`. It switches hello-world.

CONFIRMED. `Makefile:730-731` is `$(KMX) use $(PRESET)`; `AGENT` is not in
`KMX_ENV` (`Makefile:105-111`) and kmx reads no `AGENT` variable
(`grep -rn '"AGENT"' internal cmd` hits only a test); `kmx use --agent`
defaults to `hello-world`.

Fix: `kmx use governed-copilot --agent ap-agent`, or thread `--agent $(AGENT)`
through the recipe.

### A9. `kmx down` prints "context not created yet" and then deletes by name, with no confirmation

CONFIRMED by execution (the incident above). `internal/kmx/guard/guard.go:155`
classifies an absent `kind-*` context as `Local` with label "local kind
(context not created yet)", the allowance meant for `kmx up`;
`internal/kmx/app/down.go:22-25` accepts that and runs
`kind delete cluster --name <KIND_CLUSTER>`. kind deletes by container
name regardless of kubeconfig, so a misconfigured `KUBECONFIG` plus
`kmx down` deletes a real cluster under a banner saying it is not created.
The package doc (`guard.go:14-16`, "An unknown context … refuse[s] rather
than guess") does not disclose the kind-name exception; the `Classify`
comment does.

Fix: in `Down`, refuse when `Classify` reports the context absent.

### A10. `kmx tools sandbox status` says "not installed" for a cluster it never reached

CONFIRMED by execution. `./bin/kmx tools sandbox status --context nosuch-ctx`
prints three "not installed / none" rows and exits 0. `unreachable()`
(`sandbox.go:118-142`) matches the substring `context does not exist`;
kubectl prints `context was not found for specified context: nosuch-ctx`
for this query and `context "x" does not exist` (name in between) for
others. The unit test (`sandbox_test.go:22`) fabricates wording kubectl
never emits, so it passes while production fails. The same function maps
an RBAC `Forbidden` to "not installed". The file's own comment calls this
"the one failure mode a governance read must not have".

### A11. `kmx workflow run --dry-run` says "Nothing is created". A seam refresh still applies a Secret.

CONFIRMED by code order, not executed (needs a cluster). `do()`
(`workflow_run.go:269`) calls `refreshFor` before the kind switch and before
either dry-run stop (`:402`, `:433`). The release blueprint's first step is
a turn step with no upstream, so `refreshFor` re-mints every seam that
declares `refresh:` (`blueprints/release.yaml:213-219`, the ado seam) and
runs `kubectl apply -f -` on a Secret (`:973`) whenever `az` is on PATH.

Fix: skip `refreshFor` when `opt.DryRun`, or say "nothing is created except
a refreshed credential".

### A12. `kmx tools add` no longer refuses two committed upstream names

CONFIRMED. `internal/kmx/scaffold/upstream.go:269-271` hardcodes
`kagent-tools, slack, github, erp` as "the committed table's own four";
the table has six (`github-release`, `ado` added). An overlay named `ado`
passes the scaffolder and collides at proxy boot, the late failure the
comment says it prevents.

### A13. Gateway comments say a grant admits only a digest-matching call; the store honours legacy NULL-digest grants for any call

CONFIRMED. `plane/internal/gateway/gateway.go:71-72,398` and
`digest.go:203-206` state absolute binding; `store/approvals.go:402-403`
selects `arg_digest = $3 OR arg_digest IS NULL` and
`TestLegacyVerbLevelGrantIsHonouredAndComesLast` (run against a throwaway
Postgres) asserts a grant is consumed for a different call's digest.
The README now discloses this class (`README.md:52-54`); the gateway
comments, `docs/release-agent.md:214-215` and `docs/ap-demo.md:283` do not.

### A14. `scripts/release-run.sh:556` prints a claim its own design refutes

CONFIRMED. The closing banner says "Every consequential call was denied
first, approved by a human against the exact call". `consequential()`
(`:275-319`) files the request itself; lines 255-270 explain that the agent
is wired no such tool, so no denial happens.

### A15. Five agent manifests say `runAsUser: 1001` is "pinned by a CI assertion". No such assertion exists.

CONFIRMED. `k8s/ap-agent.yaml:55`, `github-agent.yaml:40`,
`hello-world.yaml:40`, `slack-agent.yaml:48`, `tools-agent.yaml:46`.
`grep -n 1001 .github/workflows/ci.yml Makefile scripts/*.sh` hits only an
ADO pipeline-id example. The only `1001` in tests pins kmx's own template.

### A16. `identity.go` and migration 00009 say `run_id` is empty unless a person acted

CONFIRMED. `plane/internal/store/identity.go:44-46` and
`00009_run_identity.sql:69-72`; contradicted by `identity.go:100` and by
`TestARunWithNoPersonIsValidAndDistinguishableFromALostOne`, which asserts a
`none` attribution carries its run id. `00008_argument_binding.sql:28-30`
("rows that predate this migration are the only ones a NULL can describe")
is true only for `kind='tool'`; budget and inbound grants are NULL by design.

### A17. Stale operational facts in code comments (kmx)

CONFIRMED, grouped.
- `internal/kmx/app/plane.go:28-30,189-191`, `preflight.go:26`, `up.go:22-23`:
  "only a REGISTRY target renders, and that is the script's job", "only
  `kmx plane` needs Go", the kind `UP_STEPS` list. `kmx lift` renders the
  registry image and needs Go; the Makefile's kind `UP_STEPS` is empty.
- `internal/kmx/app/lift_observability.go:292-307`: `enableMetricsAddon`
  promises a refusal its body does not make (it prints and returns).
- `internal/kmx/app/lift.go:17-21,331`: "Every one of them is overridable
  by a flag" (the node disk size is not); `lift/plan.go:47-48` "contacts
  nothing" (tool preflight and `az account show` run before `--plan`).
- `internal/kmx/toolchain/provision.go:21,60-64`: reports "cache" for a
  binary it re-downloaded after a failed re-verification.
- `internal/kmx/app/chat.go:33-49` says no kmx command performs a
  non-idempotent action; `workflow_run.go:321` drives release steps through
  the same ambiguous-class retry.
- `internal/kmx/admin/flow.go:165`: the saturation note always names the
  50-row limit, even when the 200-row inbound trail saturated.
- `internal/kmx/scaffold/agent.go:283-285`, `app/create.go:155-156,367`,
  `use.go:35-38`: scaffolded manifests still say `make plane`/`make govern`
  and "milestone 2 of kmx will own that seam".
- `cmd/kmx/root.go:116-123`: `kmx version` Short says "installed component
  versions"; it prints compile-time pins with no cluster consulted.
- `cmd/kmx/tools_commands.go:31`: "the WASM sandbox that tool calls execute
  in"; nothing kmx writes sets `runtimeClassName`, so installing sandboxes
  zero calls until a server opts in.
- `internal/kmx/guard/guard.go:4-16,48`: "CurrentContext is read but never
  acted on" (a refusal decision at `:245` depends on it); "the contract is
  the shell script's, unchanged" (the nobody-chose rule is kmx-only).
- Misattached godoc: `chat.go:59-72` (on `ChatJSON`), `admin.go:323-331`,
  `blueprint/render.go:87-98`, `lift/record.go:179-192`, `up.go:182-202,443-456`;
  `blueprint.go:328-330` and `render.go:90-91` name a `Rendered.Check` that
  does not exist.
- Stale counts: `chatview.go:24-25` "eight places" (14 verify-chat calls),
  `config.go:9-10,50,59` cites a Makefile `GOVERNED_SECRET` that is not
  there, `lanes.go:10-23` describes an Ollama/kagent lane pair that is never
  built.

### A18. Stale operational facts in code comments (plane, k8s, scripts)

CONFIRMED, grouped.
- `plane/internal/gateway/gateway.go:6-8,606`: upstreams come "only from
  the committed … table"; the overlay ConfigMap adds them
  (`TestAnOverlayAddsAToolUpstreamWithoutTouchingTheCommittedOnes`).
- `plane/internal/redact/redact.go:15`, `slog.go:9`: cite a
  `secrets.Resolver.AllValues()` and a "vault" that do not exist.
- `plane/internal/notify/poster.go:258`: sends `"version": "p8b"` over the
  wire as the plane's clientInfo. A planning identifier as a value; the
  fix is `metrics.Version()`, which is a behaviour change and so is left
  as a finding. Other planning identifiers surviving as values:
  `OWNER_TAG_VALUE=p5b` (`scripts/aks-up.sh:74`, `aks-down.sh:28`, and
  load-bearing: aks-down refuses a group without it), `PLANE_IMAGE_TAG ?= p10`,
  `kaimahi-proxy:p15`.
- `scripts/plane-deploy.sh:8-21`: describes a kind path nothing calls (kind
  `make plane` is `kmx plane --source .`).
- `k8s/kaimahi-tools.yaml:3` "the only entry", `k8s/plane/proxy.yaml:205`
  and `plane/internal/config/overlay.go:5` "four upstreams",
  `scripts/ci/plain-upstream.sh:8-9`, `plain-mcp-server.py:7,12`,
  `k8s/plane/network-policy.yaml:32,73` (Copilot host "twice"; "the three
  in-cluster upstreams" while the rule also opens ERP 8085).
- `k8s/plane/upstreams.yaml:41-44`: the standing-constraint summary omits
  the payee clause the injection demo relies on.
- `scripts/slack-secret.sh:7-10`: the bot Secret reaches "the MCP pod
  only"; `proxy.yaml:267-270` projects its channel key into the proxy.
- `scripts/kube-guard.sh:38-43`: explains `make chat`'s exemption with a
  `$(KUBECTL)` mechanism that no longer exists.
- `plane/internal/store/identity.go:172` "the gateway has five" audit
  points (four); `validate.go:29` "around 3KB" (6.3KB); `plane/Dockerfile:8-9`;
  `scripts/aks-up.sh:78-79` usage omits `AKS_NODE_OSDISK_SIZE`.
- `docs/hosted-upstreams.md:114` "60 s to start answering" omits
  `EGRESS_HEADER_TIMEOUT` (`wire.go:44-52`, up to 10m, in no doc).
- `plane/internal/proxy/handler.go:409`: the hosted cap is 8 MiB
  (`egress.go:53`), not the 50 MiB buffer. Corrected in this PR's comment;
  whether 8 MiB is the intended bound for hosted Copilot completions is a
  question for the owner.

### A19. Doc facts contradicted by other docs or by CI

CONFIRMED.
- AKS run count stated four ways: `README.md:192` "once", `README.md:236`
  "one verified run on 2026-09-01", `docs/aks.md:21` "two",
  `docs/README.md:84` "three"; aks.md's own section records runs on 09-01,
  09-02 and 09-06.
- `docs/development.md:111-127`: "four shard jobs"; `ci.yml:3625` needs six
  (`e2e-ap`, `e2e-quickstart` missing) and `kmx-clone-free` (`:3644`) is a
  third clusterless job the table omits.
- `docs/development.md:54-69` and `CONTRIBUTING.md:18-29` list five checkers
  as "the CI hygiene job"; the job also runs the release-notes and
  verify-chat self-tests and eleven inline checks. Nothing the docs list is
  absent from CI.
- `docs/development.md:44,52` and `CONTRIBUTING.md:27`: the plane test
  command passes vacuously without Postgres. All 23 `plane/internal/store`
  tests skip without `KAIMAHI_TEST_PG_DSN` and neither doc names it.
- `docs/ap-demo.md:281` vs `:463`: two digests for the same approved call
  (`5f2c1e8a44b7` vs `e533a844d950`; the gateway's `Bind` over the doc's
  call gives the second).
- `docs/egress.md:19-36` says "Everything not in the table is denied" and
  omits proxy to controller 8083, proxy to ERP 8085, and the two 9092 scrape
  allowances. `docs/inbound.md:490-493` "nothing else in the kagent
  namespace is reachable from the plane" omits `kagent-tools` 8084.
  `docs/inbound.md:41-47` says three hooks ship; four are configured
  (`slack-tools`).
- `docs/slack.md:118-123` "a second `tool_upstreams` entry … the one marked
  entry"; `docs/identity.md:137-140` implies `make govern`/`govern-tools`
  take a lifetime (only `kmx govern --ttl` does; `kmx tools govern` has no
  `--ttl`).
- `docs/release-agent.md:23-27,263-268`: omits the publish step and the
  real `STEP` set (`Makefile:1483` help lists four of eight).
- `docs/kmx.md:217-219` and `README.md:342,402`: "`kmx agent list` says so
  and prints the commands" for kubectl read/update/delete; `agent_list.go`
  prints a table only.
- `docs/getting-started.md:169-176`: "`make chat` prints the raw A2A task
  JSON"; a TTY gets the readable view, raw only when piped or `--json`.
- `docs/isolation.md:130`: "`--image` carries the governance seams across …
  prints exactly what it injected"; `GovernanceEnv` returns nil unless
  `governed-ollama` is readable on the cluster, and the emitted manifest
  then has no `env` at all.
- Overlay custody refusals: docs name four fields
  (`tool-governance.md:165`, `govern-your-agent.md:347,357-358`); the plane
  refuses five (`overlay.go:74`, `extra_headers` too), and
  `hosted-upstreams.md` tells operators to add `extra_headers`, which an
  overlay cannot.
- `cmd/kmx/commands.go:263-265`: `kmx approve` help omits the plane's
  bounds (ttl at most 30d, uses at most 1e6, amount only on budget
  requests); `--ttl 60d` round-trips as a bare "bounds out of range".

SUSPECTED (reading only): `docs/getting-started.md:85` "2m43s" against the
178s the CHANGELOG measured; `docs/hosted-upstreams.md:78` "The script
proves…" (now Go); `demo.md:13` and `entry-point-principles.md:16-18` still
list kind/kubectl/helm as prerequisites; `release.yml`'s checksums comment
misquotes `releases.md` (the doc is right); `plane/internal/notify/poster.go:445-446`
classifies `MsgUpstreamRefused` as ambiguous and never retries it if the
notifier is ever pointed at a hosted upstream, which `config.go:407-420`
does not forbid.

---

## B. Tests that cannot fail, or pass vacuously

CONFIRMED unless marked.

- **`kmx workflow run`'s poll, bounded and propose drivers are exercised by
  nothing** (`internal/kmx/app/workflow_run.go:273-279`). The CI fixture
  `scripts/ci/workflow-fixture.yaml` has two step kinds and two `when:`;
  `blueprints/release.yaml` has five kinds and five `when:`. No test file
  names `pollStep` or `boundedStep`. Fix: a poll and a bounded step in the
  fixture, or a Go test driving `do()` over a fake plane for each kind.
- **`scripts/verify-chat.py` never exercises `state == "completed"`**: every
  non-completed fixture also has an empty reply, so deleting the state check
  passes the self-test. The `function_call` half and tool-name matching are
  likewise untested (a response with no call, or under another tool's
  name, passes). The nested `hitl_parts` shape `chat_question.go:71` parses
  has no fixture. Ten of thirteen mutations went unnoticed.
- **`scripts/check-no-azure-ids.sh:143`** GUID entropy exemption: widening
  `<= 4` distinct digits to `<= 15` (which exempts 93% of real GUIDs)
  passes the self-test, because the one refused fixture uses all sixteen.
  Also uncaught: dropping the empty-set refusal, skipping unreadable files,
  checking only the first `placeholder_path` component.
- **`scripts/kube-guard-test.sh`**: no other-name plus loopback case, so a
  mutant that drops the name check calls `docker-desktop` on 127.0.0.1
  "local kind". The unreadable-kubeconfig refusal has no case.
- **`scripts/check-readme-front-door-test.py`**: no failing case for the
  hero image, product line, spend outcome, `kmx agent chat` or `kmx govern`
  markers, nor for command order or a mid-line command. Eleven of fifteen
  mutations uncaught.
- **Three self-tests test the function, not the script**: front-door,
  kmx-delegation and verify-chat self-tests stay green when only `main()` /
  `check()` / `__main__` is neutered. The Azure scanner and release-notes
  self-tests run end to end and caught the same mutation.
- **`plane/internal/proxy/version_test.go:37-42`**
  `TestTheContractNeverGoesBackwards` compares `AdminContract` to
  `AdminContractInitial`, and `version.go:56` defines one as the other.
  Cannot fail for any value.
- **Migration lower bound of 7** in `.github/workflows/ci.yml:1694` and
  `plane/internal/store/store_pg_test.go:97`; there are ten migrations.
- **`plane/internal/metrics/metrics_test.go:204-216`** hand-lists 28 of 30
  `Reason` constants (`ReasonConstraint`, `ReasonCredentialExpired` missing,
  both used live); the label-shape test draws reasons from `Vocabulary`
  itself.
- **`cmd/kmx/root_test.go:56-73`** omits `flow`, `credential capture` and
  all four `workflow` subcommands; `args_test.go:41` checks six of ~30.
- **`internal/kmx/app/chat_slash_test.go:80-93`** iterates a literal list
  and already misses `/quit`, which `chat_interactive.go:457` dispatches and
  `chat_slash.go` does not list.
- **`internal/kmx/admin/flow_test.go:273-289`** saturation test builds a
  60-row slice and compares its length to two constants; `collect` is never
  called. `:301-305` proves "absent prints as `-`" with `Contains(out, "-")`
  against output that always contains `-`.
- **`internal/kmx/app/cluster_test.go:75-77,139-141`**: the "kmx went on to
  create a cluster" assertion cannot fire (`Echo: false`, stub never
  prints it).
- **`internal/kmx/lift/plan_test.go:196-205`** `TestTheBannerNeverPrintsASubscriptionID`:
  `Banner` takes no id.
- **`internal/kmx/app/capture_test.go:129-140`** "anything left on disk"
  scans a directory capture never writes to (`KMX_HOME`/`TMPDIR` not
  pointed there). `:217-253` "piped stdin is refused" fires on the `a.Err`
  not-a-file clause first.
- **`internal/kmx/app/lift_test.go:174-190`** checks a hard-coded metric
  list, not the workbook. **`quickstart_test.go:33-48`** asserts a literal it
  wrote. **`backup_test.go:143-146`** asserts no `"About to"`; the banner is
  lowercase. **`manifests_test.go:408-434`** says `egress-copilot.yaml` is
  applied by no kmx command; `kmx lift` applies it from the second embed FS
  the test never reads. **`delegation_test.go:37`** "every target the
  Makefile delegates" misses `credentials` and `credential-renew`.

SUSPECTED: `scripts/release-notes.py` `## ` anchoring untested and
`assert`-based (stripped under `python3 -O`; CI runs plain python3);
`ci.yml:618-650` asserts `ok` per package, which an all-skipped package also
prints (none does today).

Nothing found: no `t.Skip` that always fires in CI; no e2e step trusting a
message without an independent state check; no `grep -q` matching prose
instead of the artifact; every all-reject table has an accepting
counterpart; the `equivalence_test` guard is now `len(want) < 5`.

---

## C. The checkers themselves

Each was run against the real tree and against deliberately broken copies.

- **"No secrets in tree" (`ci.yml:46-59`) covers three shapes.** The grep
  matches the Anthropic and OpenAI key prefixes and a quoted `api_key`
  assignment, and nothing else. A fake `kmh_`, ADO PAT,
  Copilot token, `xoxb-` or `xapp-` in `docs/leak.md` passed it. GitHub
  `ghp_`/`github_pat_` are caught only by a grep inside the hosted-upstreams
  step (`:517-521`); Slack `xox[bpca]-` only in the `e2e-tools` shard
  (`:2268`), which is docs-only-guarded. `docs/slack.md:348` therefore
  overclaims "no `xox[bpca]-` token shape anywhere in the tree": a docs-only
  PR never runs it. The scaffolder (`scaffold/agent.go:173-183`) and
  blueprint loader (`blueprint/credentials.go:28-36`) each carry fuller
  lists; CI's is the narrowest of three. Fix: one hygiene-level grep built
  from the scaffolder's list.
- **`check-kmx-delegation.py`** reports "0 targets delegate" as success
  (`:196-209`, an emptied `OWNED` exits 0); its docstring names ten targets
  against 37 in code; its self-test comment (`:231-232`) names an exemption
  the file says it removed.
- **`check-doc-links.py`** checks inline Markdown links and anchors (verified
  against a dead self-anchor) but not HTML `<img src>` or reference-style
  links, which is how the README's two hero images are written. `ci.yml:73-76`
  says "every relative Markdown link".
- **Holds, with evidence**: brand assets (6/6 mutations caught, including a
  CRC-resigned flipped byte); Azure identifiers (7 classes caught, self-test
  catches a stubbed scanner; the entropy gap is in B above); kube-guard
  (always-allow and always-refuse both caught; the name-half gap is in B);
  release-notes (exit codes asserted through `main()`); every inline ci.yml
  meta-check: docs-only guard removed or rewritten as `always() ||`, shard
  dropped from `needs`, aggregator `always()` removed, checkout added to
  clone-free, `make` in the aggregator, a new `e2e-extra` shard not needed,
  registry render policy flipped, committed image tag changed, a private
  range dropped, default-deny removed, edge ingress moved, an upstream
  unmarked, the AP bound raised, `secrets.FOO` or `contents: write` added,
  Unreleased heading renamed, LICENSE deleted, a broken YAML added. All
  caught. One designed blind spot: a new cluster job whose name does not
  start with `e2e-`.

---

## D. Dead and orphaned things

- **`bin/kmx` does not relink when twelve of its embedded files change**
  (`Makefile:96-101` `KMX_ASSETS` vs `embed.go:51-105`). Not prerequisites:
  `k8s/wasm/runtime.yaml`, `blueprints/release.yaml`,
  `scripts/release-publish.sh`, the three `k8s/observability/*` files,
  `k8s/egress-copilot.yaml`, and five AKS scripts. An edit to any of them in
  a checkout leaves a stale binary that `make sandbox`, `make lift` or a
  blueprint run applies. `delegation_test.go:345-354` guards this by
  asserting two hard-coded names. CI is unaffected (fresh runner).
- **`make ap-ask` downloads `bin/kagent` it never uses** (`Makefile:1616`
  prerequisite, then delegates to `make chat`, which calls kmx). The comment
  at `Makefile:1196-1199` ("in a checkout it is handed this one") is false
  for a plain `make chat`: `KMX_ENV` forwards `KAGENT` only when its origin
  is command line or environment (`:108`), so kmx fetches its own copy.
  `command make -n ap-ask` shows the curl and no `KAGENT=`.
- **`RefuseUnknownAgentVerb`** (`internal/kmx/app/create.go:375-388`) has
  no caller, not even a test; Cobra handles unknown verbs, so the kubectl
  hint it prints is never shown.
- Exported, test-only: `ChatRetryableSafe`, `toolchain.Fetchable`,
  `config.LoadDir`, `store.RunByID`, `inbound.Sign`. `seamverdict.Fresh`
  has no callers at all.
- Nothing found: no orphaned scripts (all 55 referenced outside the board),
  no make target naming a missing file, no manifest nothing applies, no
  embedded-but-unread or read-but-not-embedded asset, no orphan doc, no
  dead environment variable. staticcheck U1000 could not run (the installed
  binary predates Go 1.26).

---

## E. Duplication and contradiction within docs

Covered in A19 above (AKS run count, shard count, upstream/hook counts,
constraint clauses, digests, table columns). Additionally, five docs paste
table excerpts missing columns the CLI now prints (`acted for`,
`created (UTC)`, `expires (UTC)`, `binds`): `docs/ap-demo.md:263-281`,
`spend.md:145-148`, `demo.md:78-80,130-134,235-238`, `inbound.md:172`.

Checked consistent across every site: ports 9091/9092/19091/19092, kagent
0.9.12, `qwen2.5:3b`, `kaimahi-p1`, every Secret name, table names against
migrations, "four audit trails", "one prerequisite", `--step` lists, `kmx
status` `none`/`unknown` wording, credential TTL bounds, the toolchain pins.

---

## F. Overclaiming in user-facing text

The README items are A4; the guard claim is A3; the credential claims are
A1. Beyond those:

- **`kmx tools sandbox` on a kind cluster.** The command prints "This
  writes a containerd shim onto every Linux node with a PRIVILEGED
  DaemonSet … Nothing else in Kaimahi asks for these rights" and then
  applies it. On any `kind-*` context the guard returns before any prompt
  (`guard.go:246-248`), TTY or not; the command has no flags. The
  "nothing else" sentence is true (`grep -rn privileged k8s/` hits only
  `wasm/runtime.yaml`), and the behaviour matches the documented guard
  contract for local clusters. It is still a privileged, hostPID,
  host-root-mounted node install riding the same no-question path as
  `kmx up`, and the text omits hostPID and the root mount. The board's
  batch-verification sheet saw the same thing live and declined to file it
  as a defect. This is a judgement call for the owner: a typed confirmation
  for this one command regardless of posture, or leave it and say so in the
  docs that do not yet mention the command.
- Every other enforcement sentence spot-checked (sixteen "refuses / never /
  cannot" claims across gateway, inbound, capture, guard, overlay, approve
  bounds, lift `--byo`, credential expiry) traced to enforcing code, most
  to a pinning test. No opt-out flag exists in any help page; `KAIMAHI_CONFIRM`
  is the only override and is documented.

---

## Board entries contradicted by code

Not edited; for the coordinator.

- The state-of-the-world row for the chat flake lane reads "unassigned |
  SHAPED"; the work merged as #122.
- Rows for #112, #114 and #118 read "coordinator verification owed" while
  the batch-verification sheet dated the same day closes all three
  (`kmx workflow run` starts, the identifier cleanup, the context guard).
- The same sheet already records the `kmx tools sandbox` no-confirmation
  behaviour ("the guard announces, it does not gate") and chose not to
  file it as a defect; item F below is the same observation from the code
  side, with the two facts the banner omits.

---

## What this PR changes

One commit, "Mechanical drift fixes found by the review", fourteen files,
no behaviour change. Each item is a one-line factual correction verified by
the lead:

1. `cmd/kmx/workflow_commands.go`: `--admin-port` usage no longer renders
   its value type as `kmx approve`.
2. `docs/kmx.md`: thirty-seven delegating targets, naming `credentials` and
   `credential-renew`; no `quickstart --agent`; v0.1.0 is released.
3. `README.md`: v0.1.0 is released.
4. `docs/tool-governance.md`: NetworkPolicy is built; six upstreams.
5. `k8s/plane/upstreams.yaml` header: six tool upstreams, four hooks.
6. `docs/approvals.md`: the shipped constraint carries both clauses.
7. `docs/slack.md`: the self-contradicting "egress is not enforced" bullet.
8. `k8s/kaimahi-release-github.yaml`: `X-MCP-Toolsets` is the ado entry's.
9. `plane/internal/metrics/metrics.go`: the label is `version`.
10. `plane/internal/proxy/handler.go`: 8 MiB hosted cap, 50 MiB in-cluster.
11. `internal/kmx/app/workflow_run.go`: the deleted scripts comment.
12. `scripts/plane-deploy.sh`, `inbound-probe.sh`, `release-run.sh`: a
    stale example tag and two usage lines.

Verification after the commit: `gofmt -l` clean in both modules, `go vet`
clean in both, `go test -count=1 ./...` green in both (17 root packages, 13
plane packages), and all ten checkers pass with the same output as before
the review began. The store's Postgres-backed tests were run once by an
investigator against a throwaway `postgres:16` container and pass.
