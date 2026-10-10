# Copilot inference and tools

Interactive quickstart always waits for discovery and requires an explicit
inference choice, including reruns and cases with only one option. No source is
auto-selected.
The legacy `--inference` flag does not bypass the picker.

Discovery checks local runtimes, Copilot installation, CLI `auth.getStatus`, and
login status. Step 2, **Inference Provider**, groups authenticated Copilot into one
**Copilot · auto** entry. **KMX-Managed Ollama** is the fallback when no alternatives
are found; the user still confirms it. Detailed model discovery remains available
through `/inference-copilot` in chat using `models.list`.

New agents can choose bundled Ollama, detected host models, or a Copilot model.
Existing agents can choose their current Orka Provider or Copilot auto.
Choosing Copilot does not rewrite the stored Orka Provider. Local setup remains
available for explicit comparisons.

In chat, `/inference-copilot` opens a discovered-model picker. `/inference-local`
returns to the Agent's Orka Provider; `/retry` repeats the previous prompt using
the chosen route. Each reply shows total elapsed request time.

## Tool translation

KMX supplies enabled registered HTTP Tool descriptions and parameter schemas to
`copilot -p --model <selected>`. The model returns one JSON action: a tool call or
a final answer. KMX validates the tool name and arguments, executes the HTTP call,
and feeds its result into the next prompt. It permits at most eight steps and
five minutes per turn, with bounded request and response sizes and no automatic
retry after execution failures. Ctrl-C cancels the subprocess and active requests.

Supported tools are POST HTTP tools pointing to a Service in the Agent namespace,
without special headers, auth Secrets, outbound policy or MCP configuration.
Calls use an owned loopback port-forward and the selected Kubernetes context.
The policy-backed Kubernetes listing tool runs through native Orka. Copilot
reports it unavailable rather than tunnelling around the gateway policy.

Tools requiring Orka approval, built-in memory tools, MCP tools and authenticated
or policy-backed tools are not executed by this adapter. Explicit unsupported
references are reported; use the local Orka path for those tools. This adapter
uses the operator's Kubernetes access and does not claim Orka worker execution
or its authorization semantics.

Copilot native tools are restricted to an adapter-only allowlist; the registered
Orka tools execute in KMX. Copilot output is decoded from JSON events to avoid
terminal wrapping corrupting tool arguments. Instructions come from the selected
Agent; each turn runs in an empty temporary directory with no repository custom
instructions and no inherited conversation. Credentials remain managed by the
installed CLI, including GitHub CLI login. No credential files are read by KMX.
