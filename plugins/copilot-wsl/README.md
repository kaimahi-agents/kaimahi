# kmx in Windows Copilot through WSL

The plugin contains skills, canvas assets and MCP configuration. It uses the
Linux kmx binary in your default WSL distribution; it does not bundle an engine.

## Install

1. In WSL, install the v0.9.0-rc.2 Linux kmx binary at `/usr/local/bin/kmx`:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/v0.9.0-rc.2/install.sh | sudo sh -s -- --version=v0.9.0-rc.2 --bin-dir=/usr/local/bin
   ```

   The installer downloads the binary and checks its published SHA256 over HTTPS;
   it does not verify the separate signature bundle.

2. In a Windows terminal with Copilot installed:

   ```powershell
   copilot plugin install kaimahi-agents/kaimahi:plugins/copilot-wsl
   ```

There is no folder or model to configure during installation. The fixed command
is `wsl.exe -e /usr/local/bin/kmx mcp`. The WSL and native-bundle plugins share the
name `kmx-agent-builder`; installing one replaces the other. The Copilot CLI warns
that direct plugin installs are deprecated; this install still works, and a
marketplace install will come later.

## Use

In Copilot, ask it to create an agent through kmx and open the agent canvas. Give
it a full WSL path when the file is outside the current directory. The same
server handles files in unrelated repositories.

Choose a runtime model in the canvas: picker override → authored `Model.Preferred`
→ known chat model → `gpt-5.6-terra`. Model may be omitted; a picker choice never
rewrites the agent file. Changing the model makes prior results stale until
another run. A following default reads the chat model when Run/Test starts and
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
prompts and inputs to the selected model.
If the host has no canvas renderer, the same kmx tools remain available in text.

See the [agent-file guide](../../docs/agent-file.md) for authoring and checks,
and the [native packaging guide](../copilot/README.md) for non-WSL bundles.
