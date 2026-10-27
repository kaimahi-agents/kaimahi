<div align="center">

<img src="brand/ketu.svg" alt="Kaimahi ketu mark" width="128" />

# Kaimahi

</div>

You need to test an agent before shipping it, and deploy the same version you tested.

> You write instructions for a release-notes agent.
>
> Run it against a few example requests in a local test cluster.
>
> When the tests pass, publish that exact package.
>
> Deploy it to another cluster after reviewing the target and planned changes.

Kaimahi's `kmx` command-line tool helps developers create, test and deploy
agents on Kubernetes. It packages the agent, runs test cases and moves that
package to the selected cluster. It is not an agent runtime.

**Status: experimental. Not recommended for production use.**

**Run / Test / Publish:** Run invokes an existing live Orka Agent; Test checks
workspace cases with `kmx test`; Publish transfers the tested package with
`kmx push`. Deploy places that package on a real target. Run is not local
workspace execution.

These docs follow main. `kmx test` and `kmx deploy` are unreleased; v0.8.1 uses
`kmx eval` and `kmx lift` with the same arguments. See
[Test compatibility](docs/kmx.md#test-compatibility-unreleased) and
[Deploy compatibility](docs/kmx.md#deploy-compatibility-unreleased).

[Use kmx in the Copilot app](docs/copilot-app.md) to create an agent, edit its prompt
and run tests without Kubernetes or Docker.

## Try it locally

Have Docker running and Homebrew installed. For Podman, [select it explicitly](docs/getting-started.md#using-podman-instead-of-docker).
On Windows, use Windows Subsystem for Linux (WSL); native Windows builds are not
supported yet.

```sh
brew install kaimahi-agents/tap/kmx
kmx version
kmx quickstart
```

Quickstart prepares a local Kubernetes test cluster and runs a sample agent.
It succeeds only after the agent returns an answer. On an uncached first run,
the local model download is about **1.9 GB**. It changes local cluster resources;
no Azure account or model key is needed.

Use a dedicated test environment. See [other install methods, prerequisites and cleanup](docs/getting-started.md).

## Agent files in Copilot

On macOS, use the [plain Copilot plugin](plugins/copilot/README.md#install-on-macos):
install the rc.3 prerelease darwin binary, register `kmx-plugins`, then install the
PATH plugin. macOS CI checks installation and
the offline fake-model loop, not a Copilot model or the desktop app.

### Windows through WSL

For the Markdown-agent inner loop without a Kubernetes cluster, use the
[WSL Copilot plugin](plugins/copilot-wsl/README.md). Install the paired v0.9.0-rc.3 prerelease in WSL:

```sh
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/v0.9.0-rc.3/install.sh | sudo sh -s -- --version=v0.9.0-rc.3 --bin-dir=/usr/local/bin
```

Then, in a Windows terminal:

```powershell
copilot plugin marketplace add kaimahi-agents/kaimahi
copilot plugin install kmx-agent-builder@kmx-plugins
```

Ask Copilot to create an agent through kmx and open its canvas. No folder or model
is selected at install time. Choose a model in the canvas or use the agent file's
model; Enter saves edits and tests it. This preview is not deployment evidence.
This is three install commands across two terminals. Catalog entries pin the
plugin to the paired release commit, not main; installing a plugin does not
update kmx. See [direct-install migration and paired upgrades](docs/copilot-app.md#existing-direct-installs-and-upgrades).

## Go deeper

- [Create an agent of your own](docs/getting-started.md#an-agent-of-your-own): instructions, model selection and destination setup.
- [Workflow and runtime details](docs/workflow-overview.md): individual stages, version limits, safety and the workspace/live distinction.
- [Workspace lifecycle](docs/suite-workspace.md) and [runtime support](docs/runtime-adapters.md): exact package identities, evaluation evidence and supported operations.
- [Command reference](docs/kmx.md) and [models](docs/models.md).
- [Azure walkthrough](docs/aks-demo.md): take a local proof to a remote cluster.
- [All guides](docs/README.md), [development](docs/development.md) and [contributing](CONTRIBUTING.md).

[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)

[About the ketu mark](brand/README.md#ketu-mark) · [Security reporting](SECURITY.md)
