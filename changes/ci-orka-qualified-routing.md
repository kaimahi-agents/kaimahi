### Changed

- Skip Orka runtime qualification on PRs only when every changed path is explicitly unrelated. Shared runtime, deploy, chart, command-builder, module and unknown changes retain the full proof; required check names stay unchanged.
- Run the same full Orka proof nightly on main and before every release and release dry run. Nightly failures surface in Actions and one assigned health issue. Main pushes retain full qualification during rollout.
