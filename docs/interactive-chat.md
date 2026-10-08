# Interactive chat entry points

Use `/inference` to choose **Foundry (Azure login)**, Copilot CLI, or the Agent's
native Orka Provider. `/inference-foundry` opens saved configuration, Azure browse
or manual endpoint setup. Foundry host mode uses Entra authentication and native
model function calls with KMX's supported cluster HTTP tools. See
[local Foundry inference](local-foundry-inference.md) for scope and setup.

Quickstart's post-wizard chat and direct Orka chat now use the same entry point,
backend, renderer and command loop:

```sh
kmx agent chat hello-world-agent
kmx agent chat --runtime orka --namespace orka-system hello-world-agent
kmx agent chat --azure-discovery sdk hello-world-agent
```

The default runtime selection discovers Orka's API and checks the named Agent
in `orka-system` (or `--namespace`). A matching Orka Agent uses the wizard chat
shell: sticky agent/location header, tools, `/agent`, `/lift`, inference selection,
verbose controls, response timings and reusable connections. Discovery/read errors
are reported rather than silently connecting to a different runtime.

If no Orka Agent matches, the command says so. There is no fallback: the Kagent
create-only adapter is not registered for chat or discovery. Its former
resumable chat, A2A approval flow and in-chat preset governance remain removed,
and `--runtime kagent` is refused rather than resolved to Orka. Those
runtime-specific semantics are not implemented by the Orka Task API.

An optional message after the Agent name is sent once before the interactive
prompt. Orka chat is always a session, so that message is not a one-shot; use
`kmx agent run` for one Task and an answer-only stdout. The `--interactive` flag
is removed; v0.4.1 and earlier require it. Direct Orka chat starts with
the Agent's current Provider; use
`/inference-copilot` to choose Copilot, or `/inference-local` to return. The wizard
retains its explicitly chosen inference source when entering this same shell.

Shell Agent-name completion reads Orka names in the selected namespace, and respects the
namespace flags. No additional runtime CLI installation is needed for Orka chat.

Typing `/` opens a floating completion menu above the message editor. It filters
as you type and lists only the active backend's commands. Up/Down selects; Tab
or Enter inserts a completion, and Enter on the completed command runs it. The
menu clears on submission and does not appear in approval/answer prompts.

Full-screen chat reflows when terminal dimensions change, retaining typed input
and the completion selection. It redraws the sticky header and anchors a fresh
editor below the existing transcript rather than erasing with obsolete cursor
coordinates. Resize itself never submits a message or approval. The non-full-screen
raw-input fallback retains its conservative resize-abort behavior.

## Message editing and scrollback

Full-screen Orka chat uses a retained transcript viewport with a pinned header and
message editor. The same interface is used after quickstart and by Orka
`agent chat`.

- **Ctrl-W:** delete the word before the cursor.
- **Ctrl-A / Ctrl-E:** move to the beginning/end of the message.
- **Left / Right:** move within the message; edits happen at the cursor.
- **Up / Down:** recall sent messages; Down past the newest restores the draft.
  When slash completions are open, Up/Down selects a completion instead.
- **PageUp / PageDown**, **Shift-Up / Shift-Down**, or the **mouse wheel:** scroll
  conversation history while leaving the header and editor fixed.
- **Ctrl-Home / Ctrl-End:** oldest history / latest messages.
- **Shift-Enter / Alt-Enter:** insert a newline. Enter sends when idle.

History can be browsed and the next message drafted while a response is running;
Enter does not submit another request until the current one finishes. Ctrl-C
cancels the active worker and restores the terminal. `/agent` resets conversation
and recall history on a successful switch. Tool-call headings and payloads are
indented underneath the agent heading, including after wrapping on narrow windows.
