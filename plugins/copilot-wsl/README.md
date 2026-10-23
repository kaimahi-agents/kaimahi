# kmx in Windows Copilot through WSL

The plugin contains skills, canvas assets and MCP configuration. It uses the
Linux kmx binary in your default WSL distribution; it does not bundle an engine.

## Install

1. In WSL, install a current Linux kmx binary at `/usr/local/bin/kmx`. While
   iterating from this checkout:

   ```sh
   go build -o bin/kmx ./cmd/kmx && sudo install -m 0755 bin/kmx /usr/local/bin/kmx
   ```

2. In a Windows terminal with Copilot installed:

   ```powershell
   copilot plugin install kaimahi-agents/kaimahi:plugins/copilot-wsl
   ```

There is no folder or model to configure during installation. The fixed command
is `wsl.exe -e /usr/local/bin/kmx mcp`. The WSL and native-bundle plugins share the
name `kmx-agent-builder`; installing one replaces the other.

## Use

In Copilot, ask it to create an agent through kmx and open the agent canvas. Give
it a full WSL path when the file is outside the current directory. The same
server handles files in unrelated repositories.

Choose a runtime model in the canvas: picker override → authored `Model.Preferred`
→ `gpt-5.6-terra`. Model may be omitted; a picker choice never rewrites the agent
file. Changing the model makes prior results stale until another run. Choices
load on picker focus; catalog failure does not block editing or offline tests.
Unbound real-model runs use Copilot by default; effective `fake` is deterministic
and offline.
`kmx model list --provider copilot` lists the available model names.

Edit prompts and tests in the canvas. Enter saves and tests; saved agent files
remain ordinary Markdown. Tests send prompts and inputs to the selected model.
If the host has no canvas renderer, the same kmx tools remain available in text.

See the [agent-file guide](../../docs/agent-file.md) for authoring and checks,
and the [native packaging guide](../copilot/README.md) for non-WSL bundles.
