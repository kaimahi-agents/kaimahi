# Copilot inference and tools

## Markdown-agent spike (unreleased)

The [scoped tool design](markdown-agent-inner-loop.md#proposed-infer-and-review-scoped-local-tools)
now has a first implementation: explicit inference-assisted proposals and reviewed
repository-bound GitHub PR reads, plus explicitly authored local CLI bindings.

## Same-file CLI tools and compatibility checks

Keep native tool names in front matter and put executable metadata in a final
`## Tool bindings` section. KMX extracts this section before sending instructions
to Copilot. This section is a KMX extension; loading the file directly in another
host does not install or implement its tools.

````markdown
---
name: dg-cluster-reader
description: Inspects approved development clusters
tools: [get_dg_cluster]
---

Use get_dg_cluster for development cluster questions.
Explain when a cluster is outside the allowed scope; do not invent results.

## Tool bindings

```yaml kmx-tool
name: get_dg_cluster
description: Read one approved dg development cluster
command: az
args:
  - aks
  - show
  - --subscription
  - YOUR-SUBSCRIPTION
  - --resource-group
  - dg-development
  - --name
  - argument: cluster
  - --output
  - json
  - --only-show-errors
parameters:
  cluster:
    type: string
    pattern: '^dg-[a-z0-9][a-z0-9-]{0,59}$'
    max-length: 63
execution:
  timeout: 15s
  max-output-bytes: 65536
  approval: granted-scope
validation:
  args: [version, --output, json]
  exit-code: 0
  stdout-pattern: '"azure-cli"\s*:\s*"2\.'
  timeout: 5s
```
````

Replace the example's subscription/resource group with your intended scope. The
regex applies only to the model-supplied cluster operand; the model cannot select
another executable, subscription, resource group, flags or arbitrary arguments.
All parameters are required, and undeclared parameters refuse before execution.
String arguments require a Go/RE2 anchored pattern or explicit enum, default to
256 bytes (up to 4096 via max-length), and refuse control characters or option-like
leading hyphens. Integers require nonnegative minimum/maximum bounds. Argument
placeholders are individual argv entries, never shell interpolation; the first
argument must be a reviewed literal subcommand/flag. Up to eight bindings, sixteen
parameters and sixty-four argv entries are supported.

### Resolve, review, validate, activate

```sh
kmx validate dg-cluster-reader.agent.md
kmx tools approve dg-cluster-reader.agent.md
kmx tools approve dg-cluster-reader.agent.md --apply
kmx run dg-cluster-reader.agent.md
# Or apply the reviewed binding to a running instance:
kmx run dg-cluster-reader.agent.md --update
```

`command: az` resolves using your PATH, so different users can keep different
installation paths without editing the file. Review displays the resolved absolute
path and SHA-256 along with the exact argv, constraints and validation command.
Plain validate/approve is offline inspection: no command, probe or login runs.
`--apply` asks before executing the declared probes and saving the grant. Scripts
must explicitly use `--apply --yes`. Output can be text/JSON/YAML.

Validation always uses the same resolved executable with **fixed args**, an expected
exit code and Go-regex stdout match. It has its own default five-second timeout
(maximum fifteen seconds) and 16 KiB output bound. The probe is optional; absence
is reported as "no probe declared". Failure, timeout, malformed output or source
changes prevent approval. Approved probes run again on start/update before tools
activate; failure leaves an existing running version unchanged. A version probe
establishes only its declared compatibility check—not login, API reachability or
cluster health. A probe can itself have side effects: review it as executable code.

The local grant binds the whole definition (including probes/constraints) and each
resolved executable. PATH resolving elsewhere or launcher bytes changing requires
reapproval. Interpreter-backed launchers may depend on untracked interpreter/modules;
the hash does not attest that entire installation. Calls run as your user with
inherited credentials, in an isolated temporary working directory. There is no
filesystem/network sandbox and no automatic read-only guarantee for arbitrary
binaries. Use native Azure/GitHub authorization for credential-level restrictions.
This first slice supports **granted-scope** only: explicit approval enables the
reviewed commands unattended. Per-call and once-per-session approval UI is not yet
implemented; unsupported approval modes refuse rather than silently auto-approve.

Execution has a default 15-second deadline (up to two minutes), bounded stdout,
withheld stderr and process-group cancellation. Credential-shaped outputs are
withheld. At most eight tool calls run per model turn. No binary is auto-installed,
no shell aliases/functions are resolved, and no automatic retry is performed.

The checked-in [Go-version agent](../.github/agents/local-go-version.agent.md)
demonstrates a harmless fixed CLI call with a compatibility probe; its
[mock tests](../.github/agent-tests/local-go-version.agent.test.md) validate response
handling without executing the binary. A live exercise resolved Go from PATH,
passed the probe, invoked the actual tool through Copilot and reported its version;
the separate mocked suite passed. Native deployment still refuses local tools.

## Suggest and approve PR-monitor tools

### General suggestions (default)

`kmx tools suggest FILE` now analyzes the definition once and proposes **zero to
three scoped CLI commands**, not a GitHub-only catalog. Each proposal includes a
reason, literal resource scope, fixed argv/subcommands, validated operands, an
explicit timeout of at most 30 seconds and at most 16 KiB output. String operands
need regex/enum limits and a maximum length of 1–256 bytes. Missing repository,
subscription, resource-group or other required scope is returned as clarification
items; an incomplete tool cannot be selected. A persona agent can receive no tools.
No commands or compatibility probes execute during suggestion.

```sh
kmx tools suggest agent.agent.md
kmx tools review p-PROPOSAL_ID
kmx tools review p-PROPOSAL_ID --apply --select tool_name
kmx tools approve agent.agent.md --apply
```

The review applies only selected declarations to `tools: []` and a final Tool
bindings section, preserving the instruction body. Existing tool-enabled files
require explicit manual merging rather than automatic replacement. Runtime access
still requires the separate local executable approval. Paired tests must acquire
mock coverage for new tools. The planner is advisory: the reviewer must verify that
the displayed command scope matches the intended work; regex/argv validation does
not prove a generic binary has no other side effects.

### Fixed GitHub templates (explicit --repo)

Start with the checked-in [PR monitor](../.github/agents/pr-monitor.agent.md) and
its [mock tests](../.github/agent-tests/pr-monitor.agent.test.md):

```sh
kmx tools suggest .github/agents/pr-monitor.agent.md \
  --repo kaimahi-agents/kaimahi
kmx tools review p-PROPOSAL_ID
kmx tools review p-PROPOSAL_ID --apply
kmx run .github/agents/pr-monitor.agent.md
kmx chat pr-monitor
```

Suggest sends only the definition and a bounded capability catalog to a tool-free
Copilot planner. It requires an explicit literal OWNER/REPO and can select only
`github.pr.list/v1` and `github.pr.read/v1`. It cannot run tools, grant permissions,
modify the agent file or invent executable commands. It reports unsupported needs
as gaps. Review shows exact scope, tools, auth executable/digest and limits; apply
requires confirmation (default No) or explicit `--apply --yes` for scripts.
`--select github_pr_read` approves a subset; `--repo OWNER/REPO` adjusts scope
explicitly before approval. Definition/binding edits during review refuse.

Accepted grants are private overlays under `KMX_HOME/agents/tools/`, keyed by source
path and bound to the definition hash, adapter, repository, tool set and gh binary
hash. No source rewrite is needed: `tools: []` is valid for the portable definition;
KMX supplies only approved overlay handlers to its session. A project copy does not
carry permission grants. Existing instances stay on their frozen binding until
`kmx run FILE --update`; a binding-only change replaces sessions too. Definition
edits require another suggestion/review. Changing the gh executable refuses until
reviewed again. `inspect` shows the binding digest and approved tool names.

In chat, `/suggest-tools OWNER/REPO` runs the same isolated planner;
`/review-tools PROPOSAL_ID` displays the scope and CLI acceptance command. Apply
in the terminal, then `/update` to activate. Suggestion results never auto-approve.

### Exact GitHub execution boundary

- `github_pr_list` accepts an empty object and lists up to 20 open PRs.
- `github_pr_read` accepts only a positive integer `number`, then reads PR metadata,
  up to 20 reviews and up to 20 check runs. Truncation/limits are reported.
- The executor constructs fixed HTTPS GET routes beneath the approved repository
  at `api.github.com`. Redirects refuse; returned URLs never become request targets.
  It projects metadata/review state/check conclusions, excluding bodies and logs.
- The pinned gh executable supplies `gh auth token --hostname github.com` through
  bounded capture into memory; no token is stored in proposals, grants, source,
  argv or output. Host/repo/debug/pager and ambient token overrides are removed
  for this subprocess. Sign in explicitly with `gh auth login` beforehand.
- The installed gh credential can have broader access. This is application-enforced
  repository scope, not a token permission reduction. Use a separately constrained
  credential when credential-level repository isolation is required.
- Requests have a 15-second budget and 64 KiB response/output limits; each model
  turn has at most eight tool invocations. No automatic inference/tool retry.
  No generic shell, MCP, file-read, write or Copilot built-in tools are exposed.

Mock Test never uses live grants as a fallback. Every approved tool needs a fixture
in each case; results bind the grant digest and stay labeled mocked-tools. The
checked-in PR test makes a real model call to a mock `github_pr_read` handler.
The live path was separately exercised against PR #654: inferred list/read tools,
approved only read, and returned observed checks with the 20-of-22 limit disclosed.
Neither mock nor live local results authorize AgentSuite Deploy; the mapper refuses
agents with local grants until native cluster tool bindings exist.

This branch's Copilot-native front-matter format is an explicit additional spike
alongside the canonical Squad charter preview on main. Canonical charters starting
with `# Name` retain upstream Run/Test/MCP behavior. Use `--type md` to select the
persistent Copilot-native instance path explicitly. The local native-header parser
does not replace the canonical charter parser or the direct model-access credential
flow. Root `test` is the canonical native/workspace evaluation spelling; `eval`
remains a hidden compatibility alias.

Build this branch and run the local browser:

```sh
go build -o bin/kmx ./cmd/kmx
./bin/kmx console
```

Bare `kmx` displays help. On first interactive agent execution, KMX detects the installed Copilot CLI and asks
`Use it by default for local agents? [Y/n]`. Enter accepts; No is remembered.
The per-user choice lives in `preferences.json` under `config.StateDir()`:
`$KMX_HOME`, or normally `~/.config/kmx` on Linux and
`~/Library/Application Support/kmx` on macOS. Credentials remain managed by Copilot.
Missing authentication directs you to `copilot login`. Help/version/completion
and redirected bare-root invocations never prompt. An empty browser offers to
create a hello starter using `kmx create NAME --type md`.
The shared agent console displays `md` file-backed agents and Orka agents by type;
Markdown is not a separate command namespace. This follows a resource-oriented
workflow: list, inspect, create, run and chat with agents, selecting their type
where needed. Source files and AgentSuite artifacts remain distinct backing forms.

```sh
./bin/kmx settings --provider copilot
./bin/kmx settings --provider none
./bin/kmx settings --provider reset
./bin/kmx list --type md
./bin/kmx list --all
./bin/kmx status
./bin/kmx info
./bin/kmx list --definitions --type md
./bin/kmx console
./bin/kmx console --definitions
./bin/kmx validate hello
./bin/kmx inspect hello --type md
./bin/kmx chat hello --type md
./bin/kmx chat hello --new --label research
./bin/kmx chat hello --type md --session s-SESSION_ID
./bin/kmx run hello --type md --prompt 'My name is Sam'
./bin/kmx run .github/agents/hello.agent.md
./bin/kmx chat hello --type md
./bin/kmx stop hello
./bin/kmx run -i .github/agents/hello.agent.md
./bin/kmx test hello --show-content
./bin/kmx test hello --case greet-sam
./bin/kmx test weather --provider copilot --show-content
```

`--provider copilot` overrides the saved route for one invocation. `--model ID`
overrides the file's string model; an omitted model inherits Copilot defaults.
Default/auto is explicitly unpinned. Observed model IDs are retained in test rows.
`settings` prompts in a terminal and prints preferences when redirected. The
preference file stays 0600 even when an existing owned config directory is 0755;
save failure is an error, never a silently forgotten choice. Explicit `.agent.md`
paths select type md; registered local instance names/IDs also resolve locally
before Orka discovery. Thus `kmx chat frogagent` attaches directly when frogagent
is running in the background. Explicit `--type orka`, runtime or cluster selectors
take precedence. Ambiguous local names require a full instance ID. `list` shows tracked local instances;
`--definitions` explicitly discovers source files. `inspect` dispatches by type; full native
AgentSuite evaluation remains `eval`. `test` and `validate` currently support md.

### Agent and test files

Start with `kmx create frogagent --type md` to generate a definition and its tests,
then edit the instruction body below the YAML header. For an existing plain-text
file, `kmx validate frogagent.agent.md` prints a copyable header when one is missing:

Interactive Run/Chat offers a single **Import agent** confirmation for missing
front matter. `kmx import frogagent.agent.md` starts that flow explicitly. Name and
description default from the filename, model inherits provider defaults and tools
are none. Press Enter to save **a new file**, `frogagent.imported.agent.md`.
Choose `e` to edit metadata/output path, `r` to replace the original with additional
confirmation, or `n` to cancel. Existing files are not overwritten by default.
The instruction bytes are preserved.
Run/Chat continues with the saved path. Cancelling or declining changes nothing;
scripts get the import command instead of prompts. Validate remains read-only.

After saving an imported definition, interactive import/Run/Chat runs one inference
pass for up to three scoped CLI tool suggestions. There is no GitHub-first question.
Each complete proposal shows its command, scope, operand constraints and execution
limits, then asks **allow/deny** (default deny). Only allowed declarations are added
after the complete review; cancelling midway adds nothing. Missing scope is shown
as a clarification request and cannot be allowed until supplied in the definition.
Import stays saved if suggestion fails or returns no tools. Accepting declarations
grants no execution permissions: review PATH resolution and probes separately with
`tools approve FILE --apply`. Existing tools are retained rather than auto-merged.

```markdown
---
name: frogagent
description: Answers questions as a frog
tools: []
---

You are literally a frog. Answer as if you are one.
```

Name defaults to the filename; description and explicit tools are required by the
local runner. Omit model to use Copilot's default. `tools: []` disables tools.
Validation never edits the original file. Run it with `kmx run -i frogagent.agent.md`.

- [hello definition](../.github/agents/hello.agent.md) pairs with
  [hello tests](../.github/agent-tests/hello.agent.test.md).
- [weather definition](../.github/agents/weather.agent.md) pairs with
  [weather tests](../.github/agent-tests/weather.agent.test.md), demonstrating a
  real model tool call handled by a deterministic mock.

Definitions use native Copilot front matter: optional name/model strings,
required description, and explicit tools (`[]` for tool-free execution).
Other Copilot features produce a located unsupported-field diagnostic rather
than being silently ignored. The old nested model object and inline test format
are retired: move tests to the companion and use a string model or omit it.

Pairing is by filename stem in `.github/agents/` and `.github/agent-tests/`.
For an agent elsewhere, supply `--tests PATH`. Test documents have
`schemaVersion: 1` front matter, prose/headings and `yaml agent-test` fences.
Case blocks use existing `id`, `input`, `assertions`/`expectContains`, plus optional
`mocks.tools`. Supported checks: contains, notContains, regex, toolCalled and
toolNotCalled. Each case has a fresh conversation and fresh mock counters.
Tests and expected answers are never included in agent instructions.

Every file entry point validates offline before provider discovery. Diagnostics
include path, line and column; `validate --json` emits structured diagnostics.
Reads require bounded regular files (definition 64 KiB, tests 128 KiB), UTF-8,
valid front matter, unique IDs, valid regex and local mock parameter schemas.
Definitions have a 30,000-character instruction ceiling; suites have at most 32
cases. Invalid paired tests block execution; missing tests permit tool-free chat
but Test refuses an empty suite. Discovery includes `.github/agents`, skips
other hidden directories/dependencies/symlinks, and is bounded to 10,000 entries
and 100 agents. Native Windows remains unsupported by this checkout.

### Editing TUI

`kmx console` opens the shared agent console; bare `kmx` displays help. The local
instance environment is selected initially; `console --definitions` explicitly
shows files that have never run. `/env local CONTEXT` selects a local cluster and
includes tracked md rows beside Orka rows. Remote columns remain cluster-only.
Use `--type md` or `--type orka` to filter console inventory, and `--directory DIR`
to choose local source. Arrow keys select an agent; `i` inspects, `c` chats, and
Enter opens type-specific actions. MD actions include edit definition, edit tests
and run tests. `chat hello --type md` opens the same editing chat directly.
Use your editor normally, or:

| Command | Action |
|---|---|
| `/edit` | Open the definition in VISUAL/EDITOR (default vi) |
| `/edit-tests` | Open the companion test file |
| `/reload` | Adopt saved instructions and start a fresh conversation |
| `/update` | Review a replacement of the running version; `/update yes` applies and opens a fresh chat |
| `/test [id]` | Test the latest saved pair; ID selection is partial |
| `/tests` | Show case results, source locations, expected checks and actual answers |
| `/show` | Show the loaded instructions |
| `/diff` | Compare saved files with the last in-memory tested snapshot |
| `/reset` | Create/select a new session without restarting the agent; older transcript stays visibly historical |
| `/auto-test on` / `/auto-test off` | Opt into debounced full tests on save; default off |
| `/settings copilot` / `none` / `reset` | Change the saved provider preference |
| `/cancel` | Cancel the active job; Ctrl-C also cancels, or exits when idle |
| `/quit` / `/back` | Return to the browser (or end explicit chat) |

For a GUI editor, set `VISUAL='code --wait'`. Quoted editor executable/arguments
are parsed without a shell. Bubble Tea releases/restores terminal ownership for
the editor. The active pair is reread every 500 ms; save-triggered testing waits
for a stable interval and never overlaps another job. Invalid saves show INVALID.
Instruction changes require reload before chat sends; test-only changes retain
the chat conversation. Earlier messages stay visibly separate from the reloaded
revision. Wide terminals show a test panel beside chat; `/tests` toggles detailed
expected/actual output (and switches views on narrow terminals). `list --type md`
is suitable for redirected output; explicit redirected `chat FILE --type md`
supports the simpler line-oriented controls.

Edit Hello to Goodbye in the hello definition, save and run `/test`: the greeting
checks fail. Restore Hello and retest. Git stores source history; exact SHA-256
hashes of both files bind test attempts. File changes during a run mark the
captured result stale, and partial tests never become a full-suite pass.

### Test history and execution

### Versions and instances

Remove a stopped local instance with `kmx rm ID_OR_NAME`. Use `kmx rm --force ID`
to stop a running background instance first. Removal hides the instance from all
inventory/lookup; source Markdown, paired tests, cached versions and test history
are preserved. Private lock/generation bookkeeping remains to prevent stale
clients from acquiring authority over a later run of the same source. This is not
a cluster-resource or cache-pruning command. `-o json|yaml` reports removal.

ID/name completion is available for run, test, chat, inspect, stop and rm (and run's
--agent flag). Stop/chat suggest running instances; rm/inspect/run/test include stopped
ones. Ambiguous names are omitted in favor of unique IDs. Local completion reads
records/leases only, never Copilot or source content. Explicit Orka selection keeps
the existing live-agent completion.

Run/Test accept the completed short ID and resolve it to the tracked source path.
Test evaluates the current saved working copy (not the cached running version).
If no instance matches a partial file path, Run/Test allow shell file completion.

For the current zsh session, enable completion for the checkout binary:

```sh
autoload -Uz compinit && compinit
source <(./bin/kmx completion zsh)
compdef _kmx ./bin/kmx
# Then type: ./bin/kmx stop 1f<Tab>
```

Installed `kmx` uses the same generated completion. Re-source it after command-tree
changes; other shells can use `kmx completion bash` or `kmx completion fish`.

Inventory includes **AGE**, elapsed wall time since the instance's latest start
(not cumulative uptime). `inspect` shows the exact UTC start and age; JSON/YAML
retain `started`. Reusing an already-running version preserves this timestamp;
restart updates it; new/reset conversations do not. Generation remains an internal
stale-request guard and is still available in structured inspection.

Use `-o yaml` wherever a JSON report is supported. Local list/status, inspect,
definition listing, info, validate, settings, create, stop, background run,
one-shot run and Test provide structured output. For example:

```sh
kmx list --all -o yaml
kmx inspect frogagent -o yaml
kmx test .github/agents/hello.agent.md -o yaml
kmx validate .github/agents/hello.agent.md -o yaml
```

Test reports omit answers by default; `--show-content` explicitly includes them.
YAML renders the same field contract as JSON, preserving timestamps and numeric
precision. Interactive chat, `run -i`, and import do not emit report documents and
refuse structured-output selection before execution. Runtime-specific commands
without JSON reporting (such as `orka status`) keep their existing text interface.

`list` is the canonical instance inventory: running by default, `--all`/`-a`
includes stopped instances. Local `status` is a compatibility alias with identical
filtering/output; `status --to NAME` retains deployment inspection. `inspect ID`
shows full instance identity, version, session, source, generation, timestamps and
cached instructions; it works even when the source is deleted. `-o json` retains
full IDs and digests. Use `info` for setup and `list --definitions` for files.

The ID column displays 12 hexadecimal characters (extended when necessary to
disambiguate). Chat, stop, run and inspect accept a full ID (with or without `md-`),
a unique prefix of at least six hex characters, or a unique name. Exact IDs win
over prefixes, which win over names. Literal file paths select that source.
Ambiguity lists matching IDs/states/paths; it never picks the first match.
IDs remain source-path-derived in this iteration, preserving existing records;
moving a file can create another instance. Version hashes identify content and
are not interchangeable with instance IDs. The TUI's inspect view shows full IDs.

`kmx run FILE.md` validates/caches the definition and starts a background local
worker, then prints exact Chat and Stop commands. `kmx run -i FILE.md` opens a
foreground chat instead. Explicit filenames may end in `.md`; discovery recognizes
`.agent.md`. With `--prompt`/`--prompt-file`, Run answers once and stops.
`chat NAME_OR_ID --type md` attaches to an existing background instance using its
cached definition, even if source has changed or disappeared. Exiting attached
chat disconnects without stopping the worker; use `kmx stop NAME_OR_ID` to stop it.
Only one inference turn is allowed at a time in a shared background conversation.

The instance now owns zero or more independent conversations. `chat NAME`
creates/resumes the **default** conversation; `--new` creates another, optionally
named with `--label research`. `--session s-ID` resumes an exact session belonging
to that agent. `/reset` creates a new conversation for this client, leaving the old
one available and preserving the agent's version, generation and other sessions.

`list`/local `status` retain one row per agent with **SESSIONS**, the open session
count. `inspect ID` expands ID, label, state, activity and pinned version per
session. KMX session IDs exist when opened; Copilot's session is created on the
first message, with its ID available in inspect JSON. Activity is responding when
any session is handling a turn; different sessions execute independently. Errors
require a new conversation instead of silently resetting the old session identity.

Stop closes all sessions and retains their closed metadata in the stopped record.
KMX does not retain transcripts: reconnect restores model context but does not
replay previous messages in the terminal. Restart begins with zero conversations.
At most 64 sessions are allowed per running instance; stop/restart releases them.
Older managers require stop/rerun for this protocol; unavailable counts show unknown.

Exact definition bytes are stored without replacement under
`KMX_HOME/agents/versions/<sha256-hex>/agent.md`. Identical content reuses the entry;
execution reads its verified bytes. Model overrides are recorded separately.
`KMX_HOME/agents/instances/<source-path-id>/instance.json` retains selected/previous
version, generation, state, model route and timestamps. Re-running the same path
increments its generation; edits select a new version and preserve old snapshots.
The source path, not display name, identifies the instance. An OS-held lease
prevents another process from overwriting an active session. Re-running unchanged
background content returns its existing instance. Plain Run refuses to replace
changed content; use `run FILE --update` to apply an explicit replacement.
Foreground `/reload` ends its conversation and the next message starts
the saved version; attached chat retains its worker's frozen version.

### Edit → test → update → chat

```sh
kmx test .github/agents/hello.agent.md --show-content
kmx run .github/agents/hello.agent.md --update -i
```

Update validates and caches the saved definition before affecting the running
agent, shows old/new version and model, open conversations that will close, and
the latest matching local test status. Full passing tests are informative, not
mandatory for local experimentation. Confirm once to close all previous sessions,
replace the version under the same instance ID, advance the internal generation
and UTC start time, and start a fresh chat with `-i`. Without `-i`, it remains a
background agent and prints the chat command. Scripts must supply `--yes`.

Unchanged definition/model is a no-op: no confirmation, restart, session loss or
timestamp change. Source edits after review and stale instance generations refuse.
Old cached versions remain. Update requires an existing running instance on the
current manager; initial/stopped agents use plain Run. Other agents are unaffected.
Existing attached clients cannot send into the replacement generation. In the
TUI, `/update` shows the plan, `/update yes` applies, and `/update cancel` discards
the plan. `/reset` still creates a conversation on the same running version.
The worker uses a private Unix socket and inherited process lease. Startup returns
only after Copilot initialization and socket readiness. It does not expose an HTTP
listener, auto-restart, or survive machine shutdown. Status still detects crashes.
Cancelling an attached request disconnects that request; its model turn may finish
in the worker. Stop cancels the worker and its active turn.

Running agents now share **one local manager and one Copilot CLI process per
KMX_HOME**. Each agent has its own Copilot session, conversation, turn lock,
version and instance lease. Stop closes only that agent's session; other agents
continue running. After the last agent stops, the manager releases the shared
provider and exits after 30 seconds idle. Foreground and one-shot agent runs use
the manager too; isolated test invocations retain their own temporary provider.
Status includes an Inference column (`copilot / default` or the selected model).
Older dedicated workers are not interrupted automatically: stop and rerun them to
move to the shared manager. Conversations are not migrated between processes.

The chat header has separate Status, Def, Tests and Tools labels. Input and prior
user messages share a solid blue left rail. Foreground inference reports safe
runtime progress and elapsed wait; the limit is five minutes per turn. Tools are
explicitly none for ordinary md agents, or fixture-only for tests: no built-in
web search or interactive tool-approval UI is enabled.

`status` reads only these records and checks the owner lease. Abrupt process exit
is reported stopped rather than trusting a stale running flag or recycled PID.
`info` reports setup; `list/console --definitions` show available source files.
Test-only runs do not register agent instances. Version cache eviction is not
automatic; historical source snapshots remain available locally.

### Test metadata

Last-10 metadata records are stored privately under
`KMX_HOME/md-tests/<agent-path-hash>/history.json`. Records contain definition/test
hashes, adapter/runtime/model selection, mode, coverage, safe outcomes and times.
Raw answers stay in memory for `/tests` or print only with `--show-content`.
After restart, previous source bytes and answers are unavailable; Git is the
source-history tool. A passing badge includes its timestamp: source equality is
not a guarantee that an unpinned hosted model will behave identically later.

The pinned Copilot Go SDK v1.0.24 starts the installed CLI, selects the supported
native custom agent explicitly and maintains real conversation state. Sessions
run in an isolated temporary directory with config/skill/hook discovery disabled.
Test callbacks return fixtures only after an actual tool call. Wrong arguments,
missing calls or extra calls fail; mocks never fall through to real tools. Ordinary
chat/run accepts tool-free definitions; the weather agent requires a separately
configured real provider when used directly in Copilot outside mock tests.

Calls have a five-minute deadline and no KMX automatic resend. Answers are bounded
to 64 KiB. Copilot owns its session/log retention. Exit 0 means all selected cases
passed; exit 1 means failed/incomplete execution or invalid input. This is local
preview, not native publication or deployment evidence. Full implementation
decisions and remaining extensions are in the [inner-loop design](markdown-agent-inner-loop.md).

## Graduate an md agent to AgentSuite and remote Orka

### One deployment workflow

Use the existing Deploy command directly; its hidden `lift` compatibility alias
accepts the same arguments:

```sh
kmx deploy .github/agents/hello.agent.md --to prod --model qwen2.5:3b --plan
kmx deploy .github/agents/hello.agent.md --to prod --model qwen2.5:3b
# Compatibility spelling:
kmx lift .github/agents/hello.agent.md --to prod --model qwen2.5:3b
```

KMX freezes the working-copy definition and tests, converts them to temporary native
Orka source, then delegates to the existing full evaluation → evaluated publication
→ deployment pipeline. No manual conversion step is required. The temporary source
is removed afterward; the package/evidence/lifecycle handlers retain their existing
rules. Each invocation maps afresh, so it does not rely on reusable workspace-path
completion shortcuts. Copilot results never substitute for native passing evidence.
`--plan` may create temporary local mapping/preparation data, but makes no cluster
writes, model calls or publication. It cannot establish actual runtime readiness.

Provide operational configuration in `kmx.yaml` in the current directory, or select
its directory with `--directory DIR`; no workspace marker is needed for this file
route. For example (replace the repository, contexts and Provider names):

```yaml
version: 1
publishRepository: ghcr.io/YOUR_ORG/hello
targets:
  eval:
    context: kind-kmx-local
    namespace: orka-system
    runtime: orka
    inference: provider:local
  prod:
    context: REMOTE_CONTEXT
    namespace: orka-system
    runtime: orka
    inference: provider:remote
    evaluationTarget: eval
```

Both targets need the supported Orka installation and a Ready inference Provider
serving the exact `--model`. Registry access/authentication must already work.
The existing reviewed execution plan governs effects and promotion. Source-side
tests are not destination evaluation. Tool-enabled agents/mocks refuse. Deployment
does not change the local running instance. This path has offline-plan and mapping
qualification and a one-off live kind-to-AKS demonstration described below.

### Inspect the generated workspace or run stages manually

The explicit offline mapper converts the saved definition and complete paired
text tests into a one-member native Orka workspace:

```sh
kmx suite map .github/agents/hello.agent.md \
  --workspace ./hello-suite --model qwen2.5:3b
```

Use `--tests FILE` for nonconventional companions and `--name ID` if the md name
is not a valid native member name. `-o json|yaml` reports source/test/pair hashes,
destination, model and case count. This snapshots the working copy, not a running
instance's older cached version. The mapper writes instructions without front
matter, a pinned build profile, member/workspace descriptors and ordered
`eval/001-ID.yaml` cases. Existing workspace/member paths refuse replacement.
The native packager embeds those cases as public AgentSuite evaluation documents.

**An exact deployment model is required**, even if the local definition uses
Copilot auto/default. The target must serve that model through a Ready Orka openai
Provider. Copilot CLI authentication does not become cluster inference credentials.
Changing runtime/model can change behavior; native tests must pass independently.
Tools, mocks and tool assertions refuse rather than being silently stripped.
Mapping produces no passing receipt and does not install, publish or deploy.

After preparing an evaluation cluster with the existing Docker/kind path
(`kmx local up`) and a destination cluster with the supported Orka runtime/model
Provider, use the existing lifecycle. Replace the context, Provider, repository
and digest placeholders with your actual values:

```sh
# Evaluate the complete mapped inventory; receipt path must be new and private,
# outside the workspace. Native evaluation uses the canonical test command.
kmx test --workspace ./hello-suite \
  --to-context kind-kmx-local --inference provider:local \
  --out ./hello-passing.json

# Publish exactly the evaluated suite. Registry authentication is configured separately.
kmx push --workspace ./hello-suite --evaluation ./hello-passing.json \
  ghcr.io/YOUR_ORG/hello:v1

# Use the digest returned by push, not the mutable v1 tag.
kmx deploy ghcr.io/YOUR_ORG/hello@sha256:DIGEST \
  --to-context REMOTE_CONTEXT --to-namespace orka-system \
  --inference provider:REMOTE_PROVIDER --evidence ./hello-passing.json \
  --promote --plan

kmx deploy ghcr.io/YOUR_ORG/hello@sha256:DIGEST \
  --to-context REMOTE_CONTEXT --to-namespace orka-system \
  --inference provider:REMOTE_PROVIDER --evidence ./hello-passing.json --promote

# Use the Agent identity reported by Deploy; this creates a fresh remote Task.
kmx run --type orka --agent DEPLOYED_AGENT --context REMOTE_CONTEXT \
  --namespace orka-system --prompt 'My name is Sam'
```

`--promote` explicitly consumes source-environment evidence; it does not claim
the destination was evaluated. Deploy validates exact artifact/runtime/model and
destination compatibility; installing a different model or Orka version is not a
substitute. See [digest deploy](suite-workspace.md#digest-lift) for target prerequisites,
ownership rules and evidence limits. Native tool-free suites need no per-agent
Docker image build.

### Live qualification (2026-10-22)

A one-off run against existing kind and AKS environments demonstrated automatic
Markdown mapping, two passing native greeting cases, candidate cleanup, exact
AgentSuite publication to ACR, remote Provider/Agent creation and a separate
successful remote answer (`Hello, Sam!`). Both environments used Orka v0.2.0 and
the declared model `qwen2.5:3b`. The successful remote Task took 2m19s from start
to completion, including runtime/model startup. This is not a clean-machine
timing study or continuous CI qualification.

Setup exposed three issues: Docker Desktop was stopped and saved kind endpoints
were unavailable; a saved AKS context was stale; and private proof storage refused
a group-writable ancestor directory. Explicit private kubeconfig/state directories
and a normal restrictive umask resolved the test setup. Registry credentials were
kept in an isolated private Docker configuration, never committed.

The deployment itself reached Ready, but the first remote answer failed because
the existing Ollama pod had an empty model inventory despite a Ready Provider
declaring the expected model. Pulling the exact model (about 1.9 GB) and issuing
a new Task produced the answer. The failed Task was not retried or rewritten.
This reinforces that destination readiness and source passing evidence are not
destination inference qualification. A clean-machine demo/write-up and the
day-0 Publish recommendation remain outstanding for #604.

## Existing host chat integration (Orka)

Host Copilot chat inference is independent from native workspace evaluation
and Deploy. `/inference-copilot` in existing Orka chat discovers models through
the authenticated installed CLI without rewriting the saved Provider. Native
eval/Deploy requires its packaged model and native binding, not host execution.
On a local kind target it opens a model picker; remote targets require a cluster
Provider. `/inference-local` returns to that Provider; `/retry` sends another turn.

## Direct model access

The independent unreleased `kmx model login`, `login --gh`, `logout` and
`list --provider copilot [-o table|json]` commands manage an explicitly owned
Copilot route, not this existing CLI-chat adapter. The
[dated access decision](copilot-access.md) distinguishes the selected experimental
compatibility route from official OAuth/SDK documentation and unqualified
hosted modes. See [sign-in and storage](models.md#explicit-copilot-model-access).
Runtime auth prefers explicit `COPILOT_GITHUB_TOKEN`, then an existing github.com
gh sign-in, then the owned OS keychain item if gh is unavailable. A working gh
sign-in requires no kmx login or keychain setup; a service refusal never switches
credentials. Runtime tokens are never copied to
storage, exported in argv/environment, or returned in routes. There are no token
flags or plaintext-cache fallback.

Markdown-file Run/Test and their shared MCP path consume this transport with
explicit `--provider copilot`, an exact authored model and the pinned endpoint.
They remain temporary tool-free, no-journal preview execution, not the CLI-chat
tool adapter below or an embedded Sessions harness. Selecting this route sends
instructions and inputs to Copilot; judging also sends answers and rubrics.
Known Copilot refusals stop later sends and expose only static safe reasons.
See [file preview usage](kmx.md#agent-file-local-preview-unreleased-integration).

Internal callers use `copilot.New(store)` (`nil` selects the native keychain),
then `Client(ctx, route)` to obtain an `http.RoundTripper` for a validated exact
`modelaccess.Route`. Use a standard request with POST
`https://api.githubcopilot.com/chat/completions`, a JSON body naming that same
model, and a caller-owned context/deadline. The transport preserves the caller's
Chat `Accept` header and successful JSON/SSE bytes for Chat routes. For enabled
Responses-only models it translates the owned text request/result privately,
including JSON and streaming completion/refusal/tool outcomes. Models offering
both endpoints retain Chat. Actual model identity is not replaced by the requested
name, and incomplete or failed output is not a successful completion. Callers own
closing the response body. Non-2xx responses return a classified error, not a raw
upstream response or error body. Request bodies are
bounded to 4 MiB, auth/catalog/error responses to 1 MiB, and credential/process
output to 16 KiB. Ambiguous top-level JSON keys and changed models or destinations
refuse before inference. Responses uses the existing direct caller's 1 MiB JSON
response budget and the same completion-token budget; streaming does not add a
line-size limit or a background reader. No agent loop, tool execution, listener
or OpenAI proxy is added.

Auth is established through GET `/models` before inference. The original GitHub
credential is tried directly as a bearer. Only 401 credential refusal or a 403
with explicit `unsupported_token` code authorizes the legacy GitHub
`copilot_internal/v2/token` exchange. Known subscription/policy/quota codes do
not authorize exchange, even with 401. Exchanged bearers remain in memory,
honor `expires_at` and refresh 30 seconds before expiry; the reusable keychain
credential is not replaced. Subscription, policy and quota refusals never select
another model or credential source. Catalog checks can be repeated; inference
is never automatically resent, including after an inference credential refusal or
connection failure. The next explicit request resolves auth independently.
Logout clears memory and deletes only kmx's item, not upstream account tokens;
it cannot retract requests already in flight or disable gh/environment credentials.

Safe static error prefixes distinguish `copilot_auth_required`,
`copilot_auth_revoked`, `copilot_no_subscription`, `copilot_policy_denied`,
`copilot_quota`, `copilot_model_unavailable`, `copilot_cancelled`,
`copilot_timeout`, `copilot_malformed`, `copilot_keychain_unavailable` and
`copilot_unavailable`. Unknown 403s are policy refusals. Raw upstream errors,
bodies, URLs and credential-bearing causes are not included in diagnostics.
Caller-owned successful model content is not redacted by this transport.

Auth/header reference: [vekil authenticator](https://github.com/sozercan/vekil/blob/3e77a9d0739baf7740b141bd88b3fdf40caf7c16/auth/authenticator.go)
and [request headers](https://github.com/sozercan/vekil/blob/3e77a9d0739baf7740b141bd88b3fdf40caf7c16/proxy/handler.go).
KMX does not reuse its disk cache, proxy, retry or failover behavior. Hermetic
native package tests are not live GitHub/subscription or OS-keychain acceptance
proof, nor full Windows CLI qualification. Explicit credential access may show
OS unlock UI; Linux uses the existing login/default collection with kmx-specific service
and account attributes. Native keychain calls/prompts have no context-cancellation
guarantee. Unexpected native-dependency panics during credential access are
mapped to the safe keychain error without exposing their value.

## Tool translation

The existing Orka host-chat adapter supplies enabled HTTP Tool descriptions to
`copilot -p --model <selected>`, validates returned JSON actions, executes scoped
HTTP calls and feeds results into the next prompt. It permits at most eight steps
and five minutes per turn. Supported tools are POST HTTP tools targeting a Service
in the Agent namespace, without special auth headers, Secrets or outbound policy.
Calls use an owned loopback forward and the selected Kubernetes context.

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
instructions and no inherited conversation. This adapter's credentials remain
managed by the installed CLI, including GitHub CLI login. It does not read
credential files or consume the independent direct model-access keychain item.
