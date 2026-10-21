# Copilot inference and tools

Host Copilot chat inference is independent from native workspace evaluation
and Deploy. Copilot wizard authoring is retired; automatic wizard tool attachment
and live source tools/inference edits remain retired. Outside the wizard,
`/inference-copilot` discovers models through the authenticated installed CLI
without rewriting the saved Orka Provider. Native eval/Deploy requires its exact
packaged model and native binding, not host Copilot execution.

On a local kind target, `/inference-copilot` opens a discovered-model picker.
Host Copilot inference is refused on remote targets; configure a cluster Provider
there instead. `/inference-local` returns to the Agent's Orka Provider; `/retry`
repeats the previous prompt using the chosen route. Each reply shows total elapsed
request time.

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
