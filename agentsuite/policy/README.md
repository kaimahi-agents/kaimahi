# Policy contract

This package owns AgentSuite policy meaning. It strictly validates `SuitePolicy`, composes administrative restrictions without weakening, emits exact canonical `EffectivePolicy` bytes, and creates a domain-separated digest. Consumers preserve the bytes rather than reserializing them.

`filesystem/deny-write/v1` requires denial of create, writable open/write, truncate, rename effects, unlink, link, metadata and symlink mutation, including declared mount/path bypass coverage. A provider must reject the policy when it cannot cover the full required operation set.

## Language-neutral vector

`testdata/effective-policy-v1.json` contains the exact canonical UTF-8 bytes for a composed example. `testdata/effective-policy-v1.sha256` contains the expected domain-separated digest. Implementations must consume the JSON file bytes directly, not parse and reserialize them before digest verification.
