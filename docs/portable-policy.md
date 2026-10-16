# Enforcement policy

The `github.com/agent-primitives/suite/policy` package owns policy validation, composition, canonical bytes, and digest generation. It does not select or configure an enforcement implementation. Input is strict JSON with clean absolute paths. Resolution sorts and deduplicates set-valued fields and unions administrative deny paths, so administration cannot weaken requested restrictions.

Canonical bytes are authoritative. The digest preimage is `agentsuite-effective-policy\0v1\0`, followed by the unsigned 64-bit big-endian byte length and the bytes. Provider payloads carry those exact bytes and required semantic capabilities. Unknown versions and unsupported capabilities fail closed.

Resolve a policy without loading operational configuration or selecting a runtime:

```sh
kmx suite policy policy.json
kmx suite policy policy.json --administrative-policy admin.json
```

Stdout contains canonical effective-policy JSON on the first line and its
`sha256:` digest on the second. Each input file and the canonical output are
limited to 4 MiB. Omitting the administrative flag is allowed; supplying an
explicitly empty value refuses instead of silently dropping that input.
Malformed policy diagnostics are safe to print to a terminal. These commands
produce policy payloads only; they do not enforce filesystem restrictions.
