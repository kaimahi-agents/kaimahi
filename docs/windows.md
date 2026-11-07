# Native Windows

Native Windows support is **prerelease-only and operation-specific**. Current
`main` adds explicit-target remote Orka inspection and existing Task Result on
amd64 to the narrow day-zero CLI.
Published v0.9.0 release candidates beginning with `v0.9.0-rc.4` retain only the
day-zero commands until a later release contains this exception. This is not the
Kubernetes agent lifecycle on Windows. Stable v0.8.1, including the version
selected by `@latest`, has no Windows `.exe`.

Use [WSL](#use-wsl-for-broader-workflows) for every broader workflow. In
particular, the existing Windows Copilot integration remains the
[WSL Copilot plugin](../plugins/copilot-wsl/README.md), not a native plugin.

## Supported native commands

The native command boundary is closed: only the following operations are
supported by a checkout build and by the published v0.9.0 release-candidate
Windows assets described below.

| Operation | Native Windows contract |
|---|---|
| Identity and documentation | `kmx version`, root or command help, and existing `completion bash\|zsh\|fish` generation. Help and completion are inert; seeing another command in their output does not enable it. There is no PowerShell completion claim. |
| Package-free fake Run | `kmx run FILE ["input"]`, optionally with `--tests FILE`, legacy inline `--prompt TEXT` or `--no-content`, for a Markdown agent whose effective model is `fake`. Run loads the selected agent-and-tests pair: the default companion tests, or the `--tests FILE` override. KMX opens no workspace, package, cluster, provider, network or tool-binding route. |
| Package-free fake Test | `kmx test FILE [--tests FILE]` for the same offline fake route and the same selected agent-and-tests pair. A completed pass or failure is a local preview only. |
| Offline file diagnostics | `kmx test explain [failure-id] --file FILE [--tests FILE]`, including its file-scoped content and clear options. Explain compares retained failures with the current agent-and-tests selection. Hidden `kmx eval explain ... --file FILE` may remain callable only as the deprecated compatibility spelling. |
| Read-only remote Orka inspection (current main, amd64) | `kmx --context CONTEXT cluster orka list --namespace NAMESPACE` and `kmx --context CONTEXT cluster orka show NAME --namespace NAMESPACE [-o table\|json] [--tasks N]`. Both selectors are mandatory on native Windows. List is table-only on native Windows; these commands perform bounded reads only and create no KMX state. |
| Existing Task result (current main, amd64) | `kmx --context CONTEXT cluster task result TASK --namespace NAMESPACE [--wait DURATION] [--result-port PORT]`. It reads one exact AI Task without resending; pending exits 2 and opens no tunnel. Answer stdout is accepted only after the owned loopback forward exits cleanly. |

Flag-parsing errors label their help hint **PowerShell** on native Windows.
The hint uses PowerShell's `&` call operator and quotes the executable path,
including doubled apostrophes inside the quoted path. Run that hint in
PowerShell, not `cmd.exe`. Operational Task recovery also uses PowerShell syntax;
Unix recovery and other existing operational commands keep their POSIX quoting.

For example, after downloading a qualified amd64 artifact:

```powershell
.\kmx-windows-amd64.exe version
.\kmx-windows-amd64.exe --help
.\kmx-windows-amd64.exe completion bash
.\kmx-windows-amd64.exe run .\greeter.agent.md --prompt Ada
.\kmx-windows-amd64.exe test .\greeter.agent.md
.\kmx-windows-amd64.exe test explain --file .\greeter.agent.md
# Current-main amd64 build only; not present in the published rc.4-rc.6 contract:
.\kmx-windows-amd64.exe --context existing-orka cluster orka list --namespace orka-system
.\kmx-windows-amd64.exe --context existing-orka cluster orka show greeter --namespace orka-system
.\kmx-windows-amd64.exe --context existing-orka cluster task result task-name --namespace orka-system
```

The file must satisfy the [agent file contract](agent-file.md), including its
[companion tests file](agent-file.md#tests-in-a-separate-file-cli) when one is
selected, and the
supported native execution route is the deterministic `fake` model. Provider,
endpoint, tool-binding, prompt-file and `--github-summary` modes are outside this
boundary.
Run/Test create private local-preview state as described below, but they do not
create AgentSuite evidence, a passing suite receipt, publication authority or
deployment authority. Offline Explain reads or clears only the selected file's
retained local diagnostics. It does not contact a model or turn those diagnostics
into evidence.

### Remote Orka prerequisites

Put an operator-installed native `kubectl.exe` on `PATH`. KMX does not download,
install or update it on Windows. The selected kubeconfig and its exec-auth
programs are trusted exactly as they are by direct kubectl use. A kubeconfig that
uses `kubelogin -l azurecli` therefore requires an existing Azure CLI sign-in;
KMX never runs `az login`, opens a browser/device flow, converts credentials or
falls back to WSL. Missing, expired or refused credentials produce a bounded safe
error. KMX freezes the resolved kubectl path and file identity and refuses a
changed file between calls. This is a cooperative replacement check, not byte
attestation or a sandbox. KMX holds kubectl before user-mode execution until it
enters a kill-on-close Windows Job Object, so cancellation and abrupt KMX exit
reap ordinary descendants. An arbitrary exec plugin may still perform side effects or use
brokered process creation outside that cleanup boundary.

Task Result additionally requires the selected namespace's Orka API
Service and result-reader ServiceAccount. KMX requests one bounded in-memory
ServiceAccount token, opens an IPv4 loopback-only kubectl port-forward, pins one
connection and never reconnects it. Tokens are never placed in argv, files or
output. Task Result creates no Kubernetes resource and never resends work.

Human table output removes terminal control and bidirectional-formatting
characters and bounds cluster-controlled fields. Native Windows list is
table-only; Show JSON is a bounded KMX summary, not a raw resource. Show resolves
cross-namespace Provider references exactly, but withholds invalid or
credential-bearing Provider endpoints and status diagnostics from every output.
Show names the referenced Secret but reports presence as unknown rather than
fetching a Secret object or value. Kubeconfig exec plugins are local
operator-trusted code, not sandboxed extensions.

## Everything else is deferred

Every command or mode not listed above refuses before operational configuration,
subprocess, network, cluster or ordinary output effects. Native Windows does not
currently support:

- Docker, Podman, kind, local Kubernetes setup, AKS or any cluster mutation;
- workspace or OCI storage, AgentSuite build/load/test/push/pull, suite evidence,
  registry access, publication, Deploy or retirement;
- local or hosted model discovery, provider routes, credentials, model network
  calls, MCP tool bindings or any other network route;
- `kmx mcp`, a native Copilot plugin, AgentKit, Buildx or native Squad setup;
- managed tools, tool downloads, cluster records, remembered cluster preferences
  or context operations; or
- live Agent Run/chat, runtime setup/status, package export, publication or deployment.

Run and Chat stay closed even with explicit selectors: Orka's current Task
`agentRef` is name-only and has no create-time Agent UID/generation precondition,
so KMX cannot prevent a raced Agent replacement from receiving a new prompt.

This refusal boundary is operation-specific, not a claim that the underlying Go
packages have no Windows tests. Package tests for files, models or adapters do not
enable their commands. Deferred adapters remain unavailable even when help or
completion can describe them.

## Writable state

Without `KMX_HOME`, native Windows state defaults to
`%LOCALAPPDATA%\kmx`. `KMX_HOME`, when set, must be an absolute path to a
separate qualified state root.

Writable native state is qualified only on a local-disk **NTFS** volume that
reports persistent ACL support. KMX-created private objects are owned by the
current token user and use a protected DACL with only explicit ordinary allow
entries for that user. Existing state must already satisfy that contract. An
unsafe owner, inherited or broader DACL, unsupported ACE, unreadable security
descriptor or ambiguous object is refused and is **never repaired**, adopted or
permission-widened by KMX. Select a new empty qualified state root rather than
asking KMX to change an existing ACL.

The writable-state contract excludes all of the following:

- non-NTFS filesystems, including ReFS, FAT and exFAT, even if they expose some
  ACL behavior;
- UNC paths, network shares, mapped network drives, devices and device paths;
- `SUBST` drives and other remapped roots whose drive letter is not the physical
  local volume root;
- alternate data streams and names containing a stream separator;
- symlinks, junctions, mount points and other reparse-point components; and
- exposed placeholder, redirect and other reparse-point roots, including
  Files On-Demand entries when Windows exposes them that way.

Synchronized folders, including OneDrive, are outside the qualified writable
boundary even when their current NTFS representation is fully hydrated and not
exposed to KMX as a reparse point. KMX does not detect every registered sync root,
and opening a read-only Files On-Demand source may let Windows hydrate it. Keep
KMX state and qualification fixtures in an ordinary unsynchronized local folder.

The local NTFS checks cover the tested lock, flush, identity and private-ACL
protocol. They are not power-loss certification, filesystem recovery
certification, a backup, or a guarantee that a sync or backup product captured
the complete SQLite journal family. Back up important source separately and do
not treat preview state as durable release evidence.

### Keep native and WSL state separate

Native Windows and WSL/Linux writable KMX state must be **physically separate**.
Do not point Windows `KMX_HOME` at a directory that WSL reaches as `/mnt/c/...`,
and do not point WSL `KMX_HOME` at a Windows-drive mount. Native Windows locks and
Linux/WSL locks do not coordinate, even when both paths appear to name the same
files. Sharing can therefore defeat exclusion and make publication or cleanup
ambiguous.

WSL/Linux refuses recognized Windows-drive mounts for writable KMX state. Keep
the native default under `%LOCALAPPDATA%\kmx` and WSL state inside the distro's
Linux filesystem, or choose two other physically distinct qualified roots.

Read-only Windows source input is a separate contract. Native file Run/Test does
not edit the selected Markdown file, and the ability to read a source file does
not qualify its volume for KMX state. The source loader independently requires a
bounded regular file and refuses symlink, junction or other reparse components.
Test journals and retained failure diagnostics still go only to the qualified
writable state root.

## Release availability and installation

Stable v0.8.1 and the current `@latest` selection do **not** contain
`kmx-windows-amd64.exe` or `kmx-windows-arm64.exe`. Do not substitute a Unix
binary, rename an asset, or treat a locally cross-built executable as a released
artifact.

The repository's `install.sh`, Homebrew formula and generic Go `@latest`
instructions remain Unix or WSL installation routes where applicable. They are
not native Windows installers. Kaimahi makes no Winget, Chocolatey, Scoop or
other Windows package-manager namespace claim.

Published v0.9.0 release candidates beginning with `v0.9.0-rc.4` list the
Windows assets. Download the exact `.exe`, `checksums.txt` and
`checksums.txt.sigstore.json` from one containing tag. Do not use these commands
with v0.8.1. Select `amd64` for an x64 machine or `arm64` only with the
[arm64 evidence limitation](#what-ci-and-release-qualification-prove) understood:

```powershell
$Version = 'v0.9.0-rc.6' # or another exact tag containing Windows assets
$Asset = 'kmx-windows-amd64.exe' # or kmx-windows-arm64.exe
$Base = "https://github.com/kaimahi-agents/kaimahi/releases/download/$Version"

Invoke-WebRequest "$Base/$Asset" -OutFile ".\$Asset"
Invoke-WebRequest "$Base/checksums.txt" -OutFile .\checksums.txt
Invoke-WebRequest "$Base/checksums.txt.sigstore.json" -OutFile .\checksums.txt.sigstore.json

cosign verify-blob `
  --certificate-identity "https://github.com/kaimahi-agents/kaimahi/.github/workflows/release.yml@refs/tags/$Version" `
  --certificate-oidc-issuer https://token.actions.githubusercontent.com `
  --bundle .\checksums.txt.sigstore.json `
  .\checksums.txt
if ($LASTEXITCODE -ne 0) { throw "Sigstore verification failed" }

$Rows = @(Get-Content .\checksums.txt | Where-Object {
  $_ -match ('^[0-9a-fA-F]{64}\s+\*?' + [regex]::Escape($Asset) + '$')
})
if ($Rows.Count -ne 1) { throw "missing or duplicate checksum for $Asset" }
$Expected = ($Rows[0] -split '\s+')[0].ToLowerInvariant()
$Actual = (Get-FileHash -Algorithm SHA256 ".\$Asset").Hash.ToLowerInvariant()
if ($Actual -ne $Expected) { throw "checksum mismatch for $Asset" }
```

Use cosign v3.0.6 or newer. Verify the Sigstore bundle before trusting the
manifest, then compare the exact asset hash before running it. If the asset,
bundle or one exact checksum row is missing, that release does not provide the
native Windows CLI.

### Executable trust and Windows policy

The Sigstore bundle signs the checksum manifest, not the PE executable. It
is not Authenticode. Windows `.exe` artifacts produced by current main may be
unsigned at the Authenticode layer. On a clean Windows machine, record the exact
release and asset, checksum/Sigstore result, Authenticode result, download-zone
observation and SmartScreen outcome before calling the release qualified for that
environment:

```powershell
Get-AuthenticodeSignature ".\$Asset" |
  Format-List Status, StatusMessage, SignerCertificate
Get-Item ".\$Asset" -Stream Zone.Identifier -ErrorAction SilentlyContinue

# Only after verification and only when local policy permits execution:
& ".\$Asset" version
```

Record whether `Zone.Identifier` was present; do not remove it for the test.
Record whether SmartScreen was actually evaluated and, if so, whether it allowed,
warned or blocked execution; a PowerShell launch with no shell reputation UI is
"not evaluated", not "allowed". Check organization policy through its normal
software-approval process when an Explorer/browser-origin observation is required.
Never claim that checksum verification is an OS publisher signature, and never use
`Unblock-File`, disable Defender or SmartScreen, change execution policy, enable
Developer Mode, or bypass organization policy to make KMX run. A policy block is
a blocker, not an installation step. Follow the organization's normal software
approval process or use WSL if that route is approved.

## What CI and release qualification prove

Windows amd64 is natively executed on hosted `windows-latest`. The native job
builds and vets the CLI, verifies version/help/completion, executes a real
Markdown file through package-free fake Run, a failing fake Test and offline
file Explain, and executes list/show against a compiled native kubectl protocol
fixture. Release qualification repeats the day-zero journey and reaches the
read-only inspection boundary with a checksum-verified real `kubectl.exe` using
the exact staged `kmx-windows-amd64.exe`. It does not claim a live remote cluster
or real Azure authentication; those remain separate runtime qualification.

Windows arm64 is cross-built, vetted and included in the checksum inventory. It
is not executed on native arm64 hardware. Do not describe the arm64 asset as
natively executed, and do not infer behavior beyond cross-build, vet and checksum
evidence.

Neither lane proves a live Kubernetes cluster, real exec-auth account, model or
provider route, native Copilot/MCP integration, power-loss recovery, or
organization policy acceptance. Hosted CI also cannot establish the clean-machine Authenticode,
`Zone.Identifier` and SmartScreen observation above; record that separately for
the exact release.

## Use WSL for broader workflows

Use the Linux build inside WSL for quickstart, Docker/kind/local Kubernetes,
workspace and OCI operations, model/provider access, MCP/Copilot integration,
publication, Deploy and every other deferred native operation. Follow the ordinary
[getting started](getting-started.md) and [release](releases.md) instructions
inside the Linux distribution, and keep its writable state in the distro's Linux
filesystem.

WSL is a separate execution environment, not a silent fallback. The native CLI
never starts WSL or forwards a refused operation to it. Invoke the approved WSL
workflow explicitly.
