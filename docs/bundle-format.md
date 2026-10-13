# Bundle format compatibility

A bundle's portable definition is `agent.yaml`; `bindings.yaml` describes its
creation target, `eval/*.yaml` holds optional tests, `lift-policy.yaml` can
require evaluation before lift, and `receipts/` holds local deployment and
evaluation evidence. kmx is pre-1.0: these are the rules of the
current reader, not a guarantee that every pre-1.0 release can read every
other release's output.

## Which format goes where

These inputs are not interchangeable:

| Format | Files / example | Use it with |
|---|---|---|
| Portable YAML bundle | `agents/my-agent/agent.yaml`, `bindings.yaml` and `eval/*.yaml`, created by `kmx create --live` | `kmx bundle lift`, `bundle status` and `bundle evaluate`. The [CI eval action](agent-lift.md#run-evals-in-ci) takes this bundle directory, not an image. Runtime-specific limits still apply. |
| Extracted AgentSuite definition | `agentsuite.json` and referenced JSON manifests, such as the tool-free incident-analyst fixture | `kmx suite validate`, `push` and `pull`. Use incident-analyst for the [experimental build example](kmx.md#experimental-agentsuite-image-build). |
| AgentSuite OCI artifact / layout | The packaged definition distributed by `kmx suite push` and recovered by `kmx suite pull` | Distribution and offline format validation. This is not a runnable sandbox image. See the [AgentSuite specification](../agentsuite/spec.md). |
| Sandbox image archive | `incident-analyst.oci.tar`, produced by the experimental `kmx suite build` example | Docker import and container serving under that example's image-store and inference prerequisites. It is not the YAML input to `bundle evaluate` or the CI action, and current builder output is not AgentSuite-conformant. |

The specification's [coordinator-workers fixture](../agentsuite/testdata/coordinator-workers/)
illustrates invocation edges. It is a format-validation example, not a build
example for today's tool-free AgentKit adapter. A valid Suite can use features
that this narrow builder does not support; validation is not a promise that it
can build or run that Suite. Neither example converts an `agent.yaml` bundle
into an AgentSuite.

## What a reader accepts

| File | Current rule |
|---|---|
| `agent.yaml` | Exactly one `kmx.kaimahi.dev/v1alpha1` `PortableAgent`. `extensions` is absent, `{}`, or holds exactly one of `orka` (`core.orka.ai/v1alpha1`) or `kagent` (`kagent.dev/v1alpha2`). `extensions: null`, null arms, unknown fields and other versions are refused. |
| `bindings.yaml` | `kmx.kaimahi.dev/v1alpha1` `OrkaBindings` or `KagentBindings`; Orka lift supplies new target bindings rather than copying these. Explicit v0.10.3 bundle lift/status uses KagentBindings here or from `--bindings`; status requires the same desired binding input. |
| `eval/*.yaml` | One case per file with `id`, nonblank `input`, and at least one `expectContains` entry or typed `assertions` entry (or both); **no** `apiVersion` field. See [assertion schema](agent-lift.md#evaluating-a-deployed-revision). |
| `lift-policy.yaml` | Optional strict YAML mapping of destination and evaluated cluster UID/namespace rules; **no** `apiVersion` field. See [the evaluation gate](agent-lift.md#requiring-evaluation-before-lift). |
| Exact-v0.10.3 lift receipts | Private readiness evidence in `receipts/kagent-v0.10.3/`, binding runtime/version, cluster/namespace, primary/deployment UIDs and portable/rendered digests. Separate from create, Orka, eval and execution evidence; no kagent remembered selection. |
| Orka lift receipts and remembered target | JSON with **no** format-version field or version negotiation. Remembered selections live in local kmx state, outside the bundle and Git. |
| Orka evaluation receipts | New writers use `schemaVersion: 2`, `runID`, per-case source/input digests and payload-free assertion rows, plus existing revision/target/Task evidence and the target model identity. Legacy unversioned receipts remain readable subject to evidence checks. |
| Agentsessions evaluation receipts | New writers use `schemaVersion: 2` and the same run/assertion fields. The separate version-1 `target.identity` remains `host-reported` address, returned harness and journal-observed model; each case retains session UID, journal head and output digest. No verified descriptor or lift-gate support. See [sessions evaluation](agent-lift.md#evaluating-on-agentsessions). |

`agent.yaml`, evaluation cases and a present `lift-policy.yaml` use strict
YAML decoding: an unknown field (including a future optional field) is an
error, not silently discarded. A
future document or extension `apiVersion` is also refused with the required and
found versions. `bindings.yaml` is strictly decoded too. An older kmx thus
refuses a newer bundle that uses fields it does not know, even if its
`apiVersion` has not changed. This is safer than deploying a bundle after
ignoring part of its behavior, but **the same version is not a promise of
backward parsing compatibility across every release**: v0.2.0 had a portable
document shape with target bindings inside the Orka extension; current kmx
rejects those fields even though the version string is unchanged. Persistent
`agent.yaml`/`bindings.yaml` bundles first shipped in v0.3.0.

A core-only document with absent or empty `extensions` can be rendered to Orka;
explicit v0.10.3 bundle rendering selects Go for core-only definitions. The
v0.10.2 create renderer still requires `extensions.kagent.runtime`. An explicit
extension binds native behavior; it is not permission to ignore unsupported
fields or to auto-select a runtime.
`spec.coordination.allowedAgents` is optional portable-core behavior: when present
it must name at least one distinct, non-self helper in the destination scope.
It never permits all Agents. Orka renders it as enabled coordination with exactly
those names and no stated concurrency or depth limits. Kagent refuses it because
it cannot render delegation. It cannot coexist with
`extensions.orka.agent.coordination`, even if the latter says `enabled: false`.
Legacy Orka-extension-only documents retain their source digests and rendered
bytes; migrating a coordinator to the core field changes the authored revision.
Older kmx readers reject `spec.coordination` as an unknown field; they must be
upgraded before reading a migrated bundle. No AX adapter is shipped yet.
A target refuses each behavior field in an extension it does not consume before
rendering; `apiVersion` alone does not specify behavior. Older kmx readers
refuse core-only documents instead of ignoring them. The Orka-only reader at
`47e7d88` reports `portable agent document: extensions.orka is required:
"orka" is the only runtime this document targets`; the reader at `3926b18`
reports `portable agent document: extensions must contain exactly one of
"orka" or "kagent"` (a command may prefix that parser error).

## Private recorded-session manifests

`eval from-sessions` writes an external mode-0600 JSON manifest, not a
portable bundle file. The strict contract uses `schemaVersion: 1`,
`policy: single-turn-text-chat-v1`, and `cases`: each has a stable ID, authored
`expectContains`/`assertions`, `assertionsDigest`, and `source` with `store`,
`sessionUID`, `harness`, `harnessVersion`, `model`, `inputSeq`, `prefixSeq`,
`prefixHash`, `inputDigest`, `answerDigest` and `configDigest`. Store is an
explicitly selected normalized endpoint identity, not permission to dial it.
Only the five-record fresh text-chat shape with input sequence 2 is supported.
Unknown/duplicate/null fields, incompatible identities, changed assertions and
duplicate IDs or store/UID pairs are refused. Manifests are limited to 1 MiB and
1,000 cases; the smaller execution receipt budget still applies.

Manifests hold **no recorded input/answer text**, but references, hashes and
rubrics remain sensitive. Existing outputs are atomically appended within one source store, with
cooperative writer locking; mixed-store additions are refused and all selected
UIDs must extract successfully before
any additions are saved. Keep manifests and receipts outside Git. Resolution
uses the explicit `--source-sessions` selector and bounded prefix verification,
never exports content when a source expires. Reference-case bytes bind policy,
source prefix and authored assertion identities into existing v2 case/set
digests; v2 receipts themselves retain their unchanged payload-free schema.
See [recorded cases and consent](agent-lift.md#recorded-sessions-as-reference-cases).
An unavailable source produces its own `unknown` case row in diff, while cases
with compatible observed candidate identities still compare. Any unknown row
keeps the comparison incomplete/nonzero. If either receipt has no complete
observed case identity, or the observed identities disagree, diff is incompatible.

## What the digests say

The **portable digest** identifies the exact authored bytes of `agent.yaml`,
including comments, whitespace and trailing newline. kmx hashes a framed
entry—`portable-agent.yaml`, a space, the decimal byte length, a newline, the
file bytes and a final newline—with SHA-256. It is not the bare SHA-256 of the
file or a hash of decoded YAML. Target bindings, evaluation cases, lift policy
and receipts are outside this digest. An upgrade that leaves `agent.yaml` untouched cannot
change its portable digest under this format's digest rule.

The **rendered digest** instead identifies the exact ordered rendered documents
(including the review-only Secret skeleton), each framed with its position and
byte length. It depends on target bindings and the kmx renderer. An upgraded
kmx can produce different output—and a different rendered digest—from the
same `agent.yaml` against the same target; the portable digest still matches.
Orka `bundle status` compares the live Provider and Agent against today's target-specific
rendering using a server dry-run. With matching ownership markers it reports
`drifted` and changed field **paths** when fields differ, or `in sync` when
both fields and markers agree. A stale rendered-digest ownership marker on an
otherwise identical resource instead reports `unknown` with `live ownership
digests do not match the rendered revision`. This can take precedence over
field drift on a later resource (Provider is checked before Agent). If status
cannot establish the comparison, it also reports `unknown`, not `in sync`.
Lift can refresh stale ownership markers on unchanged resources, but it still
performs its usual checks before writing. See [lift and status](agent-lift.md).

## Local evidence and moving forward

Evaluation case files have no version tag; an unknown case field is refused.
Typed entries require explicit unique assertion IDs, `type`, and exactly its
operands: `contains`/`notContains` use `value`, `regex` uses `pattern`, and
`toolCalled`/`toolNotCalled` use `tool`. `llmJudge` requires nonblank `rubric`
(up to 4,096 UTF-8 bytes), integer `minScore` (0–100), and explicit printable
ASCII `judgeModel`/`judgeVersion` identities (up to 256 bytes each).
`llm_judge` is refused. There is no default judge model or endpoint, nor a
fallback to the agent model. Transport is separately selected with
`--judge-base-url` and optional `--judge-key-env` (environment name, not a key).
See [judge configuration and privacy](agent-lift.md#explicit-llm-judge).
The `expectContains.*` ID namespace is reserved for legacy entries expanded to
`contains` rows. Regex patterns are Go RE2, nonblank and at most 1,024 UTF-8
bytes. Older readers without judge support refuse `llmJudge`; readers without
typed assertion support refuse `assertions` as an unknown field, even though
case files still have no version tag.

Evaluation preflight reserves 8 KiB per receipt, 4 KiB per case and 384 bytes
per ordinary assertion within the 1 MiB receipt limit, plus actual bounded
serialized judge identity/digest/threshold/score metadata, including JSON
escaping. **232 one-ordinary-assertion cases** is only the upper bound before
known metadata is added; judge rows, additional assertions and metadata lower
it. Preflight also adds the actual JSON-serialized sizes of model, bundle and
target metadata before execution. The exact 1 MiB serialized-size writer
backstop remains for unbounded metadata learned during execution. `--case <id>`
selects one case, not an arbitrary subset, and its receipt cannot satisfy a full-case lift gate. Reader limits (including the
1,000-case verification limit) are separate from this execution budget.

Their exact bytes and filenames have a **separate** case-set digest, so
changing a case does not change the portable revision but invalidates the
prior evaluation result for that set. V2 receipts additionally record each
case file's exact-byte `caseDigest`, decoded-input `inputDigest`, and assertion
`definitionDigest` (type plus authored operands, independent of ID). For a
judge, that definition also binds rubric, `minScore`, model/version and the
versioned prompting/parser/threshold policy. Assertion rows retain ID, type,
definition digest, verdict and fixed reason, not operands. Judge rows additionally
require `judge` evidence with `model`, `modelVersion`, `provenance: user-declared`,
`evaluatorDigest`, `configurationDigest` and `minScore`; `scores` records both
valid integer samples or is absent when either response is unavailable/malformed.
The evaluator digest identifies policy; the configuration digest binds canonical
endpoint/model/version/policy without credentials. Model/version are authored
claims, not provider attestation. Non-judge rows refuse judge metadata.
Both scores at or above the threshold pass, both below fail, split scores are
unknown. Offline readers validate this coherence; lift gates also require
current authored identity/threshold/definition and two passing scores.
Older strict v2 readers reject the added judge field, despite the unchanged
schema version. Existing non-judge typed receipts remain readable. Both current
production evaluation paths always mark tool assertions unknown, including negative assertions with empty or present journals; parser
support is not runtime tool capability.

New evaluation receipts are v2; deployment receipts and remembered selections
remain unversioned. Agentsessions versions its limited host identity separately
from the receipt schema. V2 writers archive each run once in private
`receipts/runs/eval-<runID>.json` and then update the unchanged latest
`receipts/eval-<key>.json` filename. Archives never participate in lift-gate or
status selection. No prompts, answers, assertion operands (including rubric),
tool payloads or reasoning are persisted in new evaluation receipts. Judge input/answer/rubric
are sent to the explicitly chosen provider, whose retention policy applies;
terminal reasoning may repeat answer text. Receipt privacy does not cover
terminal logs or private sessions journals. Older Orka receipts can
contain legacy matched/missing expectation text. Keep all local evidence
private and outside Git.

[Sessions verification](agent-lift.md#verifying-a-sessions-receipt) accepts valid
legacy and v2 receipts without synthesizing evidence. Valid agent-answer
evidence may replay even with unknown judge outcomes, but unknown ordinary
assertions, unavailable execution/answers and unsupported tool-bearing replay
remain refused. Replay makes zero model/judge calls and does not verify any
judge outcome; `judgeAssertionsNotVerified` counts judge rows in the report
when nonzero, with an explicit terminal notice. Verification and
[offline diff](agent-lift.md#comparing-saved-evaluations-offline) strictly reject
unknown/duplicate JSON members, including case-folded duplicates. Diff
recognizes legacy receipts but reports them incompatible because they lack
assertion-level evidence. V2 full runs may compare across different portable
revisions, but need compatible runtime identities and matching inputs and
assertion IDs/definitions for verdict comparisons. Judge model/version,
provenance, policy/configuration digests and threshold must also match; changed
judge identity makes diff incomplete/nonzero, not a fix/regression, and valid
comparable rows report both scores. Readability is not a general
migration or cross-version compatibility promise for local evidence.

Orka `bundle status` skips malformed deployment receipts and reports `none` for evaluation
receipts with invalid JSON shape or mismatched target identity or digests.
V2 receipts require valid per-case assertion and judge evidence even for
recorded `fail` and `unknown` outcomes. A claimed evaluation pass additionally
requires complete passing evidence matching today's authored assertions.
A malformed remembered selection is refused as ambiguous. Another kmx version's
readable receipt does not by itself establish that today's bundle or target was tested.

The explicit v0.10.3 path refuses malformed selected deployment evidence as
unreadable. It re-renders desired bindings, checks saved UIDs/digests/dependencies
and reports drift rather than repairing it. No-flag Orka readers exclude foreign
runtime receipts but continue to accept historical runtime-less Orka receipts.
A v0.10.2 create receipt cannot authorize v0.10.3 reuse; matching names or labels
are insufficient. See [exact-version placement](agent-lift.md#exact-v0103-declarative-bundle-placement).

Today kmx recognizes the Orka and Kagent extension versions above, but no
negotiation for future versions. The optional
`extensions.orka.agent.coordination` block uses the Orka version: a kmx
release without that field refuses a bundle that states it. Any added optional
field under this version still requires a kmx release that knows that field. Changing an existing
field's meaning would require a new extension version **and** reader support
for whichever older version remains accepted; changing the string alone does
not provide compatibility. Before using a newly authored field, upgrade kmx
on machines that will read or deploy the bundle and check that release's format
and upgrading notes. Keep the original `agent.yaml` bytes if its digest is
needed for provenance; make a deliberate new revision when adopting a changed
format, then lift and re-evaluate as needed. If a reader refuses a bundle,
inspect the named version or field and use a release that supports it rather
than deleting a field to make an older reader proceed.
