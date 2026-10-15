# Review guidance for kaimahi

`AGENTS.md`, `CONTRIBUTING.md` and `docs/development.md#invariants-to-preserve` are the rules. When reviewing a pull request, focus on these, in order:

1. **Fail closed.** Missing, unreadable, partial or ambiguous state must refuse or report `unknown`, never pass, publish, deploy or count as "absent". Flag any path that turns an error into success, a default, or a silent skip.
2. **Evidence and identity.** Evaluation evidence binds the exact suite digest, member, runtime, model and cases. Flag anything that reuses, relabels or converts evidence across versions or runtimes, skips the gate, or lets lift switch runtime or model.
3. **Credentials and privacy.** No secrets, tokens or keys in argv, logs, manifests, ConfigMaps, receipts, published suite content, test fixtures or error messages. Prompts, answers and judge reasoning stay out of receipts and terminal summaries.
4. **Mutations name their target.** Cluster or registry writes use explicit, pinned contexts and destinations; remote targets need confirmation; nothing adopts, updates or deletes resources kmx doesn't provably own.
5. **Command surface.** Changes follow `docs/command-conventions.md` and `docs/kmx.md`; removed commands stay removed (no aliases or stubs); help and completion stay inert.
6. **Scope and size.** One change per PR. Point out unrelated edits, hand-edited generated files, timeout increases used to make tests pass, and tests that greatly outweigh the product change without covering distinct behavior.
7. **Docs and compatibility.** User-visible behavior changes update the docs and `CHANGELOG.md`; breaking changes say what to do instead.

Skip pure style nits that `gofmt`, linters or the repository checkers already enforce. Prefer one concrete comment with a failing scenario over several speculative ones.
