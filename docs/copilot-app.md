# Use kmx in the Copilot app

You want to improve an agent's prompt without losing track of what it should do.

> You are building a release-notes agent. Give it a few change summaries and
> check that each answer has a heading, mentions the change and avoids internal
> issue numbers. Change the prompt, rerun the same tests, then try another model.

kmx keeps the prompt and tests in one Markdown file. The Copilot app opens that
file in a canvas where you can edit and test it. This path needs no Kubernetes
cluster, Docker or local model download.

**Experimental.** This page describes v0.9.0-rc.3. The install below pins that
binary; updating the plugin alone does not update kmx. Features merged after
rc.3 need a newer binary too.

## Install

On Windows, you need the Copilot app and CLI, a signed-in Copilot account with
model access, and a default WSL distribution. Native Windows kmx is not supported
for this path.

1. In WSL, install kmx:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/v0.9.0-rc.3/install.sh | sudo sh -s -- --version=v0.9.0-rc.3 --bin-dir=/usr/local/bin
   ```

2. In a Windows terminal, install the plugin:

   ```powershell
   copilot plugin install kaimahi-agents/kaimahi:plugins/copilot-wsl
   ```

The installer checks the binary's published SHA256 over HTTPS, not its separate
signature bundle. The plugin uses `/usr/local/bin/kmx` in your default WSL
distribution. You do not select a folder or model during installation.
The CLI warns that direct plugin installs are deprecated; a marketplace install
is not available yet.

On macOS, use the [macOS install instructions](../plugins/copilot/README.md#install-on-macos)
instead. They install a macOS binary and the plain Copilot plugin, not WSL.

### Check model access

Windows Copilot app sign-in does not sign in kmx inside WSL. In WSL, with the
[GitHub CLI](https://cli.github.com/) installed, sign in to your Copilot-enabled
GitHub account and check access before running hosted tests:

```sh
gh auth login --hostname github.com
kmx model list --provider copilot
```

On macOS, run the same check in your Mac terminal. kmx can use that existing gh
sign-in without copying it into kmx's keychain. If listing fails, follow the
[Copilot authentication options](kmx.md#copilot-model-access) before continuing;
kmx does not read the app's credentials or start sign-in during a test.

## Create an agent

Open a conversation in the Copilot app and ask:

> Use kmx to create a release-notes agent at
> `/mnt/c/work/release-notes/release-notes.agent.md` and open its canvas.
> It should turn a change summary into a short release note with a heading,
> explain the user-visible change and omit internal issue numbers.

Use a path in your own repository. On Windows, kmx resolves drive-absolute paths
such as `C:\work` through their WSL mounts (`/mnt/c/work`); WSL paths also work.
Files can live in different repositories; installation does not bind kmx to one folder.

The canvas shows **Prompt**, **Tools** and **Tests**. The saved `.agent.md` file
holds the prompt, tool names and tests; you can keep it in version control with
the code it describes. If your host
cannot render the canvas, Copilot can still use kmx's tools in text.

*Screenshot to add: a newly created agent with Prompt and Tests visible.*

## Choose tools for tests

With a matching rc.3 plugin and binary, **Add tools** opens this app's
MCP servers. Tick tools to add them to the agent. A ticked tool's **Enabled** or
**Disabled** word flips a draft; Disabled tools stay declared but do not run in tests.
Enter saves edits, then tests. Esc discards Tools edits without discarding prompt
or test edits. Server rows show saved disabled counts; the tool page shows drafts.

Tool names live in the agent file. Test settings live in `kmx.yaml` beside its
physical target and also affect other agents in that folder. These are separate
saves, not one transaction. If a save is partial or unconfirmed, tests do not run:
keep the draft and deliberately reload to inspect what was saved. Do not retry
an uncertain write. This switch uses `set_tool_state`, included in the pinned
v0.9.0-rc.3 binary above.

## Add tests and run them

Ask Copilot to add and save tests for the agent. For example:

> Add tests for a bug fix and a new feature. Check that both answers include
> a Markdown heading and mention the change. In the bug-fix test, include the
> internal issue number BUG-123 in the input and check that it is absent from
> the answer. Save the tests; do not run them yet.

You can also use **Suggest tests** or add and edit tests in the canvas. A
suggestion request is not a completed save: wait for the saved tests to appear.
Open a test to see **The agent gets** and **Passes if**. Use explicit checks for
requirements you can measure; an answer that looks good is not itself a check.

Press **Enter** in the main view to save pending edits and run every test.
**Shift+Enter** adds a line break. In a test's detail view, the save/run control
runs only that test; a passing subset does not mean the whole agent passed.
Each Run or Test invocation has a five-minute overall bound, with at most two
minutes per turn including judging; hitting a deadline is an error, not a pass.

Testing sends the agent prompt and test inputs to the selected hosted model.
Judged checks also send the answer and rubric. Local preview journals retain
instructions, inputs and answers in cleartext; read the
[journal retention and clearing guidance](local-preview.md#journal-retention-and-clearing)
before using private material. Never put secrets in an agent file or test.

Read the verdicts and open a test to inspect its answer and check explanations.
The canvas shows when Copilot approval is waiting in chat. The host-tool relay
allows 30 seconds per call, including approval waiting. A deadline does not dismiss
the approval: granting it later may still execute the tool. The result is unconfirmed;
reload before continuing to inspect the saved file.
Test verdicts appear as each case finishes. They remain provisional until the
full Test reply confirms the run. A running indicator does not mean a test has passed.
Known issue: Copilot may keep showing “Working” after a canvas run until the next
chat turn, even when the run has finished.

*Screenshot to add: a completed test and its check explanations.*

## Change the prompt and rerun

Edit **Prompt**, then press **Enter** again in the main view. This saves the
prompt and runs the same tests. Editing or moving between tests alone does not
save or run anything. Old results become stale when the source changes; dimmed
last-run results are not proof that your new prompt passes.

If another editor changes the file while you have unsaved edits, use **Compare**
to inspect the differences, then choose **Keep my edits** or **Use the new
version** before saving.

## Try another model

Use the model picker to select an available Copilot model, then press **Enter**
to run the same tests again. The choice applies to execution; it does not rewrite
the agent file's saved model. Changing models makes old results stale.

The canvas keeps two models' verdicts side by side for the current source.
Switch the picker to inspect each model's results without another run. This
comparison lasts only in the open canvas, not as durable run history; record
anything you need to keep before closing it. A model picker choice is not a test
result, and a preview pass is not deployment evidence.
Declared tools bind to the app's current MCP servers when the host supports the
binding path. An unbound tool gives one plain reason; judges remain tool-free.
See the [CLI reference](kmx.md#agent-file-local-preview-unreleased-integration)
for supported file-preview routes and limits.

*Screenshot to add: the model picker and a rerun on a second model.*

## Run the same tests in CI

Commit the agent file, then run `kmx test FILE` in a GitHub Actions step after
checkout. Use a file-preview-capable binary such as v0.9.0-rc.3, not v0.8.1.
These steps install it on an Ubuntu runner and test `greeter.agent.md` using its
authored `fake` model:

```yaml
- name: Install kmx
  run: curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/v0.9.0-rc.3/install.sh | sudo sh -s -- --version=v0.9.0-rc.3 --bin-dir=/usr/local/bin
- name: Test the agent
  run: kmx test ./greeter.agent.md
```

For an offline check, start with the [complete greeter sample](../internal/kmx/agentfile/testdata/greeter.agent.md).
Its `fake` model follows a small set of deterministic instructions; it checks
wiring, not a real model's quality. Your release-notes agent needs a real model:
use an available model ID and `kmx test ./release-notes.agent.md --provider copilot`
with credentials supplied securely to the runner. App sign-in is not CI sign-in;
see [Copilot authentication](kmx.md#copilot-model-access).
Never commit credentials or print them in workflow logs.

File-mode exit codes make this a CI gate without parsing the output:

| Exit | Meaning |
|---|---|
| `0` | All selected checks completed and passed. Use no case filter to test the whole file. |
| `2` | A completed assertion failed or was unknown. |
| `1` | Refusal or execution, deadline, journal or output failure. |
| `130` | Cancelled. |

GitHub Actions fails the step on a nonzero exit. Do not use `continue-on-error`
if these tests are meant to gate the pull request. This file-mode check does not
publish a package or create evidence for deployment.
