# Repository guidance

Read [CONTRIBUTING.md](CONTRIBUTING.md), the
[entry-point principles](docs/entry-point-principles.md), and the
[repository map](docs/repository-map.md) before changing command behavior.

## Command consistency

- Use the implemented interface in [docs/kmx.md](docs/kmx.md) and the
  [command alignment record](docs/command-semantics.md). Track outstanding
  renames and decisions in [#301](https://github.com/kaimahi-agents/kaimahi/issues/301).
  Proposed spellings are not implemented capabilities.
- Canonical context commands are `kmx context show` and
  `kmx context use <context>`; canonical local setup/teardown commands are
  `kmx local up/down`. The old `ctx`, `up`, and `down` spellings are compatibility
  routes. Current agent deployment remains `kmx agent lift`; current cloud and
  runtime setup remain `kmx aks up/down` and `kmx orka install/status`.
  The AKS group is a hidden compatibility route, not a public root domain.
- Keep **Create → Prove → Lift** as the local-to-remote story. **Lift** is the
  chosen agent lifecycle verb: retain `kmx agent lift` and interactive `/lift`,
  and use Lift in corresponding help and menu labels rather than renaming the
  operation to deploy. Deployment remains a technical description of its effect.
  Bare `kmx lift` is still the deprecated AKS compatibility route; changing its
  meaning requires a separate compatibility decision.
- Prefer singular resource/domain groups and explicit actions. Avoid introducing
  another synonym for an existing operation. CLI, chat and console should use
  the same operation vocabulary where their scopes match.
- Reuse the existing operation handlers. Keep Cobra syntax, terminal rendering,
  runtime capabilities, target mechanics and infrastructure ownership separate.
  A renamed command must not silently change runtime, target, ownership or
  supported operations.
- Update command registration, help, completion, relevant chat/console labels,
  generated next/recovery commands, primary docs and CHANGELOG in the same slice.
  Record changed defaults and compatibility/removal policy. Keep old routes
  callable until an announced transition; notices belong on stderr.
- Preserve report/artifact/answer stdout and exit contracts. Test meaningful
  selection, refusal and compatibility behavior, not just matching help strings.
  Help and completion must not perform operational mutations.
- A context supplies access. A target includes the selected runtime, cluster
  identity and namespace. Inference is independent. Unknown is not absent;
  ready is not answered or evaluated; retry is not result retrieval.
- Preserve historical release records and explicit compatibility tests when
  updating current examples. Do not bulk-rename historical text or serialized
  state. Generated recovery commands must preserve selectors and shell quoting.

## Verification

Run checks appropriate to the changed paths as described in CONTRIBUTING.md.
Command changes normally require `go test ./cmd/kmx`, affected operation-package
tests, documentation links, repository-map and legacy-runtime checks. These
inventory checkers use tracked files, so include intended additions in the index
when checking the complete change. Update AGENTS.md when implemented conventions
change so subsequent work follows the same rules.
