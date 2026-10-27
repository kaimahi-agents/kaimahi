### Changed

- `kmx run FILE` now executes saved inputs without evaluating their checks or setting up a judge. Completed answers are not passing tests; use `kmx test FILE` to evaluate checks. Tool fixtures, journal finalization and completion checks still apply. Existing MCP preview behavior is unchanged.

### Upgrading

- Use `kmx test FILE` rather than `kmx run FILE` as a check gate in CI. A completed Run now exits successfully even when its authored checks would fail.
