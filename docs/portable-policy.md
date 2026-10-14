# Enforcement policy

The `agentsuite/policy` package owns policy validation, composition, canonical bytes, and digest generation. It does not select or configure an enforcement implementation. Input is strict JSON with clean absolute paths. Resolution sorts and deduplicates set-valued fields and unions administrative deny paths, so administration cannot weaken requested restrictions.

Canonical bytes are authoritative. The digest preimage is `agentsuite-effective-policy\0v1\0`, followed by the unsigned 64-bit big-endian byte length and the bytes. Provider payloads carry those exact bytes and required semantic capabilities. Unknown versions and unsupported capabilities fail closed.
