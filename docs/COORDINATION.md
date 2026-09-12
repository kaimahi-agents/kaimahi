# Coordination

Live decisions and lane register, refreshed 2026-09-10 from user rulings,
GitHub PR/issue metadata and local branch metadata. Base: `572f3a6` (#173).
Archived prompts, delta sheets and decision history are retired to Git history.

## Mission

**The goal is for running agents on AKS to be easy. Kaimahi is a means to
that, not the end.** Success is easier agent operation, not adoption of this
repository. A capability landing upstream is a win; upstreaming is forward
motion, including when it removes the need for code here.

Leadership goal, verbatim:

> "a template for an agent that creates a hello world agent running on a k8s
> cluster, then expand to leverage llm to enhance the agent, allow connectors,
> etc — use a simple cli to get the agent running on k8s"

> "having an artifact that shows my agent topology — almost agent as code
> (ideally yaml template or something like that)"

CLI before UI. Simplest possible solution. Orka is the platform; Kaimahi is
tooling to get agents onto Orka, not a competing governance platform.

## Prime directive

**DO NOT REBUILD WHAT EXISTS.** Before building a component, survey what
already exists and justify net-new work in writing in the PR. Prefer upstream
capabilities and reuse over a parallel implementation. Where reuse is relevant,
evaluate the archived project's verified working Go stack before rewriting it.

Use [CONTRIBUTING.md](../CONTRIBUTING.md) for current safety and contribution
process, and [entry-point principles](entry-point-principles.md) for the CLI
entry-point contract. Historical numbered decisions are not a second process.

## Binding rulings and open decisions

- **Orka owns the platform.** Kaimahi's purpose is onboarding, migration and
  the tooling needed to reach it. Repository success is measured in outcomes,
  not a mandated removal of the repository.
- **Migrated-app governance means model traffic only.** The application owner
  retains its Deployment. Do not imply that model routing transfers application
  lifecycle, tool execution or other enforcement to Orka.
- **The seam is a bridge, not a destination.** The seam shrinking to nothing
  is success. Do not grow a second platform around it. `orka.harness.v2` was
  rejected; it is not a target.
- **Native-Orka-only versus kagent YAML authoring remains OPEN.**
  `docs/orka.md` is a recommendation, not a user ruling. Research PRs merging
  do not turn that recommendation into settled policy.
- **OTLP upstream candidate CLOSED.** Orka already ships OTLP with GenAI
  conventions; do not reopen the candidate as missing upstream functionality.
- **No inferred assignments.** A branch name proves a lane exists, not that
  someone owns a new task. Runtime-assessment contents were not consulted.
  Post-W58 session assignments and dispositions remain unknown without evidence.

## State of the world

The original 67 lane identities and their merged PR references are retained,
without archived instructions or verification narratives. MERGED describes a
PR, not a fresh execution check or authorization for follow-up work. Historical
owner labels identify the recorded worker, not a current assignment. Added
branch/task rows have no established worker IDs; their branch names are evidence,
not inferred assignments. Unknown remaining work is marked UNKNOWN.

| Lane | Owner | Status | Notes |
|------|-------|--------|-------|
| Repo bootstrap (LICENSE, README, CI, board) | coordinator | RECORDED | Initial repository setup |
| P1: kagent hello world on kind | W1 worker | PR #2 MERGED | — |
| README value-prop + Azure path | coordinator | PR #1 MERGED | — |
| P2: LLM-enhanced via ModelConfig | W2 worker | PR #3 MERGED | — |
| P3: connectors/tools via MCP | W3 worker | PR #4 MERGED | — |
| W-RENAME: in-repo rename, tomte → kaimahi | rename worker | PR #5 MERGED | — |
| P4a: metering/enforcing LLM proxy | W4 worker | PR #12 MERGED | — |
| P4b: enforcing MCP gateway | W5 worker | PR #15 MERGED | — |
| P4c: approvals/permits | W7 worker | PR #17 MERGED | — |
| P5a: governed Slack connector | W8 worker | PR #18 MERGED | — |
| P5b: cluster portability + real AKS run | W9 worker | PR #19 MERGED | — |
| P7a: NetworkPolicy egress | W10 worker | PR #23 MERGED | — |
| P7b: P6 inbound connectors | W11 worker | PR #24 MERGED | — |
| P7c: docs restructure | W12 worker | PRs #21/#22 MERGED | — |
| Post-move: Go module path + owner refs | W13 worker | PR #26 MERGED | — |
| CI hygiene: verifier reads function_response; docs-only e2e short-circuit | W14 worker | PR #29 MERGED | — |
| AKS NetworkPolicy enforcement | W15 worker | PR #30 MERGED | — |
| Post-P7a/P7b reconciliation | coordinator | PR #28 MERGED | — |
| W16: single-pod rollout readiness | W16 worker | PR #32 MERGED | — |
| P8a: Slack loop on AKS behind a one-port TLS edge | W17 worker | PR #35 MERGED | — |
| Docs cleanup: stubs, plans/specs, CLI wording, pycache | coordinator | PR #40 MERGED | — |
| P8b: approval routing via Slack + per-approver identity | W18 worker | PR #41 MERGED | — |
| P9: stateless multi-replica plane, exact budgets, metrics | W19 worker | PR #46 MERGED | — |
| P10: hosted upstreams through a hardened dialer | W20 worker | PR #51 MERGED | — |
| P11: `kmx` milestone 1 — developer journey | W21 worker | PR #57 MERGED | — |
| P11: `kmx` milestone 2 — clone-free governance tooling | W22 worker | PRs #64/#66/#67 MERGED | — |
| P12: argument-level policy and call-bound approval | W23 worker | PR #62 MERGED | — |
| P13: accounts-payable exception demo | W24 worker | PR #73 MERGED | — |
| CI: e2e job performance | W25 worker | PR #65 MERGED | — |
| P14: AP demo on AKS | W26 worker | PR #83 MERGED | — |
| `kmx` milestone 3 — core plane verbs | W27 worker | PR #81 MERGED | — |
| W32: release agent | W32 worker | PR #95 MERGED | — |
| W31: `create-kaimahi-agent` onboarding | W31 worker | PR #106 MERGED | — |
| W28: version, release, install and upgrade | W28 worker | PR #85 MERGED | — |
| W29: govern your own agent — generic onboarding | UNKNOWN | PARTIAL SHIPPED; remaining scope UNKNOWN | MCP-server onboarding shipped; no current assignment established |
| W38: e2e chat flake | W38 worker | PR #122 MERGED | — |
| W42: audit caller identity | W42 worker | PR #149 MERGED | — |
| W48: caller vocabulary | UNKNOWN | UNKNOWN | Remaining scope and assignment not established |
| W45: tool seam interoperability | W45 worker | PR #157 MERGED | — |
| W46: model protocol interoperability | W46 worker | PR #156 MERGED | — |
| W47: extensible observability | W47 worker | PR #155 MERGED | — |
| W43: TLS on the model and tool seams | W43 worker | PR #150 MERGED | — |
| W41: govern a foreign runtime | W41 worker | PR #139 MERGED | — |
| W40: stated protection gaps | W40 worker | PR #134 MERGED | — |
| W39: prompted credential capture | W39 worker | PR #123 MERGED | — |
| W30: call identity and expiring credentials | W30 worker | PR #86 MERGED | — |
| W34a: governed status count | W34a worker | PR #110 MERGED | Superseded unmerged PR #37 |
| W35: workflow blueprint and driver | W35 worker | PR #107 MERGED | Entry-point follow-up shipped in PR #112 |
| W36: workflow entry point | W36 worker | PR #112 MERGED | — |
| W37: descriptive naming instead of planning numbers | W37 worker | PRs #114/#115/#116 MERGED | — |
| W34b: version handshake, context guard, credential lag | W34b worker | PR #118 MERGED | — |
| W33: local-to-AKS lift with managed observability | W33 worker | PR #119 MERGED | — |
| Brand assets + architecture diagram + org/front-door plans | user-run lane | PR #33 MERGED | — |
| README front door + CONTRIBUTING.md | user-run lane | PR #34 MERGED | — |
| CLI decisions + PR #16 review | user + coordinator | REVIEW RECORDED; current disposition UNKNOWN | No current assignment established |
| CI flake: agent-readiness race | coordinator | PR #20 MERGED | — |
| NetworkPolicy egress promotion | — | PRs #23/#30 MERGED | Implemented through P7a and W15 |
| P6: inbound connectors | — | PRs #24/#35 MERGED | Implemented through P7b and P8a |
| CLI: `kaimahi agent create` | teammate | PR #16 CLOSED, unmerged | — |
| Status output + host preflight | teammate | PR #37 CLOSED, unmerged | Superseded by PR #110 |
| Development guide + Python 3.9 fix | teammate | PR #38 MERGED | — |
| Podman for the kind path | teammate | PR #42 MERGED | — |
| Recover restarted Podman kind clusters | teammate | PR #53 MERGED | — |
| Docs: CLI-first framing + naming record | teammate | PR #10 MERGED | — |
| Docs: agent-first scenarios | teammate | PR #11 MERGED | — |
| Post-merge reconciliation | coordinator | PR #13 MERGED | — |
| User docs: guide + FAQ | W6 worker | PR #14 MERGED | — |
| Orka platform survey | branch `w49-orka-lane` | PR #166 MERGED | — |
| KARS survey and upstream reports | branches `w49-kars`, `w49-kars-upstream` | PRs #163/#164 MERGED | — |
| Migrate an existing demo onto Orka | branch `worktree-migrate-onto-orka` | PR #168 MERGED | — |
| Migration on AKS | branch `worktree-w50-migrate-aks` | PR #170 MERGED | — |
| Orka front door | branch `feat/orka-front-door` | PR #171 MERGED | — |
| Orka status truth and inspect-first migration | branch `feat/orka-status-truth` | PR #172 MERGED | Local `worktree-w51-migrate-every-time` points to the merge |
| Runtime assessment | branch `w51-runtime-assessment` | EXISTS, unmerged; outcome UNKNOWN | Branch `fee07df`; contents not consulted |
| Orka authoring and migration research | branch `spike/kagent-orka-compat` | PR #174 MERGED | Research, 2026-09-10 |
| Substrate evaluation boundary | branch `docs/substrate-posture` | PR #175 MERGED | Research, 2026-09-10 |
| Current authoring-contract draft | branch `w56/authoring-boundary` | PR #176 CLOSED, unmerged | Research, 2026-09-10 |
| Native Orka recommendation | branch `docs/kagent-on-orka-boundary` | PR #177 MERGED | Recommendation, not a ruling |
| Superseded shell and Make shims | branch `cleanup/remove-superseded-shims` | PR #173 MERGED | Included in base `572f3a6` |
| Bubbles agent creation wizard | branch `feat/charm-cli-ux-followup` | PR #167 MERGED | — |
| Orka agent creation | branch `feat/orka-agent-create` | LOCAL BRANCH EXISTS; disposition UNKNOWN | Branch `2267081`; no numbered lane or current assignment inferred |
| Documentation retirement | coordinator; branch `docs/orka-documentation-retirement` | ACTIVE | Documentation and authorized documentation-checker changes only |
| Plane/code retirement: inbound/notify | worker session; branch `cleanup/retire-governance-plane` | PR #183 MERGED | Inventory published before deletion; inbound/notify removed, live migration proved; no authoring changes. |
| Plane/code retirement: gateway/tool approvals | worker session; branch `cleanup/retire-tool-governance` | PR #184 OPEN | Based on main after #183. Gateway/tool execution removed; shared model helpers retained. Both authoring paths unchanged; live migration, historical-grant inactivity and retained budget grants proved. Budget approvals/seam-adjacent cleanup follows last. |

## Upstream tracker

- orka-agents/orka [PR #539](https://github.com/orka-agents/orka/pull/539): **MERGED** — stop serving the dashboard for unrouted compatibility API paths.
- orka-agents/orka [#540](https://github.com/orka-agents/orka/issues/540): **OPEN** — Responses API.
- orka-agents/orka [#541](https://github.com/orka-agents/orka/issues/541): **OPEN** — cost in money, not just tokens.
- orka-agents/orka [#552](https://github.com/orka-agents/orka/issues/552): **OPEN** — external MCP servers.

## Coordinator handoff

Keep this board under 500 lines. Record current PR/branch state and unresolved
questions, not archived narratives. Use the safety/process references above
before launching work, and verify new assignments with their owner.

PR #173 is merged and included in this lane's base (`572f3a6`).
This lane changes documentation and its checkers only; runtime code and
`docs/orka.md` remain unchanged relative to that base. It ends with an open
PR targeting `main` and green checks, not a merge.

When updating this register, run:
- `python3 scripts/check-board.py`
- `python3 scripts/check-board.py --selftest`

The tests use synthetic boards; retiring prose must not require deleting
consistency checks or keeping archived prompts.
