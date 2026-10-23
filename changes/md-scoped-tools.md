### Added

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
