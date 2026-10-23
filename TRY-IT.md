# Try kmx in Copilot

**Preview for macOS (including Apple silicon) and Windows with WSL.** You need
[Copilot app](https://docs.github.com/en/copilot/how-tos/github-copilot-app/getting-started),
its `copilot` CLI on PATH, and Copilot access. No Docker, Go, Homebrew or cosign.
Windows uses the Windows app/CLI plus an existing Ubuntu WSL distro, not a native kmx build.

## Install

The command below is for **v0.9.0-rc.1, once published**. v0.8.1 has no plugin
bundle. Don't substitute it or install the repository's source-only plugin.
Run in Terminal on Mac, or inside your intended WSL distro:

```sh
( setup=$(mktemp) && trap 'rm -f "$setup"' EXIT && curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/v0.9.0-rc.1/plugins/copilot/try-it.sh -o "$setup" && sh "$setup" --version v0.9.0-rc.1 )
```

**WSL:** append `--windows` to the `sh` command. The Windows `copilot.exe`,
`github.exe`, `cmd.exe` and `wsl.exe` must be on your WSL PATH. The script shows
both the distro and folder before you confirm. It checks Copilot credentials
through the same `wsl.exe -d DISTRO -e ENGINE` route the app will use.

Press Enter for `~/kmx-try-it-sample`, or enter an absolute folder whose parent
already exists. Confirm the exact folder; kmx is bound to that folder, not every
project you open. Existing sample files are never overwritten. Folder names
containing quotes, backslashes, control characters or plugin placeholders refuse.
If asked, follow `kmx model login`'s device sign-in, then choose an exact model
from your catalog. The suggested model is `claude-haiku-5.5`; no fallback is used.
Run/Test sends the charter, inputs, answers and judge rubrics to Copilot. Nothing
is posted. The script download trusts GitHub and HTTPS; its digest is not checked.
The script verifies the release archive against its release checksum over HTTPS.
That checksum covers the archive only, **not an independently verified publisher
signature**.

## Three things to try

1. **Open it.** Start a new session in the opened folder. Confirm the same root
   when the kmx skill asks, then say “Open `release-writer.agent.md` in the kmx
   agent view.” It shows instructions and three examples, initially **No current
   result**. If canvas opening is unavailable, `/agent-view release-writer.agent.md`
   shows text. Today's canvas is read-only; use chat for changes and Run/Test.
2. **Break and fix.** Ask “Test all examples for `release-writer.agent.md`.” The
   starting instruction omits issue references, so the issue-reference example
   is intended to fail. Real models and judges vary; don't expect a guaranteed
   score. Ask Copilot to replace only `Omit issue numbers and issue references
   from the notes.` with `Keep every issue reference as "Fixes #N".` Keep the
   examples unchanged, then test all three again. Restore the old line to break
   it again. Reopen the view for current results; an old view does not refresh.
3. **Run an input.** Ask “Run `release-writer.agent.md` with input:
   Merged: Add dark mode, closes issue #219. Users can work comfortably at night.
   Show the bounded answer.” Review the draft; the agent cannot post it.

## Other models and uninstall

To change Copilot models, choose an exact ID from the bundled engine's
`model list --provider copilot`, then change **Preferred and every judgeModel**
in the agent file. For a model you already run locally, also change the staged
plugin's startup args from `--provider copilot` to your supported provider and
`--endpoint URL`; see [model routing](plugins/copilot/README.md#try-it-model-routes).
Reinstall that directory and start a new session after changing startup config;
never edit Copilot's cache or let the assistant rebind the root.

Uninstall with `copilot plugin uninstall kmx-agent-builder` (WSL: use
`copilot.exe`), then close its sessions. Your sample stays. After uninstalling,
you can delete only the setup run directory printed by the script; WSL also
prints its Windows staging directory. `ENGINE model logout` removes kmx's login,
not your Copilot app login. Don't delete another installation's shared state.

## If something goes wrong

After prerequisite checks, the script prints a `setup.log` path, including on
later failures. Send Tom that file and
which step failed. It contains platform, release, checksum and setup-stage
status—not tokens, prompts or answers. Review anything else before sharing it.
For a Run/Test error, send the safe reason shown in chat, not credentials or
raw model traffic. If install was interrupted after creating the sample, retry
`copilot plugin install` with the configured directory printed by setup, rather
than overwriting the sample. On Mac, terminal-only tokens or `gh` on a different
PATH may not reach the app; use the printed engine's `model login` for keychain
sign-in, then start a new session. Don't clear macOS quarantine or bypass a
Copilot trust refusal.
