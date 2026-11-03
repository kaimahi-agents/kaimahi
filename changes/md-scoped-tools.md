### Added

- Console MD actions offer Suggest tools, Review & approve tools, and explicit
  running-agent update. Creation connects accepted declarations to a scrollable
  executable/scope/probe review; explicit confirmation runs cancellable probes
  through the shared approval implementation. Results provide edit/test/chat/update
  next steps while keeping approval, probe results, tests and instance state separate.

- Tool suggestions use bounded, non-executing PATH name lookups to inform command
  availability. No candidate executable, compatibility probe, installer or login
  runs during suggestion; approval remains a separate identity-bound operation.

- CLI tool failures retain their nonzero status and return bounded, sanitized
  stdout/stderr to the model for compiler/test diagnosis. Both streams share the
  declared output budget; credential-shaped, invalid or incomplete output is
  withheld. Probe patterns remain stdout-only. Includes a real local failing
  Go-test regression and a proposed TUI approval/result integration sequence.

- Keep canonical Windows parser/MCP builds independent of Unix-only Markdown
  instance primitives; native Windows instance IO/execution refuses with WSL guidance.

- Post-import inference proposes up to three scoped CLI commands with fixed argv,
  bounded operands/time/output and per-tool allow/deny, without a GitHub-first
  question. Missing scope blocks acceptance; accepted declarations still require
  separate executable approval. Run/Test
  complete local short IDs and names (including stopped instances), resolve them
  to working-copy source and allow file completion when no local selector matches.

- Same-file CLI tool declarations with PATH-resolved executables, fixed argv and
  schema/regex-limited operands. Explicit local approval runs optional bounded
  compatibility probes (exit code + stdout regex) before grant persistence;
  activation/update repeats probes and checks executable identity. Includes a
  Go-version example and hermetic cancellation/scope/refusal tests.

- Inference-assisted local tool proposals and explicit review/apply for fixed
  GitHub PR read capabilities scoped to one repository. Private grants bind the
  definition, adapter and gh executable; execution uses bounded fixed HTTPS GETs
  without redirects and memory-only gh authentication.
- Approved per-agent SDK handlers, binding-aware version updates and mocked test
  identity, plus TUI suggestion/review actions and a PR-monitor example. Scope
  approval is separate from suggestion, and local tools do not authorize Deploy.

### Fixed

- Integrate current canonical Copilot-file Run/Test and native Windows CLI without
  intercepting their supported routes. Persistent local agents remain --type md.
- Enforce complete CLI argument regex matches; canonicalize grant/source parent
  paths, ignore obsolete grants for changed tool-free definitions, and filter
  mocked tests to the approved tool inventory.
- Move activation probes outside the shared manager lock with post-probe identity
  checks. Separate UI attachment state and cached execution from editable paired
  tests; expand bounded RPC envelopes for escaped 64-KiB prompts/answers.
- Preserve YAML for ordinary help-valued operands, accept leading-blank charters,
  fix instance directory filtering/system symlink ancestors, isolate tests from
  ambient state and correct stale documentation/discovery labels.
