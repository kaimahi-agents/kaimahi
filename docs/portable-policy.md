# Enforcement policy

The `github.com/agent-primitives/suite/policy` package owns policy validation,
composition, canonical effective bytes and effective digest generation. KMX pins
suite v0.3.1, including its OCI loader nonregular-file/FIFO fix. Resolving or packaging a policy does **not** select, install or
qualify an enforcement implementation.

## Resolve offline

```sh
kmx package policy policy.json
kmx package policy policy.json --administrative-policy admin.json
```

Stdout contains canonical effective-policy JSON on the first line and its
`sha256:` digest on the second. Each input file and the canonical output are
limited to 4 MiB. Omitting the administrative flag is allowed; supplying an
explicitly empty flag refuses rather than silently dropping the input. Resolution
loads no operational configuration and contacts no cluster or model. Malformed
policy diagnostics are safe to print to a terminal.

Two strict JSON versions are supported:

- `agentsuite.policy/v1`: filesystem deny-write paths, with required capability
  `filesystem/deny-write/v1`. Paths are clean and absolute; administrative deny
  paths union with authored deny paths. Existing bytes and digests are unchanged.
- `agentsuite.policy/v2`: network egress allowances, with required capability
  `network/egress-allow/v1`. Each endpoint has a literal IPv4 `address`, `TCP`
  `protocol` and an explicit integer `port` (1–65535). Hostnames, CIDRs, UDP and
  IPv6 refuse. An explicit empty list denies all egress. Administrative allowances
  intersect with authored allowances: omission adds no restriction, while an
  explicitly empty administrative list denies all.

For example, resolve this v2 `policy.json`:

```json
{
  "version": "agentsuite.policy/v2",
  "network": {
    "egressAllow": [
      {"address": "10.20.30.40", "protocol": "TCP", "port": 443}
    ]
  },
  "requiredCapabilities": ["network/egress-allow/v1"]
}
```

A matching administrative document uses `{"network":{"egressAllow":[]}}` to
deny all, or a list of the same endpoint objects to set an administrative ceiling.
The decoder follows the selected suite policy version; filesystem administration
cannot be applied to a network document. Unknown versions or unsupported required
capabilities refuse.

Effective resolution sorts and deduplicates sets. Its authoritative bytes use the
upstream domain-separated digest: `agentsuite-effective-policy\0v1\0`, followed
by an unsigned 64-bit big-endian byte length and the canonical bytes. The bytes
include the policy version. Provider payloads carry those exact bytes and semantic
capabilities; they are not enforcement evidence.

## Declare a workspace member policy

In an Orka member's `agents/<id>/member.yaml`, add:

```yaml
policy: policy.json
```

The path is relative to that member directory, not the invocation directory.
Place the public v2 JSON document there. Only omission means no policy; explicit
blank or null values refuse. Existing confined reads refuse unsafe paths, static
symlinks and nonregular files. Policy source shares the workspace's 1 MiB per-file
and 8 MiB aggregate limits. Concurrent hostile workspace mutation is outside this
reader's guarantees.

Preparation selects the original file into the source fingerprint and provenance,
then packages **authored JCS bytes** at `policies/<id>.json`, referenced through
`Agent.policy`. Its descriptor uses the ordinary SHA-256 of those canonical bytes,
not the effective-policy digest above. Endpoint array order and duplicates remain
part of authored identity; effective resolution must not rewrite this document.
Whitespace-only edits change the source fingerprint but not packaged identity.
Policy edits propagate through the Agent and suite graph to the OCI digest.
Omission preserves existing policy-free source fingerprints, bytes and digests.

Offline preparation, inventory loading and explicitly unevaluated transfer retain
the policy. Multi-member authoring remains offline-only. Policy declarations on
kagent or AgentSessions source members refuse. General suite transfer validates
public-format bindings without claiming enforcement.

**Execution is unavailable for declared policies.** Workspace eval, private native
preparation, composed and digest Deploy, evaluated publication and the experimental
AgentKit sandbox builder refuse rather than ignore a declaration. No policy is
installed or enforced, and no allowed/blocked worker traffic has been qualified by
these offline operations. Runtime enforcement requires a separately qualified
backend and an Orka release supplying controller-owned worker labels; the current
pinned Orka v0.2.0 does not supply them. No-policy runtime behavior is unchanged.
