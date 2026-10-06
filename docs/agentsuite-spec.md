# AgentSuite Artifact Specification

**Status:** Draft
**Version:** `1.0.0-draft`
**Last updated:** October 5, 2026

## Abstract

AgentSuite is a portable, content-addressed definition of one or more related
agents and the exact tools from which their runnable sandboxes are derived. An
AgentSuite is distributed as an artifact conforming to the
[OCI Image Format Specification][oci-image-spec]. Each runnable agent and
platform has an exact tool-set lock and a pinned build profile. A producer
combines those records by file composition to create an Agent Sandbox Image.

This specification defines the OCI envelope, content layout, closed JSON
schemas, canonical identities, tool bundle model, sandbox-image binding,
validation rules, and conformance classes. It does not define session history,
checkpoints, mutable memory, credentials, or a new registry protocol.

## 1. Conventions

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHALL**, **SHALL NOT**,
**SHOULD**, **SHOULD NOT**, **RECOMMENDED**, **NOT RECOMMENDED**, **MAY**, and
**OPTIONAL** are to be interpreted as described by [BCP 14][bcp14] when, and
only when, they appear in all capitals.

Unless stated otherwise:

- a digest is `sha256:` followed by 64 lowercase hexadecimal characters;
- a document is UTF-8 JSON;
- an identifier matches `^[a-z0-9][a-z0-9._-]{0,127}$`;
- a platform is exactly `linux/amd64` or `linux/arm64`;
- a path is relative, slash-separated, clean, and contains no backslash,
  empty segment, `.` segment, or `..` segment;
- a consumer operates offline after all referenced OCI blobs are present.

## 2. Scope

Version 1 specifies:

- an AgentSuite definition artifact carried by an OCI image manifest;
- one strict, closed content graph containing agents, tools, locks, and build
  profiles;
- bundled stdio MCP providers statically installed into an agent sandbox;
- remote MCP providers using streamable HTTP;
- exact Linux platform selection;
- pinned runtime-base and harness images;
- deterministic, network-free Agent Sandbox Image construction;
- a binding record embedded in each derived sandbox image;
- suite, sandbox-image, and runtime conformance classes.

## 3. Artifact model

An **AgentSuite Artifact** is an immutable definition. It is not directly
runnable.

An **Agent Sandbox Image** is a runnable OCI image derived for exactly one:

- AgentSuite manifest digest;
- agent identifier;
- platform;
- build profile;
- tool-set lock.

A tool may be referenced by several agents. Each derived Agent Sandbox Image
materializes that tool's exact platform bundle independently. Registry
deduplication MAY avoid storing identical blobs repeatedly, but reuse does not
change the image-local closure.

## 4. OCI envelope

### 4.1 Media types

| Object | Media type |
|---|---|
| [Artifact type](#3-artifact-model) | `application/vnd.agentsuite.suite.v1` |
| [Empty config](#43-empty-config) | `application/vnd.oci.empty.v1+json` |
| [Content layer](#5-content-layer) | `application/vnd.agentsuite.content.v1.tar+gzip` |
| [Suite manifest](#7-suite-manifest) | `application/vnd.agentsuite.manifest.v1+json` |
| [Agent manifest](#8-agents) | `application/vnd.agentsuite.agent.v1+json` |
| [Tool catalog](#91-tool-catalog) | `application/vnd.agentsuite.tool-catalog.v1+json` |
| [Tool manifest](#9-tools) | `application/vnd.agentsuite.tool.v1+json` |
| [Tool-set lock](#10-tool-set-locks) | `application/vnd.agentsuite.tool-set.v1+json` |
| [Build profile](#11-build-profiles) | `application/vnd.agentsuite.build-profile.v1+json` |
| [Sandbox binding](#13-sandbox-binding) | `application/vnd.agentsuite.sandbox-binding.v1+json` |

### 4.2 OCI image layout

An on-disk form conforming to the [OCI Image Layout
Specification][oci-image-layout] MUST contain:

```text
.
├── oci-layout
├── index.json
└── blobs/
    └── sha256/
        ├── <manifest-hex>
        ├── 44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a
        └── <content-layer-hex>
```

The blob filenames are the lowercase hexadecimal portions of their SHA-256
digests, without the `sha256:` algorithm prefix. Blob filenames do not identify
their semantic role; descriptors establish that role.

The descriptor graph is:

```text
index.json
└── AgentSuite manifest descriptor
    └── blobs/sha256/<manifest-hex>
        ├── config descriptor
        │   └── blobs/sha256/44136fa...caaff8a
        │       └── {}
        └── content-layer descriptor
            └── blobs/sha256/<content-layer-hex>
                └── AgentSuite tar+gzip content
```

`oci-layout.imageLayoutVersion` MUST be `1.0.0`.

`index.json` MUST be an OCI image index with exactly one descriptor selecting
the AgentSuite manifest. The descriptor media type MUST be
`application/vnd.oci.image.manifest.v1+json`. Its `artifactType`, when present,
MUST equal the AgentSuite artifact type.

The selected OCI manifest:

- MUST have `schemaVersion: 2`;
- MUST have the AgentSuite artifact type;
- MUST contain exactly one OCI empty config descriptor;
- MUST contain exactly one AgentSuite content layer;
- MUST NOT use `subject` to claim that a runnable image is authentic or bound
  to this suite.

Every descriptor size and digest MUST match the referenced regular blob.
Every blob referenced by `index.json`, the selected manifest, or a normative
AgentSuite descriptor MUST be present in the image layout. AgentSuite image
layouts are self-contained and MUST NOT rely on an external blob store to
fulfill a missing referenced blob. Descriptor `urls` MUST be absent. Descriptor
`data`, when present, MUST decode to the exact bytes of the referenced local
blob and does not replace that blob.

The single-layer rule is intentional for version 1: it gives a suite one
self-contained validation boundary. Future versions may profile multiple
content-addressed layers for cross-suite deduplication without changing the
logical manifests.

### 4.3 Empty config

Following the [OCI artifact and empty-descriptor
guidance][oci-artifact-guidance], the OCI manifest `config` descriptor MUST
use:

```text
application/vnd.oci.empty.v1+json
```

It MUST reference a blob containing exactly these two bytes:

```json
{}
```

The descriptor therefore has:

```text
digest: sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a
size: 2
```

`agentsuite.json` is the only normative source for the suite name, agents,
platforms, capabilities, extensions, and content graph. Producers MUST NOT
duplicate those fields into the config blob.

OCI annotations MAY provide non-authoritative discovery hints. A consumer MUST
NOT treat annotations as validated AgentSuite metadata and MUST validate
`agentsuite.json` before acting on them.

The OCI layer descriptor commits to the exact compressed content blob. Logical
AgentSuite identity comes from the JCS-digested graph rooted at
`agentsuite.json`, rather than from a second config document or a digest of tar
serialization details.

## 5. Content layer

### 5.1 Logical layout

```text
.
├── agentsuite.json
├── agents/
│   └── <agent>.json
├── instructions/
│   └── <agent>.md
├── tools/
│   ├── catalog.json
│   └── <tool>/
│       └── <version>/
│           ├── tool.json
│           └── <platform>/
│               └── ...
├── tool-sets/
│   └── <agent>-<os>-<architecture>.json
├── build-profiles/
│   └── <profile>.json
├── schemas/
│   └── ...
└── metadata/
    └── ...
```

Paths stored in manifests MUST be interpreted from the content root. Consumers
MUST NOT resolve host paths, environment-variable substitutions, or URLs while
validating content.

### 5.2 Archive safety

A conforming validator MUST reject:

- absolute, non-clean, backslash-containing, or traversal paths;
- duplicate paths and Unicode case-folding collisions;
- device nodes, FIFOs, sockets, and unknown tar entry types;
- set-id or sticky bits;
- group- or world-writable regular files;
- symlinks whose lexical target escapes the content root;
- hardlinks to an absent, later, or non-regular entry;
- content exceeding implementation-declared limits.

The reference validator limits JSON documents to 4 MiB, JSON nesting to 100
levels, JSON object membership to 1,024 members per object and 100,000 per
document, content entries to 100,000, and expanded regular-file content to 4
GiB. OCI indexes contain at most 1,000 descriptors referencing at most 1 GiB
in aggregate. It retains at most 256 MiB of JSON metadata while validating an
artifact.

### 5.3 JSON profile

All normative JSON documents:

- MUST be UTF-8;
- MUST contain one JSON value followed only by whitespace;
- MUST reject duplicate object names;
- MUST reject unknown properties;
- MUST conform to [JSON Schema draft 2020-12][json-schema-2020-12];
- MUST NOT contain non-finite numbers;
- SHOULD avoid numbers where an exact string or integer is possible.

Machine-readable schemas are published alongside this specification.

## 6. Identity

### 6.1 Canonical JSON

Manifest identities use the [RFC 8785 JSON Canonicalization Scheme
(JCS)][rfc8785]:

1. parse with the strict JSON profile;
2. canonicalize the parsed value with JCS;
3. compute SHA-256 over the canonical UTF-8 bytes;
4. encode as lowercase `sha256:<hex>`.

Whitespace and object member order therefore do not change a document's
identity. Duplicate names are rejected before canonicalization.

### 6.2 Files

A regular file digest is SHA-256 over its exact bytes. An inventory records
type, mode, numeric owner, size, digest or link target, and component.

### 6.3 Tool variants

To compute `variantDigest`, take the complete variant object as represented in
the tool manifest, replace only the value of its `variantDigest` member with the
empty string, canonicalize with JCS, and hash the canonical bytes. This
identity commits to platform, install root, entrypoint, runtime requirements,
file inventory, dependencies, SBOM, provenance descriptors, and the presence
of optional members.

### 6.4 Tool sets

The digest in a suite `toolSets` reference is the JCS digest of the complete
tool-set lock. A lock is an exact result, not a version constraint or resolver
input.

## 7. Suite manifest

`agentsuite.json` is the graph root. It contains:

- suite name;
- one or more digest-bound agent references;
- one digest-bound tool catalog;
- one or more per-agent, per-platform tool-set locks;
- one or more digest-bound build profiles;
- capabilities derived from tool declarations;
- optional non-critical extensions.

Every reference MUST resolve within the same content layer and MUST match the
referenced document's canonical digest and identity.

`capabilities` MUST exactly equal the sorted, duplicate-free capabilities
derived from tool declarations:

- bundled variants imply `bundled-stdio-mcp`;
- remote streamable-HTTP declarations imply
  `remote-streamable-http-mcp`;
- isolated declarations imply `isolated-tool-sandbox`, but version 1
  validation still fails because that capability is reserved.

Unknown critical extensions MUST be rejected. Non-critical extensions MAY be
retained or ignored.

## 8. Agents

An agent manifest defines:

- a stable identifier and optional description;
- digest-bound instruction bytes;
- model protocol and model identifier;
- environment names for endpoints and secret references, never values;
- exact tool requirements;
- bounded references to other suite agents;
- optional extensions.

Every tool requirement MUST include exact `id`, semantic `version`, and
`executionMode`. Version ranges and floating tags are invalid in a packaged
suite.

For every agent, the suite MUST contain at least one platform tool-set lock,
including an empty lock for an agent that uses no tools. This makes the target
platform and build profile explicit.

Agent invocation references form a closed graph: every target MUST be another
agent in the suite. Coordination protocol and runtime scheduling are outside
version 1.

## 9. Tools

### 9.1 Tool catalog

The tool catalog is the closed, suite-level index of available tool manifests.
Each entry MUST bind one exact tool identifier and semantic version to a
content path and canonical manifest digest. Tool identities and versions MUST
be unique within the catalog. Every tool referenced by an agent or tool-set
lock MUST appear in the catalog.

Example:

```json
{
  "schemaVersion": "1.0.0-draft",
  "mediaType": "application/vnd.agentsuite.tool-catalog.v1+json",
  "tools": [
    {
      "id": "filesystem",
      "version": "1.2.3",
      "path": "tools/filesystem/1.2.3/tool.json",
      "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    },
    {
      "id": "source-control",
      "version": "2.0.0",
      "path": "tools/source-control/2.0.0/tool.json",
      "digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  ]
}
```

Each `digest` is the [RFC 8785][rfc8785] canonical digest of the referenced
tool manifest, not the digest of its original whitespace or object-member
ordering.

### 9.2 Provider and operations

A tool manifest separates the provider process or endpoint from the model-
visible operations it offers. Version 1 providers use MCP and declare one or
more operations with digest-bound input schemas and optional output schemas.

The runtime MUST expose only declared operations to the model. The presence of
other executables or protocol methods in the filesystem does not authorize
their use as model tools.

### 9.3 Execution modes

Exactly one provider arm is allowed.

#### `in-agent-sandbox`

The tool has one or more exact platform variants. The selected variant is
copied into the derived Agent Sandbox Image and executes in the same sandbox
as the agent harness.

All such tools share the sandbox's effective:

- user and group identity;
- process namespace;
- filesystem view;
- writable mounts;
- network policy;
- secret delivery boundary.

Per-tool policy declarations are build inputs whose union constrains the
sandbox. They are not independent security boundaries. A requirement for
distinct privilege, network, secret, or filesystem isolation cannot be met by
this mode.

#### `remote-mcp`

The tool uses MCP streamable HTTP. URLs and credential-bearing headers MUST be
environment or secret references. Authorization, cookie, and API-key header
values MUST NOT be literal content.

#### `isolated-tool-sandbox`

The schema shape is reserved so future suites can identify intent. Version 1
consumers MUST reject every lock requesting this mode. No interoperability or
security claim may be made from the reserved shape.

### 9.4 Bundled variants

A bundled variant includes:

- exact platform;
- absolute `installRoot`;
- payload root inside suite content;
- absolute runtime entrypoint;
- fixed arguments;
- search paths;
- named environment and secret inputs;
- writable mount paths;
- outbound network requirements;
- ABI, ABI version, CPU baseline, and required base paths;
- complete file inventory;
- exact bundle dependencies;
- optional SBOM and provenance descriptors.

Payloads are not assumed relocatable. A Homebrew-style payload may require an
absolute root such as `/home/linuxbrew/.linuxbrew`.

The declared entrypoint MUST lie below `installRoot`. Payload inventory paths
MUST lie below `payloadRoot`. Every regular payload file MUST match its size,
mode, owner, and digest. Payload files MUST be root-owned and MUST NOT be group
or world writable.

Two selected bundles may overlap at the same destination path only when the
resulting entries are identical in type, bytes, mode, ownership, and link
target. All other overlaps are errors.

Bundle dependencies are exact digest-bound bundle identities. They do not
cause network resolution or installer execution.

## 10. Tool-set locks

A tool-set lock is scoped to one agent, one exact platform, and one build
profile. For every agent requirement it contains exactly one locked record:

```json
{
  "id": "filesystem",
  "version": "1.2.3",
  "manifestDigest": "sha256:...",
  "variantDigest": "sha256:...",
  "executionMode": "in-agent-sandbox"
}
```

The lock MUST contain no ambient or undeclared tool. For bundled tools,
`variantDigest` MUST select exactly one matching platform variant. For remote
tools, `variantDigest` MUST be absent. Isolated locks MUST be rejected.

The same immutable tool and variant may appear in several agent locks.

## 11. Build profiles

A build profile pins, per platform:

- one OCI runtime-base image manifest descriptor;
- one OCI harness image manifest descriptor;
- one positive source epoch.

Descriptors MUST identify OCI image manifests by digest and size. Tags are not
part of the profile.

Every tool-set platform MUST have exactly one matching runtime-base and harness
entry in its selected profile.

OCI platform fields alone do not establish native compatibility. Producers
MUST validate the combined final filesystem against each bundled tool's ABI,
dynamic loader, shared library, interpreter, CA bundle, NSS, shell, required
path, and CPU-baseline requirements.

## 12. Agent Sandbox Image construction

Construction MUST be a pure, offline file-composition operation:

1. resolve the suite, agent, platform, lock, and build profile by digest;
2. materialize the pinned runtime-base filesystem;
3. compose the pinned harness filesystem;
4. copy each selected tool payload to its declared install root;
5. reject non-identical destination collisions;
6. install agent instructions and immutable runtime metadata;
7. create no writable content paths;
8. embed the sandbox binding and complete final inventory;
9. emit the OCI image and, for multiple platforms, an OCI image index.

The builder MUST NOT execute package managers, installers, lifecycle scripts,
or arbitrary suite content. It MUST NOT access the network.

The runnable default SHOULD be a non-root numeric user. Runtime configuration
MUST add no Linux capabilities and MUST request `no_new_privs`. Writable paths
MUST be empty runtime-provided mounts, not mutable files baked into the image.

Restricting `PATH` is useful for discoverability but is not process
confinement. Runtime conformance MUST separately test the intended execution
boundary.

## 13. Sandbox binding

Each platform image MUST embed a binding record at:

```text
/.agentsuite/binding.json
```

The image config MUST contain the label:

```text
org.agentsuite.binding.digest=sha256:<JCS digest of binding.json>
```

The binding commits to:

- AgentSuite OCI manifest digest;
- agent identifier;
- exact platform;
- build-profile identifier;
- tool-set descriptor;
- final image inventory descriptor.

For a multi-platform image index, each selected platform manifest has its own
binding. An OCI `subject` relationship alone is not an authenticity proof.
Producers SHOULD publish signed attestations or OCI referrers binding the suite
manifest, build inputs, and resulting image manifests.

## 14. Distribution

Registries are used through [OCI Distribution Specification
v1.1.1][oci-distribution-spec] operations.
Consumers MUST support digest pulls. Producers SHOULD push all blobs before
publishing the referencing manifest.

Tags are discovery names only. A deployment, lock, attestation, or sandbox
binding that requires immutable identity MUST use a digest.

Clients MUST verify every received descriptor before parsing or extracting its
content. Registry transport security and authentication are deployment
concerns; credentials are never AgentSuite content.

## 15. Conformance

### 15.1 Suite validator

A conforming suite validator asserts:

| ID | Requirement |
|---|---|
| `AS-OCI-001` | OCI layout, index, manifest, descriptors, config, and one content layer are valid. |
| `AS-JSON-001` | Normative JSON is strict, duplicate-free, closed, and schema-valid. |
| `AS-PATH-001` | Archive paths, links, entry types, modes, collisions, and limits are safe. |
| `AS-ID-001` | Every JSON, file, variant, and lock identity matches its normative algorithm. |
| `AS-GRAPH-001` | Agent, tool, build-profile, invocation, and lock references form a closed graph. |
| `AS-LOCK-001` | Every agent/platform lock is exact and contains neither missing nor ambient tools. |
| `AS-TOOL-001` | Bundled variants match content inventory and exact platforms. |
| `AS-SECRET-001` | Content contains references and names, not credential-shaped literal values. |
| `AS-CAP-001` | Declared capabilities equal derived capabilities. |
| `AS-V1-001` | Deferred and reserved version 1 features are rejected. |

A suite validator MUST accept either an extracted content directory or an OCI
image-layout directory. Validation MUST operate offline after the referenced
blobs are present and MUST NOT require a runtime or orchestration control
plane.

### 15.2 Sandbox image validator

A conforming sandbox image validator asserts:

| ID | Requirement |
|---|---|
| `ASI-BIND-001` | The label, embedded binding, binding digest, and selected image platform agree. |
| `ASI-SRC-001` | Suite, agent, lock, and build inputs match binding identities. |
| `ASI-FS-001` | Final inventory, ownership, modes, links, collision rules, and writable-path rules hold. |
| `ASI-ABI-001` | Native loaders, libraries, interpreters, CPU baseline, and required base paths resolve in the final filesystem. |
| `ASI-RUN-001` | Image user, entrypoint, capabilities, and `no_new_privs` contract are valid. |

### 15.3 Runtime conformance

A conforming runtime test asserts:

| ID | Requirement |
|---|---|
| `ASR-OPS-001` | The model can invoke only declared provider operations. |
| `ASR-FS-001` | Immutable image paths remain non-writable and declared writable mounts start empty. |
| `ASR-NET-001` | Effective egress is no broader than the union required by the locked tools and model endpoint. |
| `ASR-SEC-001` | Secret values are injected only at runtime and are not persisted into image or suite content. |
| `ASR-PROC-001` | The process executes non-root with no added capabilities and `no_new_privs`. |

Passing suite validation does not imply sandbox-image or runtime conformance.

## 16. Errors

Validators SHOULD classify failures into stable categories:

- `oci`: envelope, descriptor, blob, config, or layer failure;
- `json`: syntax, duplicate key, unknown property, depth, or schema failure;
- `path`: unsafe archive or content path;
- `digest`: canonical identity or file digest mismatch;
- `graph`: missing, duplicate, stale, or ambient reference;
- `platform`: unsupported or non-exact platform;
- `tool`: invalid provider, operation, variant, inventory, or execution mode;
- `build`: invalid pinned base, harness, or compatibility contract;
- `security`: credentials, unsafe modes, injection variables, or reserved
  capabilities.

A validator MUST fail closed. It MUST NOT report conformance after silently
dropping an unknown field, unsupported critical extension, unavailable tool,
or invalid platform.

## 17. Security considerations

AgentSuite content is untrusted input even when obtained from an authenticated
registry. Digest verification, strict parsing, archive safety, and bounded
resource use precede semantic processing.

Signing a suite establishes publisher intent over immutable bytes; it does not
prove that a derived image used those bytes or that a runtime enforces the
declared boundary. Build provenance and runtime policy remain separate claims.

Same-sandbox tools are mutually exposed through their shared process and
filesystem environment. A malicious tool can attempt to inspect another
tool's files, environment, sockets, or runtime credentials. Deployments that
require separation MUST wait for a future isolated-tool-sandbox profile or use
separate agent sandboxes.

SBOM and provenance descriptors are evidence references, not trust decisions.
Consumers choose trusted issuers and policies outside this specification.

## Appendix A. Version 1 exclusions

Version 1 does not specify:

- session transcripts, checkpoints, snapshots, or mutable memory;
- plaintext credentials or resolved secret values;
- dependency solving inside a packaged suite;
- generic shell tools, OpenAPI tools, or arbitrary HTTP connectors;
- Windows, macOS, or non-`amd64`/`arm64` platforms;
- a usable isolated tool sandbox. Its declaration shape is reserved and MUST
  be rejected by version 1 consumers;
- a new OCI Distribution endpoint or registry authentication mechanism;
- byte-identical gzip output across arbitrary compressor implementations.

## 18. Normative references

- [BCP 14: Requirement-level key words][bcp14]
- [OCI Image Format Specification v1.1.1][oci-image-spec]
- [OCI Image Layout Specification v1.1.1][oci-image-layout]
- [OCI artifact and empty-descriptor guidance v1.1.1][oci-artifact-guidance]
- [OCI Distribution Specification v1.1.1][oci-distribution-spec]
- [RFC 8785: JSON Canonicalization Scheme][rfc8785]
- [JSON Schema draft 2020-12][json-schema-2020-12]

## 19. Informative references

- [OCI Runtime Specification][oci-runtime-spec]
- [Helm: Use OCI-based registries][helm-oci]
- [AgentKit][agentkit]
- [Dalec Homebrew][dalec-homebrew]
- [AgentSessions][agentsessions]

[bcp14]: https://www.rfc-editor.org/info/bcp14
[oci-image-spec]: https://github.com/opencontainers/image-spec/tree/v1.1.1
[oci-image-layout]: https://github.com/opencontainers/image-spec/blob/v1.1.1/image-layout.md
[oci-artifact-guidance]: https://github.com/opencontainers/image-spec/blob/v1.1.1/manifest.md#guidelines-for-artifact-usage
[oci-distribution-spec]: https://github.com/opencontainers/distribution-spec/tree/v1.1.1
[oci-runtime-spec]: https://github.com/opencontainers/runtime-spec
[rfc8785]: https://www.rfc-editor.org/rfc/rfc8785
[json-schema-2020-12]: https://json-schema.org/draft/2020-12
[helm-oci]: https://helm.sh/docs/topics/registries/
[agentkit]: https://github.com/sozercan/agentkit
[dalec-homebrew]: https://github.com/sozercan/dalec-homebrew
[agentsessions]: https://github.com/aramase/agentsessions
