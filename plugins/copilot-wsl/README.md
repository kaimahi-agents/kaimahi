# kmx in Windows Copilot through WSL

The plugin contains skills, canvas assets and MCP configuration. It uses the
Linux kmx binary in your default WSL distribution; it does not bundle an engine.
The v0.9.0-rc.4 install below exposes Create/Edit/Run/Test and the revision-checked
`set_tool_state` folder-settings writer. Older rc.2 binaries expose only the first
four tools.
Internal preview children retain only Create/Edit/Run/Test and never gain
config-write authority.

## Install

1. In WSL, install the paired v0.9.0-rc.4 prerelease Linux binary at `/usr/local/bin/kmx`:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/v0.9.0-rc.4/install.sh | sudo sh -s -- --version=v0.9.0-rc.4 --bin-dir=/usr/local/bin
   ```

   The installer downloads the binary and checks its published SHA256 over HTTPS;
   it does not verify the separate signature bundle.

2. In a Windows terminal with Copilot installed:

   ```powershell
   copilot plugin marketplace add kaimahi-agents/kaimahi
   copilot plugin install kmx-agent-builder@kmx-plugins
   ```

There is no folder or model to configure during installation. The fixed command
is `wsl.exe -e /usr/local/bin/kmx mcp`. The catalog pins this plugin's source to
the matching release commit. It does not install or update `/usr/local/bin/kmx`.
The WSL and native-bundle plugins share the name `kmx-agent-builder`; PATH uses
`kmx-agent-builder-path`. Install only one variant per host profile.
For an existing direct install, follow [migration and paired upgrades](../../docs/copilot-app.md#existing-direct-installs-and-upgrades), rather than leaving both registrations active.

**Release boundary:** The v0.9.0-rc.4 marketplace plugin can display bounded,
read-only fallback text, but its `open_kmx_canvas` result does not return a
structured full revision, source or editability and cannot seed a
revision-checked Edit in a fresh chat. The structured text workflow described
below is unreleased current-main behavior until a later paired release.

## Use

In Copilot, ask: `Create a kmx agent called Release notes that writes a short
release note from a supplied pull request title, and open it.` Give it a
full WSL path when the file is outside the current directory. The same server
handles files in unrelated repositories.

To open an existing file without a model turn, use
`/kmx-canvas open /mnt/c/work/release-notes.agent.md`. Keep path spaces literal,
without surrounding quotes. `/kmx-canvas` or `/kmx-canvas help` shows usage;
`/agent-view FULL_PATH` shows bounded read-only text. Native `/kmx-canvas open`
also displays only that bounded fallback. On current main, the separate chat
tool's text mode returns exact source/revision when the checked resource is
editable, so a fresh extension-backed chat can continue with revision-checked
Edit. Native open was checked in the Copilot app with a main build before rc.4
that includes the fix, not the released rc.4 pair. If it fails or shows nothing, try
`Open the kmx agent for FULL_PATH` through chat.

In the interactive CLI, use `copilot --experimental` to load those native
commands. Without the extension, `/agent-view` is unavailable too. Select the
full installed plugin skill entry: `/kmx-agent-builder:kmx open FULL_PATH` or
`/kmx-agent-builder:kmx-agent-builder` followed by a builder task. Short `/kmx`
is not a universal CLI command.
See the [launch table and CLI spellings](../../docs/copilot-app.md#launch-commands-and-phrases)
for create/open/run/test requests, saved-snapshot test additions and recovery.
CLI command checks are not a new Copilot-app acceptance claim.

Choose a runtime model in the canvas: picker override → authored `Model.Preferred`
→ known chat model → `gpt-5.6-terra`. Model may be omitted; a picker choice never
rewrites the agent file. The open canvas keeps results for two models: picking a
model with a result for the same file revision restores it without rerunning.
Older results stay grey, with their model and revision context. A newer
single-test attempt takes precedence over an older full-run summary.
A following default reads the chat model when Run/Test starts and
freezes it through save/execution; the displayed model can lag until idle refresh.
Choices load when the menu opens and are cached; catalog failure
does not block editing or offline tests. Familiar IDs get display names and
family groups; unknown IDs remain literal and execution always uses the exact ID.
Selecting a model pins it, including the current default; use the following action
to return to the agent file or automatic default.
Older hosts without the optional current-model getter use the established default.
Transient getter failures or metadata deadlines retain the last valid model;
explicit unknown/auto
responses clear it.
Unbound real-model runs use Copilot by default; effective `fake` is deterministic
and offline.
`kmx model list --provider copilot` lists the available model names.

Edit prompts and tests in the canvas. Enter saves and tests; Shift+Enter inserts
one line break. Long prompts scroll inside their box, with the save/run footer
outside the scroll area. Saved agent files remain ordinary Markdown. Tests send
prompts and inputs to the selected model. On hosts that forward native tool
progress, test rows update as cases finish. The final full Test result remains
authoritative. Local model comparison is lost when the canvas is closed and
reopened. An unconfirmed execution requires reload rather than silently retrying.
On current main, if the host has no canvas renderer, text mode is a normal
success path. Chat
reports structured Test rows and keeps a chosen runtime model for that exact file
within the conversation. It lists newly saved tests as not run, requests answer
content only when asked and never reruns to recover omitted content. Without the
extension, a same-chat workflow can continue from returned source/revision, but a
fresh chat cannot safely reopen an existing file. With rc.4, fallback text is
display-only and opening an existing file does not establish an Edit baseline.

See the [agent-file guide](../../docs/agent-file.md) for authoring and checks,
and the [macOS/PATH plugin guide](../copilot/README.md) for the Mac install and
legacy native bundles.
