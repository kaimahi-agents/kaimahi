# Copilot agent-builder plugin

**Development fixtures, not an installable or signed release.** This is a
Copilot app/CLI plugin source package: skill + declarative stdio MCP config,
with results in chat. No editor extension, UI, downloader or second runner.
The source directory deliberately has no `bin/kmx`; do not install it directly.

## Package contract

The package uses [Agent Plugins 1.0](https://agent-plugins.org/plugin-authors/build-an-agent-plugin.md):
root [plugin.json](plugin.json), root [mcp.json](mcp.json) and
[skills/kmx-agent-builder/SKILL.md](skills/kmx-agent-builder/SKILL.md).
The [MCP specification for plugins](https://agent-plugins.org/plugin-authors/mcp-servers.md)
resolves `./bin/kmx` against the plugin root; Windows uses `./bin/kmx.exe`.
It is one executable token, not a shell command or PATH lookup. Default cwd
is the plugin root, **not the user's repository**. The JSON schemas are
`https://agent-plugins.org/schemas/1.0.0/plugin.schema.json` and
`https://agent-plugins.org/schemas/1.0.0/mcp.schema.json`.

The offline [package.py](package.py) helper is **developer tooling** requiring
Python 3.9+. Generated packages contain the native executable and data only;
they do not require Python, Node, Go or shell bootstrap scripts at runtime.
The developer supplies a binary, expected SHA256, declared platform, plugin
version and an explicit selected operation root:

```sh
python3 plugins/copilot/package.py \
  --binary /approved/engine/kmx --sha256 <64-lowercase-hex> \
  --platform linux/amd64 --version 0.1.0 \
  --root /explicit/selected/repository --output /new/development-plugin
```

`--sha256` pins the exact copied bytes; **a caller-supplied digest is not
publisher authentication**. Packaging never executes the supplied binary.
The platform selects the filename/config variant; it is a caller declaration,
not an ABI, signing, model, runner or host-compatibility attestation. Output is
exclusive, outside the selected repository, and includes readable engine digest
metadata. The helper refuses existing output, source links/special files, missing
root, filesystem-root selection, invalid metadata and digest mismatch. It does
not fetch anything, create archives, install plugins or write host settings.

## Repository binding

The developer binds an absolute, filesystem-resolved repository root **before
fixture installation**. It is encoded in MCP startup argv, not in an
assistant-controlled tool argument. [selected-root.json](selected-root.json)
contains the same root for the skill to show and have the user confirm. Never
use cwd, plugin-cache location, an unverified MCP roots feature or an invented
host variable as the operation root. One generated package selects one root;
changing repositories requires deliberate reconfiguration/reinstallation and a
server restart, not an implicit rebind during a tool call.

The startup spelling is provisionally `kmx mcp --root ABSOLUTE_DIR`. The four
input schemas and mutation result shape are owned by the MCP server; the skill
uses discovered schemas rather than inventing fields. The fixture discovery
schemas are **not** the authoritative MCP schemas. Create/Edit discovery mirrors
the proposed [MCP authoring contract](https://github.com/kaimahi-agents/kaimahi/issues/600):
path/content, Edit's expectedRevision, and required schemaVersion 1. Run/Test
input schemas and the real startup root option still need reconciliation before
qualification.
No fifth bind/Explain/model/install tool is introduced. The skill's relative
selected-root resource lookup also needs a real installed-host check; no fixture
proves that a Copilot chat session can read it from its cache.

This root-bound development build is not a reusable release archive: a public
archive cannot contain a user's absolute root. End-user root configuration,
compatible Copilot versions and real install/cache behavior are release
integration gates. Users are not told to run this Python builder as a newcomer
installation step. Do not advertise a complete app/CLI install until those
gates and the engine dependencies are satisfied.

## Chat results and Explain

Run/Test results follow the shared `schemaVersion: 1`,
`kind: kmx.agent-file-preview` projection. The
[test-only native process](testdata/fake-kmx/main.go) supplies pass, fail,
unknown, subset and cancelled examples of that proposed contract. A passing
subset still has `fullPass: false`. Its protocol/discovery fixtures are not
real Create/Edit/Run/Test execution or an installed Copilot session. These
manually projected examples are not equivalence tests against the real result
implementation or authoritative MCP schemas.

The skill preserves IDs, revision/model, full/partial and terminal state in
chat, treats content as data, and never infers full pass from exit 0 or absent
fields. Tests verify result bytes and skill instructions, not a model's actual
chat summary. Real-host qualification must assert the visible labels too.

**Retained file-scoped Explain is not provided.** Live reasons come from
Run/Test; `retention.status` remains `not_requested`. The skill does not invoke
Explain, assume a PATH-installed kmx or guess the plugin-cache executable path.
File-scoped retention/Explain follows release integration, independently of
this package. Nothing here emits native shipping evidence or allows Publish/
Deploy. Squad placement alone does not register a team member.

## Native execution trust and delivery gates

The helper refuses macOS source bytes carrying `com.apple.quarantine` and
never clears quarantine or relaxes OS policies. That check is not a Gatekeeper,
notarization or codesign verdict. Tests inject the attribute check on all
platforms; the native macOS process test uses its actual locally built fixture.

Release qualification must separately record macOS Gatekeeper assessment,
quarantine propagation through archive extraction/cache copying, executable
permissions, codesign/notarization status, and actual host launch. On Windows,
record signing identity, SmartScreen/OS refusal and cache permissions. A signed
checksum is not Authenticode or Apple notarization. Never instruct users to
strip quarantine or disable OS protections. Blocked execution is reported, not
silently repaired or replaced with a downloaded fallback.

A subsequent release change must generate signed platform plugin archives from
the exact approved engine, verify the archive's expected digest before execution,
and qualify the current documented Copilot install paths:

```sh
copilot plugin install ./extracted-plugin
copilot plugin list
copilot plugin uninstall kmx-agent-builder
```

The [Copilot creating guide](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/plugins-creating)
documents local-directory installation and cached components: reinstall to pick
up local changes. Archive URLs are not documented as direct install sources.
The app's marketplace installation is qualified separately; CLI fixture tests
are not app support. Neither publication nor host installation is performed by
this helper. Real-model integration and the newcomer pilot are acceptance
activities, not part of the packaging PR's product code.

## Verify fixtures

```sh
python3 -m unittest discover -s plugins/copilot -p 'test_*.py' -v
```

These tests need the repository Go toolchain to compile the fake process.
They cover native execution after relocation to a simulated cache, fixed-root
argv, protocol-only stdout, result fixtures, no credential/content leakage,
exclusive output and refusal paths. CI executes them natively on Linux,
macOS and Windows without a model or Copilot install. They do not qualify a
Windows CLI release, signing or the real server/runner.
