Install the pinned native Orka source profile from a local bundle with
`kmx cluster orka install --provider - --offline-bundle DIR`. The command verifies
local chart/OCI bytes, renders fixed native-only settings and checks every
schedulable kind node's exact image cache before writing. It never downloads
tools/charts/images or configures models. Exact healthy reuse preserves chart
keys and data; changed, foreign or partial installs refuse without repair.
`--no-apply` is local-only; `--dry-run` validates CRDs server-side after preflight.
The complete catalog changes the profile hash required in bundle records.
