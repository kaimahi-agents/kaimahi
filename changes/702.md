### Changed

- `kmx mcp` accepts agent-file paths in any directory without `--root`. The optional flag remains compatible with existing bound plugins and native Squad setup. Agent paths may use ordinary folder names and links to agent files, but cannot resolve to non-agent files.
- The Copilot canvas accepts full agent paths and distinguishes same-named agents in different repositories. The builder skill now opens the created agent's canvas without a folder confirmation.
