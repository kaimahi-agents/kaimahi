# Copilot agent-builder plugin

**Development package, not an installable or signed release.** This Copilot
app/CLI plugin source contains a skill and declarative MCP config, with results
in chat. No editor extension, UI, downloader or second runner.
The source directory deliberately has no `bin/kmx`; do not install it directly.

## Package contract

The package uses [Agent Plugins 1.0](https://agent-plugins.org/plugin-authors/build-an-agent-plugin.md):
root [plugin.json](plugin.json), root [mcp.json](mcp.json) and
[skills/kmx-agent-builder/SKILL.md](skills/kmx-agent-builder/SKILL.md).
The [MCP specification for plugins](https://agent-plugins.org/plugin-authors/mcp-servers.md)
resolves `./bin/kmx` against the plugin root; Windows templates use
`./bin/kmx.exe`. It is one executable token, not a shell command or PATH lookup.
Default cwd is the plugin root, **not the user's repository**.

The offline [package.py](package.py) helper is **developer tooling** requiring
Python 3.9+. Generated packages contain a native executable and data only;
they do not require Python, Node, Go or shell bootstrap scripts at runtime.
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
`test`. All require `schemaVersion: 1` and root-relative `path`; Create
also needs canonical `content`, Edit needs `content` and `expectedRevision`.
Run/Test accept `caseId` and `showContent`; Run also accepts `input`.
Use discovered schemas as authority. See the
[real MCP contract](../../docs/kmx.md#local-stdio-mcp).
No fifth bind/Explain/model/install tool is introduced.

The default package starts without provider overrides and supports the
deterministic fake orientation in the skill. Arbitrary tasks need an explicitly
configured supported route and exact Model.Preferred; no silent fallback.
The temporary direct preview runner has no Sessions journal.
Real-model route setup is not automated by this packager.

A public archive cannot contain a user's absolute root. End-user root setup,
compatible Copilot versions, skill resource lookup and actual install/cache
behavior remain release integration gates. Users are not told to run this
Python builder as a newcomer installation step.

## Results and verification

Run/Test return `schemaVersion: 1`, `kind: kmx.agent-file-preview`,
file revision, selected model, full/subset and pass/fail/unknown results.
A passing subset still has `fullPass: false`. Complete preview data is in
`structuredContent`; text is a compact human summary, not JSON. Completed
assertion failures are normal tool results: `isError: false` does not mean pass.
Preview file.path is a display basename, **not the original Edit target**.
Boundary refusals have a safe reason code instead of a preview.
Default output omits answers/operands. Explicit `showContent` opts into bounded
excerpts. Retention remains `not_requested`; file-scoped Explain is deferred.
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

Release integration must generate signed platform archives from the exact
approved engine and verify the expected digest before execution. Then qualify:

```sh
copilot plugin install ./extracted-plugin
copilot plugin list
copilot plugin uninstall kmx-agent-builder
```

The [Copilot creating guide](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/plugins-creating)
documents local-directory installation and cached components: reinstall to
refresh local changes. Archive URLs are not direct install sources.
App marketplace installation needs separate evidence; CLI tests are not app
support. No host installation or release publication is performed here.
Real-model integration, native OS trust qualification and the newcomer pilot
remain separate acceptance activities.
