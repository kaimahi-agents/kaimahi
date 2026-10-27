<div align="center">

<img src="brand/ketu.svg" alt="Kaimahi ketu mark" width="128" />

# Kaimahi

</div>

You changed one line of your Copilot agent's prompt. Does it still answer well?

> You write a release-notes agent in the Copilot app.
>
> You add three examples of what a good answer must say and press Enter.
>
> One fails because the answer left out a pull request. You fix the prompt and press Enter again.

**Know your agent works before you share it.** kmx runs your examples in the
Copilot app, shows what broke and why, compares models and replays tool calls.

[Get started in the Copilot app](docs/copilot-app.md) · [agent.kaimahi.dev](https://agent.kaimahi.dev)

**Status: experimental. Not recommended for production use.**

## Package and deploy to Kubernetes

**Run / Test / Publish:** Run invokes an existing live Orka Agent; Test checks
workspace cases with `kmx package test`; Publish transfers the tested package with
`kmx package push`. Deploy places that package on a real target. Run is not local
workspace execution.

These docs follow main. `kmx package test` and `kmx package deploy` are unreleased; v0.8.1 uses
`kmx eval` and `kmx lift` with the same arguments. See
[Test compatibility](docs/kmx.md#test-compatibility-unreleased) and
[Deploy compatibility](docs/kmx.md#deploy-compatibility-unreleased).

## Try it locally

Have Docker running and Homebrew installed. For Podman, [select it explicitly](docs/getting-started.md#using-podman-instead-of-docker).
This cluster quickstart runs on macOS, Linux or Windows Subsystem for Linux (WSL),
not native Windows. Current `main` has a separate, unreleased
[native Windows day-zero CLI](docs/windows.md) limited to version/help/completion,
package-free fake `run FILE` / `test FILE`, and offline
`test explain --file FILE`. Stable v0.8.1 and `@latest` have no Windows `.exe`;
use WSL for quickstart and every broader workflow.

For stable v0.8.1:

```sh
brew install kaimahi-agents/tap/kmx
kmx version
kmx quickstart
```

On unreleased main, the command is `kmx cluster quickstart`.

Quickstart prepares a local Kubernetes test cluster and runs a sample agent.
It succeeds only after the agent returns an answer. On an uncached first run,
the local model download is about **1.9 GB**. It changes local cluster resources;
no Azure account or model key is needed.

Use a dedicated test environment. See [other install methods, prerequisites and cleanup](docs/getting-started.md).

## Agent files in Copilot

On macOS, use the [plain Copilot plugin](plugins/copilot/README.md#install-on-macos):
install the rc.4 prerelease darwin binary, register `kmx-plugins`, then install the
PATH plugin. macOS CI checks installation and
the offline fake-model loop, not a Copilot model or the desktop app.

### Windows through WSL

For the Markdown-agent inner loop without a Kubernetes cluster, use the
[WSL Copilot plugin](plugins/copilot-wsl/README.md). Install the paired v0.9.0-rc.4 prerelease in WSL:

```sh
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/v0.9.0-rc.4/install.sh | sudo sh -s -- --version=v0.9.0-rc.4 --bin-dir=/usr/local/bin
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
- [Native Windows boundary](docs/windows.md): unreleased local fake previews,
  release availability, private state and WSL separation.
- [Azure walkthrough](docs/aks-demo.md): take a local proof to a remote cluster.
- [All guides](docs/README.md), [development](docs/development.md) and [contributing](CONTRIBUTING.md).

[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)

[About the ketu mark](brand/README.md#ketu-mark) · [Security reporting](SECURITY.md)
