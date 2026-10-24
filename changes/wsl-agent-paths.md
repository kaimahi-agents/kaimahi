### Fixed

- Resolve Windows drive-absolute agent paths through mounted WSL drives for MCP creation, editing, execution and resource reads, rather than writing literal Windows filenames in the server folder. The builder uses the session folder in WSL form.
