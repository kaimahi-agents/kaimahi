### Added

- `kmx create NAME` writes `NAME.agent.md` and one example in `NAME.agent.test.md` in the current folder without prompts or model calls. Use `--path DIR` for another folder and `--description TEXT` for frontmatter text. Existing targets are never overwritten (exit 2). An explicit `.github/agents` destination puts tests in sibling `.github/agent-tests`.

### Breaking

- Native workspace authoring moves from `kmx create` to `kmx package create`. The new root command does not accept workspace, model, instructions, runtime or experimental flow flags.

### Upgrading

- Change workspace scripts to `kmx package create NAME --workspace DIR --model MODEL --instructions FILE`; all native flags and terminal prompts keep their behavior. The root command prints the next file Run and Test commands. Companion loading is a separate follow-up; until it lands, Test on a newly created agent reports no tests (exit 2).
