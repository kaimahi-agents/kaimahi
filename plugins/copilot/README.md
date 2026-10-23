# Copilot agent-builder plugin

This Copilot app/CLI plugin contains a skill, declarative MCP config and a
native Copilot agent authoring canvas with a read-only text fallback. It is not a VS Code
extension. The source tree is not an installable package; native bundles are
generated and signed by the release workflow, not committed here. Canvas actions
use the existing MCP operations, not an in-plugin downloader, file writer or second runner.

For the Copilot-default Release writer setup, see [TRY-IT.md](../../TRY-IT.md).
The external [try-it.sh](try-it.sh) helper downloads a complete platform archive,
checks its release checksum over HTTPS, asks the user to confirm a root, copies
one bundled sample without overwrite, configures the Copilot route and installs
from a fresh local directory. This is checksum verification, not independent
signature verification. It needs no extra macOS packages. `--dry-run` stages and
configures only; it does not sign in, install, invoke models or open the app.
Older archives without the exact try-it schema/inventory refuse. The sample
uses real Copilot with judge checks; fake remains an offline/CI route, not a
release-writing simulation.
The source directory deliberately has no `bin/kmx`; do not install it directly.

## Package contract

The package uses [Agent Plugins 1.0](https://agent-plugins.org/plugin-authors/build-an-agent-plugin.md):
root [plugin.json](plugin.json), root [mcp.json](mcp.json) and
[skills/kmx-agent-builder/SKILL.md](skills/kmx-agent-builder/SKILL.md) and
[skills/kmx/SKILL.md](skills/kmx/SKILL.md).
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

## Repository binding and real tools

The developer binds an absolute, filesystem-resolved root before installation.
It is encoded in startup argv as `kmx mcp --root ABSOLUTE_DIR`, not an
assistant-controlled tool argument. [selected-root.json](selected-root.json)
contains the same root for user confirmation. Never infer root from cwd,
plugin-cache location, MCP client roots or an invented host variable.
Resolved roots containing literal `${PLUGIN_ROOT}` or `${PLUGIN_DATA}` tokens
refuse: a plugin host would expand them in startup arguments.
Changing repositories requires deliberate reconfiguration/reinstallation and
a server restart, not an implicit rebind during a tool call.

The bundled engine exposes exactly lowercase `create`, `edit`, `run` and
`test`. Canonical branches require `schemaVersion: 1` and root-relative `path`; Create
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
uses prior explicit `kmx model login` credentials and an exact listed
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

After setup confirms the selected root, select the **kmx** skill in the app's slash
menu and enter `/kmx open greeter.agent.md`. Canonical Squad paths such as
`/kmx open .squad/agents/greeter/charter.md` work too. `/kmx` or `/kmx help` shows
usage without opening anything.

This is a host-model skill turn, not a model-free native slash command. The skill
asks for one built-in `open_canvas` call with the installed provider and a stable
instance ID. The provider checks the path and reads the existing root-confined
resource before every open, including reopening an existing instance. An
unreadable resource refuses that open; it does not close an already-running
view. It never infers a root from the app folder, changes the
binding, or asks for another root confirmation. Opening does not create, edit,
run or test an agent; the existing authoring controls and their checks remain
unchanged.

Without the built-in canvas opener, the skill uses the native opener's text
fallback. The manual native command `/agent-view greeter.agent.md` also displays
the projection as read-only text; canonical Squad paths work there too.

Instructions are directly editable: **Enter** saves through revision-checked MCP
Edit and then tests; **Shift+Enter** adds a line. Additional instructions retain
the other canonical responsibility sections. Inline code and emphasis are inert
formatting, with source markers retained when saving. Title, identity, model,
untouched instructions and Examples are not rewritten by an instruction edit.
Typing alone never saves or runs anything.

Add, edit or delete examples inline, retaining at least one example and check.
Example saves reformat only the Examples YAML, including comments, and preserve
all check fields, including judge metadata. Judge labels come from authored IDs;
full rubrics remain in Check details. No check is inferred from an answer.
Inputs, exact operands and model answers remain literal. Failing examples expand;
passing examples stay compact. Partial, unknown, ambiguous, stale and unsaved
states never imply a full pass.

Test and ad-hoc Run use existing same-session MCP tools directly, not chat.
**Suggest more** alone asks Copilot for proposals in chat; it does not save them.
The host still enforces its permissions. The private asset bridge permits only
declared interactions for its fixed path, with token, Host, exact Origin and
size checks. It is not a generic executor. Acknowledged actions refresh the
resource; there is no background watcher. Conflicts and uncertain outcomes retain
the draft and require deliberate reload. Failed saves stop Test. Test/Run have
no atomic revision precondition; changed result revisions are labelled stale,
never claimed as tests of the draft.

Failed saved-example answers are shown by default as bounded **untrusted
excerpts**. **Include passing answers** explicitly adds other excerpts.
The canvas requests `showFailedContent: true`, not implicit `showContent: true`:
passing and unknown answers stay out of safe results, MCP summaries and resources
when the checkbox is off. Ad-hoc answers still require `showContent`.
Fake/local journal retention is unchanged; projection controls do not promise
that the underlying runner does not journal content.

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
Changed revisions withhold old results. No observation means **No current result**.
Windows Copilot app 1.1.30 with a WSL MCP engine demonstrated instruction
Edit → Test, mixed text/judge results and native rendering using an offline fake
model. Browser/unit checks cover inline formatting and failure-only disclosure.
This is not live hosted-model, clean-machine or every desktop error-state
qualification. A loopback URL or browser mock alone is not desktop evidence.
The `/kmx` skill → built-in opener → WSL resource → visible panel path was also
demonstrated once on that Windows app, with one opener call and no agent
execution. Other platforms, marketplace-qualified provider IDs and every path
layout/instance-ID length have not been app-qualified by that check.

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
inventory including the Release writer sample and try-it schema marker, fixed
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

## Try-it model routes

The helper checks the Copilot catalog before installing, after explicit device
sign-in if necessary. The chosen exact model is written to Preferred and every
judgeModel. An unavailable suggested model stops until the user chooses a
listed ID; there is no automatic model or credential fallback in setup.
`judgeVersion: default` is a declaration, not a pinned hosted model revision.
The exact echoed model ID remains checked by kmx.

For a local model you already operate, choose its exact model ID for Preferred
and each judgeModel, and its declared version for judgeVersion. In the **staged**
plugin's mcp.json, preserve the confirmed root and replace the startup suffix
`["--provider", "copilot"]` with, for example,
`["--provider", "ollama", "--endpoint", "http://127.0.0.1:11434/v1"]`.
The endpoint must support kmx's OpenAI-compatible request contract, including
judging; local fake/compatible runs retain cleartext private journals as described
above. Do not install a model, guess its identity or configure secrets through
charter instructions. Reinstall the staged directory and start a new session.
On Windows, keep the `wsl.exe -d DISTRO -e ENGINE mcp --root ROOT` prefix;
localhost is inside that WSL distro.

The Windows try-it route is a separate operator-reviewed startup configuration:
Windows plugin assets are copied to a fresh Windows-profile staging directory,
but `wsl.exe` invokes the checksum-verified ELF at its printed private WSL path.
Keep that WSL directory while installed. The cached Windows ELF copy is not
executed. Root/distro/engine are argv elements, never a shell command. Setup
checks the model catalog through that same non-login WSL invocation to catch
unavailable credentials before installation. No Windows native binary claim is
made. Reconfiguration changes local startup files, not the pristine archive
signature; neither this helper nor checksum verification grants OS trust.

CI runs script refusal/dry-run contracts on macOS and Linux, then installs,
uninstalls and reinstalls the configured bundle with pinned Copilot CLI 1.0.102
in a fresh COPILOT_HOME. Disabled host built-ins are permitted; pre-existing user
plugins refuse. The release workflow repeats this against the exact cross-built
Linux/macOS archives before publication. Offline checks exercise real MCP
Create and discovery without model sends; they do not prove hosted model quality
or interactive Mac desktop rendering. No separate human Mac acceptance gate is
required for this preview.
