# kmx in macOS Copilot

Use this plugin on **macOS** (also usable with a Linux kmx on PATH). On
**Windows**, use the [WSL plugin](../copilot-wsl/README.md) instead. Both contain
skills, a canvas and MCP configuration, not an engine or a fixed working folder.

## Install on macOS

With Copilot installed, run these two commands in a Mac terminal:

```sh
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/v0.9.0-rc.2/install.sh | sudo sh -s -- --version=v0.9.0-rc.2 --bin-dir=/usr/local/bin
copilot plugin install kaimahi-agents/kaimahi:plugins/copilot
```

The first command selects `kmx-darwin-arm64` on Apple Silicon and
`kmx-darwin-amd64` on Intel, downloads it over HTTPS, checks its published SHA256
and installs it at `/usr/local/bin/kmx`. It uses the same binary installer as WSL,
not a plugin setup script. It does not verify the separate signature bundle.
The Homebrew tap tracks final releases, not release candidates; use this binary
for rc.2. Homebrew is the install path for final v0.9.0.

The plugin starts `kmx mcp` from PATH; `/usr/local/bin` must be on Copilot's PATH.
No root, model or binary is configured inside the plugin. The MCP schema has no
per-OS command selector, so Windows keeps its separate WSL manifest. Both
plugins have the name `kmx-agent-builder`; installing one replaces the other.
Direct repository installs currently work but Copilot warns that they are
deprecated; a marketplace install will come later.

Ask Copilot to create an agent through kmx and open its canvas. Give it a full
Mac path when the file is outside the current directory. Add or edit tests;
Enter saves and runs them. Change the prompt and press Enter again to see the
results change. The runtime model is picker override → authored model → current
chat model (when available) → `gpt-5.6-terra`; existing `gh` sign-in supplies credentials without an install-time
login step. An unknown model fails without silently switching models.

## Shared assets and verification

The skills and canvas have one source in `plugins/copilot-wsl`. This directory
links to those sources; Copilot copies them into a self-contained installed
cache. No second canvas implementation or bundled kmx is maintained here.
The existing Python developer tools and legacy templates below are inert files
in the source plugin, not additional extensions or installation steps. Node
tests live separately under `plugins/tests/copilot`.

The `macos-install` CI job installs the published rc.2 darwin binary, checks its
SHA256 and exercises the public plugin install command. On PRs, it additionally
installs the PR's local plugin to verify the new bytes before merge; on main it
checks the public source's installed contents. It verifies cached assets and
Create → Test pass → prompt-only Edit → Test fail → restore → Test pass with
`fake`. **CI does not exercise a Copilot model or the Copilot app.** An
authenticated app walk on a Mac is still needed before claiming that path works.

## Legacy native bundles

The remaining sections describe the separate root-bound native archive tooling,
not the two-command repository install above. Native bundles are generated and
signed by the release workflow. The legacy builder uses
[native-mcp.json](native-mcp.json); the repository plugin uses [mcp.json](mcp.json).
Canvas actions use existing MCP operations, not a second runner.

## Package contract

The package uses [Agent Plugins 1.0](https://agent-plugins.org/plugin-authors/build-an-agent-plugin.md):
root [plugin.json](plugin.json), root [mcp.json](mcp.json) and
[skills/kmx-agent-builder/SKILL.md](../copilot-wsl/skills/kmx-agent-builder/SKILL.md) and
[skills/kmx/SKILL.md](../copilot-wsl/skills/kmx/SKILL.md).
The [MCP specification for plugins](https://agent-plugins.org/plugin-authors/mcp-servers.md)
resolves `./bin/kmx` against the plugin root; Windows templates use
`./bin/kmx.exe`. It is one executable token, not a shell command or PATH lookup.
Default cwd is the plugin root, **not the user's repository**.

The offline [package.py](package.py) helper is **developer tooling** requiring
Python 3.9+. Generated packages contain a native executable, data and the
`com.github.copilot/extensions/agent-view` JavaScript/assets. The native component
uses Copilot's bundled SDK/runtime, not a separately installed Node, Python, Go
or shell bootstrap script. Extension APIs are experimental; loading the plugin
also loads trusted local code.
Supply a binary, expected SHA256, declared platform, plugin version and an
explicit selected operation root:

```sh
python3 plugins/copilot/package.py \
  --binary /approved/engine/kmx --sha256 <64-lowercase-hex> \
  --platform linux/amd64 --version 0.1.0 \
  --root /explicit/selected/repository --output /new/development-plugin
```

`--sha256` pins the exact copied bytes; **a caller-supplied digest is not
publisher authentication**. Packaging never executes the supplied binary.
The platform is a caller declaration, not ABI, signing or host-compatibility
attestation. Output is exclusive, outside the selected repository, and includes
readable engine digest metadata. Existing output, source links/special files,
missing root, filesystem-root selection, invalid metadata and digest mismatch
refuse. The helper does not fetch, archive, install or write host settings.

## Agent paths and real tools

`kmx mcp` accepts agent files in any directory without a bound folder. Use full
WSL filesystem paths when calling it from Windows; relative paths resolve against
the MCP server's working directory. Agent files are `*.agent.md` and
`.squad/agents/<member>/charter.md`. Ordinary folder names and symlinks to agent
files work; a link to a non-agent file does not.

The existing native bundle builder below still produces the legacy
`kmx mcp --root ABSOLUTE_DIR` configuration and matching
[selected-root.json](selected-root.json). That compatibility mode remains bound
to its configured directory; the new unbound server needs neither file nor a
folder confirmation. Plugin installation is replaced separately.

The bundled engine exposes exactly lowercase `create`, `edit`, `run` and
`test`. Canonical branches require `schemaVersion: 1` and an agent `path`; Create
also needs canonical `content`, Edit needs `content` and `expectedRevision`.
Run/Test accept `caseId`, `showContent` and `showFailedContent`; Run also accepts `input`.
Use discovered schemas as authority. See the
[real MCP contract](../../docs/kmx.md#local-stdio-mcp).
No fifth bind/Explain/model/install tool is introduced.

The default package starts without provider overrides and supports the
deterministic fake orientation in the skill. Arbitrary tasks need an explicitly
configured supported route and exact Model.Preferred; no silent fallback.
Fake/local previews retain instructions, inputs and answers in cleartext SQLite
journals under `<kmx state>/local-preview/journals` (`KMX_HOME` when set).
Each invocation owns one journal. New starts prune to the newest 20 runs,
keeping active runs; concurrent starts can leave more. Stop previews before
removing journal files there to clear retained content. See
[journal retention](../../docs/local-preview.md#journal-retention-and-clearing).
Real-model route setup is not automated by this packager.
For manually configured trusted startup, `kmx mcp --root ABSOLUTE_DIR --provider copilot`
uses existing runtime credentials and an exact listed
`Model.Preferred`, with a fixed endpoint. This sends instructions/inputs and
any judge answers/rubrics to Copilot. Startup does not sign in or access credentials;
If either agent or judge uses Copilot, the whole invocation uses the same direct,
unjournaled runner as the file CLI and reports `NoJournal`, not an SDK harness.
Fully journaled Copilot remains follow-up work.

A public archive cannot contain a user's absolute root. Release archives start
unconfigured and refuse MCP startup until an operator chooses a root as below.
Users do not need Python or this developer builder to install an archive.

## Native Squad through chat (optional)

With a separately qualified **standalone Squad 1.1.0** dependency, the same four
MCP tools can expose native v2 Create/Edit/Run/Test. Operators may supply
`--squad-program /absolute/qualified/squad --squad-release 1.1.0` to the developer
packager, or explicitly add that pair to the configured engine's MCP startup argv
and restart. This is local dependency configuration, not a bundled runtime,
authentication, downloader or upgrade. The default/release package does not infer
or install Squad. Installed-check discovery uses the configured startup flags;
it never initializes or casts during discovery.

Ask chat to **create a squad**. It proposes one Lead and up to seven specialists,
with generated task instructions, concrete models and reviewable examples; adjust
that proposal in chat before approval. Approved native Create conditionally runs
init → cast → enrich under the hood. Users never type Squad commands. Native
setup creates built-ins/state/templates/skills and local MCP configuration, and
may append `.gitignore`/`.gitattributes`. Prior local MCP/skill configuration,
linked targets, worktree pointers and partial/unsupported state refuse rather
than being adopted or overwritten. Existing supported setup is validated/reused.
These are native dependency side effects, not new kmx filesystem/shell tools.

Reopened `kmx://squad-member/{memberId}` views and member operations validate
native casting state, roster and enriched charter on each call. Invalid state
shows **not set up by kmx**, never guesses origin, and disables native Edit/Run/Test.
No kmx-owned durable mapping or historical pass is restored. Valid state does not
authenticate origin. Edit touches only Preferred/Task Instructions/Examples;
Run/Test report revision-bound local member previews, not a package or team pass.
Partial/unknown results need inspection, not automatic re-cast or rollback.
The native flow remains **chat-only**; the canvas below authors canonical files,
not native v2 members. There is no new Squad canvas, queue, posting, Ralph or dispatch.
Actual native installation/runtime qualification is platform-specific; Go tests
or cross-compilation do not qualify Squad on Windows/macOS.

<a id="read-only-agent-view"></a>

## Agent authoring canvas

Ask Copilot to make an agent. The builder skill creates it through kmx and then
opens the successful Create result's exact path in the canvas. To open an
existing agent, select **kmx** in the app's slash menu and enter
`/kmx open /home/me/project/greeter.agent.md`. Nested agent and canonical Squad
paths work too. `/kmx` or `/kmx help` shows usage without opening anything.

This is a host-model skill turn, not a model-free native slash command. The skill
asks for one built-in `open_canvas` call with the installed provider and a stable
instance ID. The provider checks the agent resource before every open, including
reopening an existing instance. Full paths distinguish same-named agents in
different repositories. An unreadable resource does not close an already-running
view. Opening does not edit, run or test the agent; the existing authoring
controls and their checks remain unchanged. No folder confirmation is required.

Without the built-in canvas opener, the skill uses the native opener's text
fallback. The manual native command `/agent-view greeter.agent.md` also displays
the projection as read-only text; canonical Squad paths work there too.

The main view shows the **Prompt** and an ordered list of **Tests**. Open a test
for **The agent gets**, **Passes if** and the actual **The agent answered** excerpt.
Going back or opening another test keeps its draft marked **Unsaved**; it never
silently saves or requires Cancel before editing something else.

On main, **Enter** saves pending edits through one revision-checked MCP Edit and
runs every test, including when nothing changed. In test detail, the save/run
control runs that test only through the existing case selection. **Shift+Enter**
adds a line. Typing or navigation alone never saves or runs. Additional canonical
instruction sections remain available in source details. Paragraph spacing and
inert inline formatting preserve source markers and untouched bytes when saving.
Title, identity, model and unrelated tests are not rewritten by a prompt edit.

Tests can be added, edited or removed; each nonempty test retains at least one
check. An explicit `examples: []` is a valid zero-test authoring state. Ad-hoc Run
with an input still works; Test refuses with “No tests yet — add or suggest one”.
Saving zero tests supplies no passing evidence, and run-profile export,
packaging and deployment still refuse an agent without test evidence.
Test saves reformat only the Examples YAML, including comments, and retain all
known check fields, including existing judge metadata. No judge or check is
inferred from an answer. Inputs, exact operands, code and answers stay literal.
Only current results have status colours. Editing locally retains known icon
shapes and counts in grey. Results already stale on the server stay neutral;
the panel does not invent or recover observations that the engine withholds. Unknown, incomplete, ambiguous and subset results never imply a
full pass. The sticky Tests header filters All / Not passing without reordering.
Dark/light tokens and the same layout support a 600px panel.

Test and ad-hoc Run use existing same-session MCP tools directly, not chat.
**Suggest tests** asks Copilot to add job-relevant tests to the saved agent through
the existing revision-checked Edit operation. It keeps the prompt, model and
existing tests and does not run them. The request status acknowledges the chat
request, not a completed save. Saved additions appear through the normal canvas
refresh; unsaved local drafts retain the existing outside-change reconciliation.
Chat reports what was actually saved. An explicit request for proposals only
leaves them in chat and says they are not saved. The host still enforces permissions. The private
asset bridge accepts only declared interactions for its fixed path, with token,
Host, exact Origin and size checks; it is not a generic executor.
The visible idle panel observes the same resource for outside changes. With a
draft, **Compare**, **Keep my edits** and **Use the new version** require a choice
before saving. Keeping edits explicitly chooses the observed new baseline;
no refusal is force-overwritten or automatically retried. Unknown execution
outcomes retain drafts and require deliberate reload, never an automatic unlock.
Failed saves stop Test. Test/Run have no atomic revision precondition; actual
returned revisions and selected case IDs must match before results are attributed.
A failed call also needs its own structured result to match the resource;
without that evidence, an older result cannot unlock a retry.
Stop, versions/restore, a new Model validated type, Ask Copilot and All versions
are not part of this canvas release; no inactive versions clock is shown.

Canvas runs explicitly request bounded answer excerpts with `showContent: true`,
so passing answers are available without a hidden setting. Those excerpts also
appear in MCP tool summaries, structured results and the latest `kmx://agent/`
resource, not only the panel. Answers remain inert text; formatting and links
cannot become controls. Direct MCP clients still opt in to content. Runner journal
retention is unchanged.

Saved checks are readable lines; click a line to edit it. Each failing check shows
its own Missing, Found or No match explanation. The input grows with its content.
Enter saves and runs the selected test; Esc discards its edits. The result pill
and right-aligned verdict words distinguish current results from dimmed last-run
results. Tests arrive together when the batch completes; a running indicator does
not imply per-test progress.

The typed resource remains `kmx://agent/{path}`; encode the entire path as one
URI component, for example
`kmx://agent/.squad%2Fagents%2Fgreeter%2Fcharter.md`.
An optional authoring snapshot carries exact source from the same bounded
descriptor read as its revision. Clipped display values are never save baselines.
Strict canonical charters are editable; older engines and minimal titled files
remain inspection-only. Inspection does not widen Run/Test parser acceptance.
There is no new tool, generic read, model route or filesystem writer. Source
details retain the complete path/revision and explicit Copy path with its
selectable manual fallback; no clipboard permissions change.

Latest results belong only to the current MCP process; a restart has no history.
Changed revisions withhold old results. An open canvas can keep already observed
verdicts dimmed for unchanged test definitions across source or model changes;
new or changed tests do not inherit them. This display-only memory is lost when
the canvas closes and never becomes a current resource. Without an observation,
tests say **Not run yet** and the Tests section offers **Run N tests**.
Windows Copilot app 1.1.30 with a WSL MCP engine demonstrated instruction
Edit → Test, mixed text/judge results and native rendering using an offline fake
model. Browser/unit checks cover inline formatting, explicit answer disclosure and result
presentation.
This is not live hosted-model, clean-machine or every desktop error-state
qualification. A loopback URL or browser mock alone is not desktop evidence.
The `/kmx` skill → built-in opener → WSL resource → visible panel path was also
demonstrated once on that Windows app, with one opener call and no agent
execution. Other platforms, marketplace-qualified provider IDs and every path
layout/instance-ID length have not been app-qualified by that check.

### Visual checks

These pairs show the Final boards beside the accepted native saved/edited
milestone. The native captures precede the final wording and paragraph-spacing
fixes. The detail pair shows the latest real-MCP **browser** check, not a native
or hosted-model run. Clock/history, stop and the new model-check type are deferred.

![Edited board beside the native visual checkpoint](../../docs/images/copilot-canvas-native-edited.png)
![Saved board beside the native visual checkpoint](../../docs/images/copilot-canvas-native-saved.png)
![Narrow detail board beside the latest real-MCP browser](../../docs/images/copilot-canvas-detail-browser.png)

## Results and verification

Run/Test return `schemaVersion: 1`, `kind: kmx.agent-file-preview`,
file revision, selected model, full/subset and pass/fail/unknown results.
A passing subset still has `fullPass: false`. Complete preview data is in
`structuredContent`; text is a compact human summary, not JSON. Completed
assertion failures are normal tool results: `isError: false` does not mean pass.
Preview file.path is a display basename, **not the original Edit target**.
Boundary refusals have a safe reason code instead of a preview.
Default tool output omits answers/operands. Explicit `showContent` includes all
bounded excerpts; optional `showFailedContent` includes only failed saved examples. Retention remains `not_requested`; file-scoped Explain is deferred.
These results grant no native shipping evidence or Publish/Deploy authority.
Squad placement alone does not register a team member.

```sh
python3 -m unittest discover -s plugins/copilot -p 'test_*.py' -v
```

Tests build the real `./cmd/kmx`, package and relocate it to a simulated cache,
launch from an unrelated cwd and perform Create → Run fail → revision-checked
Edit → partial Test → full Test pass using the skill's canonical sample.
They check root isolation, unchanged expectations, overwrite/stale/path refusals,
protocol stdout and result privacy. This uses kmx's deterministic fake model,
not a substitute MCP process or real-model qualification.
Linux/macOS CI runs the real CLI loop. **Windows runs mechanical packaging
checks only: main still blocks native CLI builds.** A generated Windows command
is not a qualified Windows release or installed Copilot chat.

## Delivery gates

The helper refuses macOS source bytes with `com.apple.quarantine` and never
clears quarantine or relaxes OS protections. This is not a Gatekeeper,
notarization or codesign verdict. Record actual native archive/cache behavior,
permissions and OS approval separately. On Windows, record signing identity and
SmartScreen/OS refusal. Signed checksums are not Authenticode or notarization.

## Release archives and explicit setup

The release job packages the already-built, signature-verified engines into
`kmx-agent-builder-{linux,darwin}-{amd64,arm64}.tar.gz`. It validates each engine's
Go command path, GOOS/GOARCH and clean source revision without executing foreign
binaries; native version stamping is checked by the existing release gate.
Go omits ldflags under trimpath, so cross-platform version identity also relies
on the reviewed release job and signed checksum inventory, not a fabricated
metadata field. No Windows archive is published while native CLI is guarded.

`plugin-checksums.txt` and its Sigstore bundle are separate from the unchanged
four-binary `checksums.txt` consumed by the installer/Homebrew. Both use the
existing keyless release workflow identity and OIDC issuer; checks performed
by the same job are not independent publisher authentication. Dry runs sign and
upload artifacts but publish no release. Archives have an exact file
inventory including the Release writer sample, fixed
tar ownership/timestamps/modes and the pinned engine digest.

1. Obtain the archive through the trusted GitHub release. Compare its SHA256
   with the expected digest from that release's `plugin-checksums.txt` or release
   guidance before extraction or execution. Both rely on the same GitHub account
   and HTTPS trust, not independent publisher authentication. Linux uses
   `sha256sum FILE`; macOS uses `shasum -a 256 FILE`. Operators verifying Sigstore
   must pin the exact `release.yml@refs/tags/VERSION` certificate identity and
   issuer `https://token.actions.githubusercontent.com`, not a wildcard.
2. Extract into a new directory using the OS archive tool. Review `SETUP.md`.
3. Edit **only** `mcp.json` and `selected-root.json`: replace `__SELECTED_ROOT__`
   with the identical, existing absolute repository root in both. Keep valid
   JSON; no filesystem root, control characters or literal `${PLUGIN_ROOT}` /
   `${PLUGIN_DATA}` tokens. Do not infer a root from the extraction/cache cwd.
   Do not change the engine or skill. These local config edits are not covered
   by the pristine archive signature; review them separately.
4. Install the configured directory and confirm the root again in chat:

```sh
copilot plugin install ./extracted-plugin
copilot plugin list
copilot plugin uninstall kmx-agent-builder
```

The [Copilot creating guide](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/plugins-creating)
documents local-directory installation and cached components: reinstall to
refresh local changes. Archive URLs are not direct install sources.
App marketplace installation needs separate evidence; CLI tests are not app
support. The packager performs no host installation or release publication.
Reconfigure/reinstall and restart to change roots. For updates, verify the new
archive and reapply the intended root deliberately; do not mutate cached bytes.
Real-model integration, native OS trust qualification and the newcomer pilot
remain separate acceptance activities.

## Installed-host check and chat qualification

With qualified Copilot CLI **1.0.86-2** or **1.0.102** (`--no-auto-update`), the
opt-in developer check uses a **new private profile** through COPILOT_HOME, not
the operator's normal configuration. It refuses other host versions before
installation; requalify isolation first.
This relies on the trusted host honoring its documented configuration boundary,
not OS-level containment. Supply the pinned engine
digest from the verified bundle:

```sh
python3 plugins/copilot/check_install.py --plugin /configured/kmx-agent-builder \
  --engine-sha256 <64-lowercase-hex> --home /new/private/copilot-check-profile
```

It accepts qualified CLI 1.0.86-2 or 1.0.102 and refuses unconfigured/mismatched
roots or engine hashes; exercises real local
install/list/skill discovery, cache byte/execute checks, exactly four real MCP
tools, uninstall and reinstall. Python is needed only for this checkout check,
not for end-user installation. No authentication, inference or trust bypass is
performed by this check.

Then qualify chat separately with the installed skill: create its fake greeter,
Run/Test, edit only Hello to Goodbye, observe greeting/greeting-text fail,
restore only the instruction, partial Test then full Test. Confirm source bytes,
unchanged assertion and structured results; tool-call success alone is not pass.
Deny shell/direct writes and enable only the kmx server; reading packaged skill
resources uses the host read tool. Record host version and denied attempts too.
The qualified CLI warns that local-directory installation is deprecated but
still works; this does not authorize creating/publishing a marketplace. A future host
removing that route is a compatibility blocker, not a reason to bypass approval.

## Native archive CI qualification

After signing, Linux and macOS release workers verify the exact cross-built
archive, configure an owned temporary root and exercise install, uninstall and
reinstall with pinned Copilot CLI 1.0.102 in a fresh COPILOT_HOME. Publication
waits for both workers. Offline checks exercise real MCP discovery and sample
creation without model sends; they do not prove hosted model quality or
interactive desktop rendering. The WSL source plugin has separate metadata and
asset-inventory checks and does not use the native archive setup path.
