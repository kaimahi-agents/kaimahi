### Added

- Add resident local Markdown agents: `run FILE` starts or reuses a process,
  local `chat` serves multiple sessions, `list` reports readiness/session state,
  and `stop` ends the worker. `run FILE --once` retains one-shot preview, while
  Test stays current-source based. Owner-only snapshot state stores no credentials;
  each Copilot turn acquires fresh bounded access.
- Add Helm-style snapshot lifecycle: immutable `li-...` worker generations can
  run in parallel with unique names, while confirmed upgrade readies and promotes
  a replacement before gracefully stopping old sessions. Stable `md-...` source
  IDs remain unchanged and refuse ambiguous selection.
- Standardize Run input on `-p, --prompt TEXT` and `--prompt-file FILE|-`; remove
  the second positional input and make either flag input-only unless `--case`
  explicitly selects a saved example too.
