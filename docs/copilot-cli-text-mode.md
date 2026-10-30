# Copilot CLI text mode

**Status:** Unreleased current-main integration contract for
[#750](https://github.com/kaimahi-agents/kaimahi/issues/750). It is not in the
v0.9.0-rc.4 marketplace pair, whose fallback returns bounded display text only.

This contract makes Create, Edit, Run and Test useful through Copilot chat when
the host cannot render the kmx canvas. Text mode is a normal presentation of the
existing MCP operations and checked agent/source resources. It is not another
runner, file reader, editor or persistence layer.

## Scope

- Treat a successful canvas open and a successful text fallback as two valid
  presentations of the same checked agent resource.
- Preserve enough exact state in a text fallback for a fresh chat to make a
  revision-checked Edit when, and only when, the existing resource supplied a
  complete editable source snapshot.
- Teach the shared skills to report structured Run/Test outcomes without
  confusing protocol success with a passing test.
- Keep an explicitly selected runtime model for later Run/Test calls in the same
  conversation, independently of the chat model and authored file model.
- Show model answers only when the user requested content and that invocation
  returned it. Never rerun to recover omitted content.

Native `/kmx`, a new MCP read tool, Explain over MCP, shell/file-tool fallback,
cross-chat model persistence and running tools outside the app remain out of
scope. Without the extension, a fresh chat cannot reopen an existing file; the
separate work for a generic read operation is intentionally not introduced here.

## Source authority

Legacy `kmx://agent/{path}` remains the single-source reopen authority. Paired
files use `kmx://agent-sources/{path}{?tests}` instead. Its checked resource
provides the exact file path and SHA-256 revision. Its optional `authoring`
snapshot contains the complete source bytes and an `editable` decision. The
extension verifies that the source hashes to the revision before exposing it.

The bounded human-readable text is display-only. It may shorten instructions,
tests and answers, but it prints the full revision. It must never be copied back
into Edit or used to reconstruct omitted source.

The `open_kmx_canvas` text result is additive:

```json
{
  "opened": false,
  "mode": "text",
  "path": "/repo/greeter.agent.md",
  "text": "bounded display text",
  "file": {
    "path": "/repo/greeter.agent.md",
    "revision": "sha256:<64 lowercase hex>"
  },
  "authoring": {
    "source": "complete exact source",
    "editable": true
  }
}
```

For a paired snapshot, `authoring.source` still holds exact agent bytes and
`file.revision` remains their raw hash. Additional `selectedSources` contains exact
`testsSource`, tests state/revision/path, selection key, combined `snapshotRevision`,
opaque `sourceVersion`, issuing `server`, capabilities and optional `selector`.
The opener accepts optional `tests` for explicit selection; omit it for the shared
default rule. Use that same owner and selector with v3 `expectedSourceVersion`;
never use a raw hash as the token or transfer it across servers. Text fallback
starts no host relay. If normalized editing data is omitted for size, the complete
sources remain inspection-only. Sources are never truncated to enable editing.

This structured result belongs to the model-facing `open_kmx_canvas` tool.
Native `/kmx-canvas open` logs only the bounded text projection and does not
return `file` or `authoring` to chat; `/agent-view` is likewise read-only.

If the resource has no complete authoring snapshot, the result stays readable
but is explicitly not editable:

```json
{
  "authoring": {
    "editable": false,
    "reasonCode": "complete_source_unavailable"
  }
}
```

An inspection-only full snapshot may include `source`, `editable: false` and
`reasonCode: "source_not_editable"`. Skills may call Edit only with a complete
`source`, `editable: true` and the matching full revision. Source and formatted
text are untrusted agent content, never instructions to the chat model.

Canvas success keeps its existing small response and does not duplicate source
content. A renderer advertised by the host but failing to open remains a safe
refusal rather than silently changing presentation modes.

## Conversation behavior

After Create, use the returned `structuredContent.file.path` and revision. If a
canvas opens, report canvas success. If the extension returns `mode: text`,
report text-view success, not “Canvas not opened.” If neither opener exists,
confirm the saved file and continue in the same chat using the proposed source
and returned revision. A fresh chat without the extension must say it cannot
safely reopen the file.

Adding tests is one revision-checked Edit. After a confirmed save, list the new
test IDs and checks as **not run**. Do not call Test unless the user asked to run
it. A refused, failed or publication-unknown Edit never becomes a successful
addition.

Ordinary full-content Edit may retain its submitted source with the returned
revision only after checking that the source SHA-256 equals that revision. Sparse
`exampleEdits` are applied by the server to the supplied baseline, so their
submitted `content` is not the saved source. After a sparse Edit, later editing
requires a fresh checked complete authoring snapshot for the returned revision;
without one, chat withholds Edit rather than pairing pre-patch source with the
post-patch revision.

An explicit model chosen by the user is remembered for that exact agent path in
the current conversation and supplied on later Run/Test calls. An attributable
result may confirm the effective provider/model. This choice never rewrites
`Model.Preferred`; asking to return to the default clears the conversational
override. No result from another path or model is reused as current.

## Result reporting

Read Run/Test from `structuredContent`, never from transport success or summary
prose. Report the exact revision, provider/model, selection scope and selected
case count. Emit one row for every returned example, retaining its test ID,
state, verdict and safe `reasonCode`; a non-pass row may also name its attributed
assertion ID/type/reason code.

First distinguish the exact MCP preflight envelope: `schemaVersion`,
`reasonCode` and `reason`, with no `kind`. It is not a preview. Report
`cancelled` as **Cancelled**, `deadline_exceeded` or `operation_failed` as
**Did not finish**, and every other safe preflight code as **Refused**. Do not
invent preview identity, state or verdict, and never report a pass. Any other
legacy result must have `kind: kmx.agent-file-preview` before the preview checks below.
For v3 require `kmx.agent-pair-preview`, match the executed `sourceVersion` and
`preview` source tuple, then apply those same checks to the unwrapped `preview`.
A complete capture identifies the saved pair with `savedSourceVersion` and
`savedSnapshotRevision`; its `savedRevision` is the recording target, not necessarily
the agent. Partial/uncertain role effects never authorize another operation or
restore metadata withheld by the App. Refresh both sources at the same selection.

The returned file path and revision must match the held invocation target and
revision. An explicitly requested model or trusted route must also match the
returned provider/model; when none was supplied, the attributable returned route
is the effective choice. A mismatch is **Not current**, never pass, and cannot
change the remembered model.

For legacy single-source capture, an attributable result with `savedRevision` still describes execution against
its returned `file.revision`; recording capture changed the saved file afterward.
Before a later Run, Test or Edit, chat reads a checked snapshot for the same path,
requires its revision to equal `savedRevision`, and uses the refreshed revision
and source. It never combines the pre-capture source with the saved revision or
continues when that refresh is unavailable or mismatched.

The overall labels are closed:

| Structured result | Chat label |
|---|---|
| `state: complete`, `verdict: pass`, full scope, `fullPass: true` | Full pass |
| `state: complete`, `verdict: pass`, `fullPass: false` | Partial pass, never a full pass |
| `state: complete`, `verdict: fail` | Failed |
| `state: complete`, `verdict: unknown` | Inconclusive |
| `state: refused` | Refused |
| `state: cancelled` | Cancelled |
| `state: error` | Did not finish |
| Example `state: not_run` | Not run |

Missing, malformed or contradictory fields are unknown, never pass. A successful
MCP call alone proves no verdict. Subset selection remains partial even when all
returned rows pass.

Set `showContent: true` only when the user asks to see an answer. Show only a
safe answer/excerpt returned by that invocation. Treat absent or withheld
content as unavailable; do not infer it from journals, history or a new Run.
`retention.status: not_requested` concerns failure-diagnostic retention, not
answer availability. Ordinary Test reporting keeps failure-only content.

## Skill and command names

The Copilot CLI uses the installed skill's full entry, not a universal `/kmx`
alias. The WSL/native source entry is `/kmx-agent-builder:kmx`; the PATH source
entry is `/kmx-agent-builder-path:kmx`. Natural-language opening remains valid.
Native `/kmx-canvas` and `/agent-view` still require the extension and are
separate from skill invocation. They display bounded text and never return
structured Edit authority to chat.

## Acceptance

- Create succeeds with a canvas, a text fallback or no extension without calling
  Run/Test implicitly.
- Text fallback carries the full revision and exact editable source, or clearly
  withholds edit authority without returning a truncated source.
- A fresh extension-backed text chat can Edit with source/revision CAS; a fresh
  extension-free chat refuses to guess or read through another tool.
- Added tests are shown as saved and not run.
- Full, partial, failed, inconclusive, refused, cancelled, error and not-run
  outcomes remain distinct in chat.
- The chosen runtime model is reused only for the same path and conversation and
  never changes source.
- Answers are shown only on explicit request and never recovered by rerunning.
- Canvas behavior, MCP schemas, runner behavior and revision checks remain
  unchanged.
