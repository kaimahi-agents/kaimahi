# Bundle format compatibility

A bundle's portable definition is `agent.yaml`; `bindings.yaml` describes its
creation target, `eval/*.yaml` holds optional tests, `lift-policy.yaml` can
require evaluation before lift, and `receipts/` holds local deployment and
evaluation evidence. kmx is pre-1.0: these are the rules of the
current reader, not a guarantee that every pre-1.0 release can read every
other release's output.

## What a reader accepts

| File | Current rule |
|---|---|
| `agent.yaml` | Exactly one `kmx.kaimahi.dev/v1alpha1` `PortableAgent`. `extensions` is absent, `{}`, or holds exactly one of `orka` (`core.orka.ai/v1alpha1`) or `kagent` (`kagent.dev/v1alpha2`). `extensions: null`, null arms, unknown fields and other versions are refused. |
| `bindings.yaml` | Creation-target `kmx.kaimahi.dev/v1alpha1` `OrkaBindings` or `KagentBindings`; Orka lift supplies new target bindings rather than copying these. |
| `eval/*.yaml` | One case per file with `id`, nonblank `input`, and at least one `expectContains` entry or typed `assertions` entry (or both); **no** `apiVersion` field. See [assertion schema](agent-lift.md#evaluating-a-deployed-revision). |
| `lift-policy.yaml` | Optional strict YAML mapping of destination and evaluated cluster UID/namespace rules; **no** `apiVersion` field. See [the evaluation gate](agent-lift.md#requiring-evaluation-before-lift). |
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
Kagent rendering still requires an explicit `extensions.kagent.runtime` choice.
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
Status compares the live Provider and Agent against today's target-specific
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
operand: `contains`/`notContains` use `value`, `regex` uses `pattern`, and
`toolCalled`/`toolNotCalled` use `tool`. The `expectContains.*` ID namespace is
reserved for legacy entries expanded to `contains` rows. Regex patterns are
Go RE2, nonblank and at most 1,024 UTF-8 bytes; `llmJudge` and `llm_judge` are
refused. An older reader without typed assertion support refuses `assertions`
as an unknown field, even though case files still have no version tag.

Their exact bytes and filenames have a **separate** case-set digest, so
changing a case does not change the portable revision but invalidates the
prior evaluation result for that set. V2 receipts additionally record each
case file's exact-byte `caseDigest`, decoded-input `inputDigest`, and assertion
`definitionDigest` (type plus authored operand, independent of ID). Assertion
rows retain only ID, type, definition digest, verdict and fixed reason, not
operands. Both current production evaluation paths always mark tool assertions
unknown, including negative assertions with empty or present journals; parser
support is not runtime tool capability.

New evaluation receipts are v2; deployment receipts and remembered selections
remain unversioned. Agentsessions versions its limited host identity separately
from the receipt schema. V2 writers archive each run once in private
`receipts/runs/eval-<runID>.json` and then update the unchanged latest
`receipts/eval-<key>.json` filename. Archives never participate in lift-gate or
status selection. No prompts, answers, assertion operands, tool payloads or
reasoning are persisted in new evaluation receipts; older Orka receipts can
contain legacy matched/missing expectation text. Keep all local evidence
private and outside Git.

[Sessions verification](agent-lift.md#verifying-a-sessions-receipt) accepts valid
legacy and v2 receipts without synthesizing evidence; unknown verdicts and
unsupported tool-bearing replay remain refused. Verification and
[offline diff](agent-lift.md#comparing-saved-evaluations-offline) strictly reject
unknown/duplicate JSON members, including case-folded duplicates. Diff
recognizes legacy receipts but reports them incompatible because they lack
assertion-level evidence. V2 full runs may compare across different portable
revisions, but need compatible runtime identities and matching inputs and
assertion IDs/definitions for verdict comparisons. Readability is not a general
migration or cross-version compatibility promise for local evidence.

Status skips malformed deployment receipts,
counts a malformed, incomplete or mismatched evaluation receipt as `none`,
and a malformed remembered selection is refused as ambiguous. Status checks target identity
and recorded digests before counting evidence; another kmx version's readable
receipt does not by itself establish that today's bundle or target was tested.

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
