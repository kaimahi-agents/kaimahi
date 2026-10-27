### Added

- Add an internal read-only companion test reader with fixed sibling/repository pairing, explicit selection and pair snapshot identity. The format uses `schemaVersion: 1` frontmatter and one `yaml agent-test` fence per test with existing check and tool-fixture semantics. CLI, MCP and canvas execution/authoring are not switched to companion files in this foundation slice; existing inline tests remain unchanged.
