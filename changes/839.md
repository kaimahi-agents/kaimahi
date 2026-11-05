### Added

- `kmx cluster discover` lists local kubeconfig context labels, or namespaces in an explicit `--context`, without changing configuration or provisioning runtime resources. Opt into bounded JSONL with `-o jsonl`; version capabilities name the implemented `contexts` and `namespaces` scopes. Incomplete reads preserve safe partial rows and exit 1. Target discovery and Send are not included.
