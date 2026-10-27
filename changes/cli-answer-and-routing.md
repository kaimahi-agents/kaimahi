# File Run/Test match the canvas

`kmx run FILE "input"` now writes the answer to stdout and the selected route/model
to stderr. Without input it prints each saved example answer. JSON includes full
sanitized Run answers. Failed tests show the canvas's check wording and the first
answer lines. Use `--no-content` to omit answers, failure operands and judge content.

Non-fake models default to Copilot with an existing sign-in; there is no prompt or
provider fallback. `--model` and `--provider` override per call. Without sign-in,
the error names `gh auth login` and `--provider ollama --provider-url URL`.

Upgrade: file commands remove `--show-content` (content is now on by default) and
rename `--endpoint` to `--provider-url`. Workspace Test, Deploy and Explain retain
`--show-content`; MCP startup retains `--endpoint`. Existing file formats are
unchanged. Positional Run input executes only that input, unless `--case ID` also
selects a saved example; legacy `--prompt` and
`--prompt-file` retain their ad-hoc-plus-examples behavior.

File-mode exits are 0 for passed checks or a completed ad-hoc Run, 1 for failed
checks, and 2 when execution cannot run or decide. Output/summary write failures
and command-line parsing errors remain 1; cancellation remains 130. Workspace/live exits are unchanged.
`--github-summary` remains content-free even when JSON includes failure content.
