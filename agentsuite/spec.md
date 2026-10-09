# AgentSuite Artifact Specification

**Status:** Draft
**Version:** `1.0.0-draft`
**Last updated:** October 7, 2026

## Abstract

AgentSuite is a portable, content-addressed definition of one or more related
agents and the exact tool providers from which their runnable sandboxes are derived. An
AgentSuite is distributed as an artifact conforming to the
[OCI Image Format Specification][oci-image-spec]. Each runnable agent and
platform has an exact composition manifest and a pinned build profile. A
producer combines those records by file composition to create an Agent Sandbox
Image.

This specification defines the OCI envelope, content layout, closed JSON
schemas, canonical identities, tool provider bundle model, sandbox-image binding,
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

### 1.1 Tool terminology

The hierarchy is **AgentSuite → ToolProvider → Tool**:

- A **ToolProvider** is the versioned contract and its bundled implementations
  or remote declaration. It is the unit of cataloging, variant selection,
  composition, sandbox construction, and runtime binding.
- A **Tool** is one named, model-callable function with input/output schemas
  and declared effects. For MCP providers, this is an MCP tool, not the server
  that exposes it.
- A **ToolProvider variant** is a platform-specific filesystem bundle. A
  **ToolProvider composition** selects exact build inputs for a
  **ToolProvider Sandbox Image**. These names do not include a protocol name.

The terminology is protocol-neutral. This draft's executable contract supports MCP
only; CLI binaries are implementation details behind an MCP adapter. Naming a
provider does not imply that its implementation is a network server or that it
has already been deployed. An inference provider is a separate concept.

## 2. Scope

This draft specifies:

- an AgentSuite definition artifact carried by an OCI image manifest;
- one strict, closed content graph containing agents, tool providers, compositions, and
  build profiles;
- bundled stdio MCP providers statically installed into an agent sandbox;
- remote Streamable HTTP tool provider authoring and cataloging without agent
  composition or runtime binding;
- exact Linux platform selection;
- pinned runtime-base and harness images;
- deterministic, network-free Agent Sandbox Image construction;
- an Agent binding or exact ToolProvider composition embedded in each derived sandbox
  image;
- suite, sandbox-image, and runtime conformance classes.

## 3. Artifact model

An **AgentSuite Artifact** is an immutable definition. It is not directly
runnable.

An **Agent Sandbox Image** is a runnable OCI image derived for exactly one:

- AgentSuite manifest digest;
- agent identifier;
- platform;
- build profile;
- composition manifest.

A tool provider may be referenced by several agents. Each derived Agent Sandbox Image
materializes that provider's exact platform bundle independently. Registry
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
| [ToolProvider catalog](#91-toolprovider-catalog) | `application/vnd.agentsuite.tool.provider.catalog.v1+json` |
| [ToolProvider manifest](#9-tool-providers-and-tools) | `application/vnd.agentsuite.tool.provider.v1+json` |
| [Composition manifest](#10-composition-manifests) | `application/vnd.agentsuite.composition.v1+json` |
| [ToolProvider composition](#101-toolprovider-compositions) | `application/vnd.agentsuite.tool.provider.composition.v1+json` |
| [Build profile](#11-build-profiles) | `application/vnd.agentsuite.build.profile.v1+json` |
| [Agent sandbox binding](#13-agent-sandbox-binding) | `application/vnd.agentsuite.sandbox.binding.v1+json` |

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

The single-layer rule is intentional for this draft: it gives a suite one
self-contained validation boundary. Later revisions may profile multiple
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
├── tool-providers/
│   ├── catalog.json
│   └── <provider>/
│       └── <version>/
│           ├── tool-provider.json
│           └── <platform>/
│               └── ...
├── compositions/
│   └── <agent>-<os>-<architecture>.json
├── tool-provider-compositions/
│   └── <provider>-<os>-<architecture>.json
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
- a gzip modification time other than zero;
- tar entries whose modification time is not the Unix epoch or whose access or
  change time is set;
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

### 6.3 ToolProvider variants

To compute `variantDigest`, take the complete variant object as represented in
the tool provider manifest, replace only the value of its `variantDigest` member with the
empty string, canonicalize with JCS, and hash the canonical bytes. This
identity commits to platform, install root, entrypoint, runtime requirements,
file inventory, dependencies, SBOM, provenance descriptors, and the presence
of optional members.

### 6.4 Compositions

An agent composition resolves:

```text
agent + platform + build profile + zero or more tool providers
```

A ToolProvider composition resolves:

```text
tool provider manifest + platform variant + build profile
```

The digest in a suite `compositions` reference is the JCS digest of the
complete composition manifest. The manifest is an exact, resolved result, not
a version constraint or resolver input.

The digest in a suite `toolProviderCompositions` reference is the JCS digest of the
complete ToolProvider composition manifest.

## 7. Suite manifest

`agentsuite.json` is the graph root. It contains:

- suite name;
- one or more digest-bound agent references;
- one digest-bound tool provider catalog (`toolProviderCatalog`);
- one or more per-agent, per-platform compositions;
- zero or more per-provider, per-platform ToolProvider compositions (`toolProviderCompositions`);
- one or more digest-bound build profiles;
- capabilities derived from tool provider declarations;
- optional non-critical extensions.

Every reference MUST resolve within the same content layer and MUST match the
referenced document's canonical digest and identity.

`capabilities` MUST exactly equal the sorted, duplicate-free capabilities
derived from tool provider declarations:

- bundled variants imply `bundled-stdio-mcp`.

Unknown critical extensions MUST be rejected. Non-critical extensions MAY be
retained or ignored.

## 8. Agents

An agent manifest defines:

- a stable identifier and optional description;
- digest-bound instruction bytes;
- model protocol and model identifier;
- environment names for endpoints and secret references, never values;
- exact tool provider requirements (`toolProviders`);
- bounded references to other suite agents;
- optional extensions.

Every tool provider requirement MUST include exact `id`, semantic `version`, and
`executionMode`. Version ranges and floating tags are invalid in a packaged
suite.

This draft defines one execution mode:

| Value | Meaning |
|---|---|
| `shared-sandbox` | The tool provider implementation is bundled into the Agent Sandbox Image and executes in the same sandbox as the agent harness. The provider and harness may be separate processes, but no isolation or security boundary separates them. |

The `executionMode` value MUST be `shared-sandbox`. Other values are invalid
in this draft. Later specification revisions MAY define additional execution
modes; implementations of this draft MUST NOT infer or accept them.

For every agent, the suite MUST contain at least one composition, including an
empty composition for an agent that uses no tool providers. This makes the target
platform and build profile explicit.

### 8.1 Agent invocation relationships

The optional `invokes` element declares which other agents an agent is
permitted to invoke. It is an array of invocation relationship objects:

| Field | Requirement |
|---|---|
| `agent` | REQUIRED. Exact identifier of the target agent. The target MUST be another agent in the same suite. |
| `maxConcurrent` | REQUIRED. Positive integer limiting concurrent invocations of this target by the declaring agent. |
| `maxDepth` | REQUIRED. Positive integer limiting the invocation chain initiated through this edge. The directly invoked target is depth 1. |

Each target agent MUST occur at most once in an agent's `invokes` array.
Invocation relationships form a closed directed graph. An edge grants
permission only from the declaring source agent to the named target:

- it does not grant a reverse edge;
- it does not grant transitive permission to other agents;
- an omitted or empty `invokes` array grants no permission to invoke another
  suite agent.

Runtimes MUST reject an invocation that has no declared edge or exceeds either
declared bound. Coordination protocol and scheduling policy within those bounds
are outside this draft.

For example, this agent-manifest fragment permits `coordinator` to invoke
`writer` and `reviewer`:

```json
{
  "id": "coordinator",
  "invokes": [
    {
      "agent": "writer",
      "maxConcurrent": 1,
      "maxDepth": 1
    },
    {
      "agent": "reviewer",
      "maxConcurrent": 1,
      "maxDepth": 1
    }
  ]
}
```

The `writer` and `reviewer` manifests have empty `invokes` arrays. Invocation
permission is directional: the coordinator's edges do not grant either worker
permission to invoke the coordinator or each other.

The complete conformant example is checked in at
[`internal/kmx/agentsuite/testdata/coordinator-workers/`](../internal/kmx/agentsuite/testdata/coordinator-workers/).

## 9. Tool providers and tools

A ToolProvider is an immutable, versioned contract exposing one or more tools,
with bundled platform variants, a remote declaration, or both. It is not merely
an executable filename, one callable tool, or an installed runtime process.

A tool provider manifest defines:

| Field | Meaning |
|---|---|
| `id` | Stable suite-local tool provider identifier. |
| `version` | Exact semantic version of the provider contract and execution definition. |
| `retained` | Optional explicit `true` marker permitting a ToolProvider with no inward reference to remain in the catalog. |
| `protocol` | Protocol exposed by the provider; `mcp` in this draft. |
| `revision` | Explicit protocol revision. |
| `tools` | Complete set of named, model-callable tools authorized by the provider contract. |
| `variants` | One or more bundled, platform-specific implementations for `shared-sandbox` execution. |
| `remote` | A remote Streamable HTTP declaration containing references and connection behavior, but no endpoint or credential values. |
| `extensions` | Optional non-critical extension records. |

At least one of `variants` or `remote` MUST be present. A remotely callable
ToolProvider MAY also contain variants used to construct its deployable ToolProvider Sandbox
Image. When present, `variants` MUST contain at least one implementation. The combination of
`id` and `version` names the provider contract; the JCS digest of the complete
manifest identifies its exact immutable definition. Two manifests with the
same `id` and `version` but different digests are conflicting definitions and
MUST NOT coexist in one suite.

The provider contract and bundled variants serve different purposes:

- `tools` defines what the model is authorized to discover and
  call;
- the selected variant defines the exact platform implementation available to
  the harness;
- the remote declaration defines immutable connection requirements without
  resolving deployment values;
- a composition binds an agent requirement to the exact manifest and one exact
  platform implementation.

The complete machine-readable shape is
[`tool-provider.schema.json`](../internal/kmx/agentsuite/schema/tool-provider.schema.json).

### 9.1 ToolProvider catalog

The tool provider catalog is the closed, suite-level index of available provider manifests.
Each entry MUST bind one exact provider identifier and semantic version to a
content path and canonical manifest digest. ToolProvider identities and versions MUST
be unique within the catalog. Every provider referenced by an agent or composition
MUST appear in the catalog.

Every catalog ToolProvider MUST have at least one inward reference from an agent provider
requirement, a ToolProvider composition, or an exact bundle dependency. A ToolProvider with no
inward reference is invalid unless its manifest explicitly declares
`"retained": true`. The marker records intentional catalog retention; it does
not authorize agent use, create a composition, or add a runtime binding.

ToolProvider topology is inward-only: providers do not name agents or compositions.

Example:

```json
{
  "schemaVersion": "1.0.0-draft",
  "mediaType": "application/vnd.agentsuite.tool.provider.catalog.v1+json",
  "toolProviders": [
    {
      "id": "datetime",
      "version": "1.0.0",
      "path": "tool-providers/datetime/1.0.0/tool-provider.json",
      "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    }
  ]
}
```

Each `digest` is the [RFC 8785][rfc8785] canonical digest of the referenced
tool provider manifest, not the digest of its original whitespace or object-member
ordering.

### 9.2 Callable tools

A ToolProvider manifest separates the provider process or endpoint from the
model-callable tools it offers. Each entry in `tools` defines `name`, optional
`description`, digest-bound `inputSchema`, optional `outputSchema`, and optional
`effects`. Tool names MUST be unique within a provider; different providers MAY
expose the same tool name. A tool is identified in the context of its exact
provider contract, not by a separate provider-level version or platform variant.

The runtime MUST expose only declared tools to the model. The presence of
other executables or protocol methods in the filesystem does not authorize
their use as model tools.

### 9.3 Execution modes

This draft supports only the `shared-sandbox` mode defined in [Section 8](#8-agents).

The bundled tool provider has one or more exact platform variants. The selected variant is
copied into the derived Agent Sandbox Image and executes in the same sandbox
as the agent harness.

A cataloged remote ToolProvider is not selectable through its remote declaration by an agent or composition in this
draft. Remote execution mode and runtime binding are outside this section.

All such bundled providers share the sandbox's effective:

- user and group identity;
- process namespace;
- filesystem view;
- writable mounts;
- network policy;
- secret delivery boundary.

Per-provider policy declarations are build inputs whose union constrains the
sandbox. They are not independent security boundaries. A requirement for
distinct privilege, network, secret, or filesystem isolation cannot be met by
this mode.

### 9.4 Bundled variants

A bundled variant includes:

- exact platform;
- absolute `installRoot`;
- payload root inside suite content;
- absolute MCP provider entrypoint;
- fixed arguments;
- search paths;
- named environment and secret inputs;
- writable mount paths;
- outbound network requirements;
- ABI, ABI version, CPU baseline, and required base paths;
- complete file inventory;
- exact bundle dependencies;
- optional SBOM and provenance descriptors.

A variant is a complete filesystem bundle, not a single-binary declaration. It
MAY contain any number of executables, shared libraries, interpreters, scripts,
certificates, schemas, licenses, and data files. Every payload entry MUST appear
in the variant's file inventory.

`entrypoint` identifies the executable that launches the MCP provider. It does
not limit the bundle to one executable. The provider MAY invoke other
executables from the same variant, but those executables are implementation
details and are not directly model-callable. Only tools declared in the
provider's `tools` array are exposed to the model.

Payloads are not assumed relocatable. A Homebrew-style payload may require an
absolute root such as `/home/linuxbrew/.linuxbrew`.

The declared entrypoint MUST lie below `installRoot`. Payload inventory paths
MUST lie below `payloadRoot`. Every regular payload file MUST match its size,
mode, owner, and digest. Payload files MUST be root-owned and MUST NOT be group
or world writable.

Two selected bundles may overlap at the same destination path only when the
resulting entries are identical in type, bytes, mode, ownership, and link
target. All other overlaps are errors.

Bundle dependencies MUST identify exact same-platform tool provider variants by digest.
Consumers MUST traverse them recursively, reject cycles, and include their
payloads in collision checks and image construction. Dependencies do not grant
model access or cause network resolution or installer execution.

A variant MUST include private runtimes required by its provider. This draft
does not define standalone runtime bundles.

### 9.5 Remote MCP declarations

A remote ToolProvider's `remote` object defines:

- `streamable-http` transport;
- an identifier naming an unresolved endpoint binding;
- optional HTTP headers whose values come only from named Secret keys;
- one or more unresolved network-destination references;
- positive connect and request timeouts;
- propagated cancellation;
- protocol-managed session behavior.

Endpoint, destination, and Secret references MUST be identifiers, not URLs,
hosts, tokens, cookies, API keys, or header values. Transport-managed headers
MUST NOT be declared. Header names are compared case-insensitively for
duplicates.

The complete fixture is
[`internal/kmx/agentsuite/testdata/remote-mcp/`](../internal/kmx/agentsuite/testdata/remote-mcp/).
Its ToolProvider manifest includes:

```json
{
  "remote": {
    "transport": "streamable-http",
    "endpointRef": "search-mcp-endpoint",
    "headers": [
      {
        "name": "Authorization",
        "secretRef": {
          "name": "search-mcp-auth",
          "key": "token"
        }
      }
    ],
    "network": [
      {
        "destinationRef": "search-mcp-egress"
      }
    ],
    "timeouts": {
      "connectMilliseconds": 5000,
      "requestMilliseconds": 60000
    },
    "cancellation": "propagate",
    "connection": "session-aware"
  }
}
```

These fields contribute to the canonical ToolProvider manifest digest. Cataloging the
provider does not authorize an agent to call its tools and does not resolve any reference.
When the same ToolProvider also declares `variants`, a ToolProvider composition MAY select one
variant for standalone sandbox construction.

### 9.6 CLI-backed providers

A command-line program that is not itself an MCP server is bundled behind an
MCP provider. The provider validates model inputs against the declared
tool schemas and translates authorized tool calls into exact CLI
invocations. The raw command line is not exposed as an ambient tool.

A Kubernetes variant can inventory a `kubernetes-mcp` provider alongside
`kubectl`. An Azure CLI variant can inventory an `azure-mcp` provider, `az`,
its Python runtime, modules, extensions, certificates, and data. Runtime
package or extension installation is prohibited; the complete runtime closure
MUST already be present in the variant or in exact bundle dependencies.

The reference schema tests validate multi-file bundle fixtures for
[`kubectl`](../internal/kmx/agentsuite/testdata/tool-providers/kubectl-tool-provider.json),
[`Azure CLI`](../internal/kmx/agentsuite/testdata/tool-providers/azure-cli-tool-provider.json),
and [`OPA`](../internal/kmx/agentsuite/testdata/tool-providers/opa-tool-provider.json).

### 9.7 OPA writer and reviewer example

The OPA ToolProvider fixture exposes one `evaluate_document` tool backed by an
`opa-mcp` adapter and the OPA executable. The tool accepts a structured
document, Rego policy, and query, and returns an allow decision and violations.

A writer can evaluate its document before handoff. A reviewer can independently
evaluate the same document. Both agents request the same ToolProvider contract
in their `toolProviders` arrays:

```json
{
  "id": "opa",
  "version": "1.0.0",
  "executionMode": "shared-sandbox"
}
```

The fixture contains both a bundled variant and a remote declaration. The
variant can participate in an Agent Sandbox Image or a standalone ToolProvider Sandbox
Image. The remote declaration catalogs the connection contract for a deployed
instance without defining agent binding.

The complete example is:

1. [`opa-tool-provider.json`](../internal/kmx/agentsuite/testdata/tool-providers/opa-tool-provider.json)
   defines the ToolProvider contract, callable tools, and available implementation;
2. [`opa-tool-provider-composition.json`](../internal/kmx/agentsuite/testdata/tool-providers/opa-tool-provider-composition.json)
   selects the exact ToolProvider manifest, variant, platform, and build profile and is
   embedded unchanged in the resulting image.

The repeated hexadecimal digests in these examples are illustrative. A
producer MUST replace them with values computed from the concrete ToolProvider
manifest and composition.

### 9.8 UTC datetime example

The following example is a bundled stdio MCP server that returns the current
UTC time. It does not execute `/bin/date`, depend on distribution userland,
access the network, use secrets, or require writable paths. The executable is
part of the suite payload and is selected by digest.

An agent requests the tool provider by exact identity and execution mode:

```json
{
  "id": "datetime",
  "version": "1.0.0",
  "executionMode": "shared-sandbox"
}
```

The tool provider manifest declares the model-callable tool and the available
platform implementation:

```json
{
  "schemaVersion": "1.0.0-draft",
  "mediaType": "application/vnd.agentsuite.tool.provider.v1+json",
  "id": "datetime",
  "version": "1.0.0",
  "protocol": "mcp",
  "revision": "2025-06-18",
  "tools": [
    {
      "name": "current_time",
      "description": "Return the current UTC time in RFC 3339 format.",
      "inputSchema": {
        "path": "schemas/datetime/current-time-input.json",
        "digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      },
      "outputSchema": {
        "path": "schemas/datetime/current-time-output.json",
        "digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
      },
      "effects": ["reads-system-clock"]
    }
  ],
  "variants": [
    {
      "platform": {
        "os": "linux",
        "architecture": "amd64"
      },
      "variantDigest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
      "installRoot": "/opt/agentsuite/tool-providers/datetime/1.0.0",
      "relocatable": false,
      "payloadRoot": "tool-providers/datetime/1.0.0/linux-amd64",
      "entrypoint": "/opt/agentsuite/tool-providers/datetime/1.0.0/bin/datetime",
      "arguments": [],
      "searchPath": [],
      "environment": [],
      "writablePaths": [],
      "network": [],
      "runtime": {
        "abi": "static",
        "cpuBaseline": "x86-64-v1"
      },
      "files": [
        {
          "path": "bin/datetime",
          "type": "file",
          "mode": 493,
          "uid": 0,
          "gid": 0,
          "size": 123456,
          "digest": "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
          "component": "datetime"
        }
      ],
      "dependencies": []
    }
  ],
  "extensions": []
}
```

The repeated hexadecimal digest values and illustrative file size stand in for
values computed from a concrete artifact. A conforming artifact MUST contain
the referenced schemas and payload bytes and MUST use their actual digests and
size.

Reading the clock makes the tool result runtime-dependent; it does not
make the provider artifact mutable. The manifest, schemas, executable, variant,
and composition remain immutable and digest-bound.

## 10. Composition manifests

A composition manifest is the closed, resolved, digest-pinned composition of
one agent for one exact platform and one build profile. It is not the suite
tool provider catalog and it is not a request for runtime resolution. It MUST NOT be
applied to a different agent.

A composition is a build-time input to Agent Sandbox Image construction. It is
not a runtime actor template, deployment object, or snapshot policy. Runtime
systems MAY derive their own templates from the resulting image, but those
templates and their lifecycle policies are outside this specification.

The suite-level composition selection key is the tuple:

```text
(agent, platform)
```

The composition selects exactly one `buildProfile`. A suite MUST contain exactly
one composition for each supported `(agent, platform)` pair; selecting another
build profile therefore requires replacing that composition and its digest,
rather than adding an ambiguous second composition for the same pair.

Its `toolProviders` array MUST be sorted lexicographically by `id` and then `version`.
For every provider requirement in the named agent manifest, it MUST contain exactly
one resolved provider record. The array MAY be empty when the agent has no provider
requirements:

```json
{
  "schemaVersion": "1.0.0-draft",
  "mediaType": "application/vnd.agentsuite.composition.v1+json",
  "agent": "writer",
  "platform": {
    "os": "linux",
    "architecture": "amd64"
  },
  "buildProfile": "default",
  "toolProviders": [
    {
      "id": "datetime",
      "version": "1.0.0",
      "manifestDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "variantDigest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
      "executionMode": "shared-sandbox"
    }
  ]
}
```

The resolved tool provider fields have these meanings:

| Field | Meaning |
|---|---|
| `id`, `version` | Exact tool provider identity from the agent requirement and catalog. |
| `manifestDigest` | JCS digest of the exact tool provider manifest selected from the catalog. |
| `variantDigest` | JCS digest of the exact bundled variant selected for the composition platform. |
| `executionMode` | `shared-sandbox`, the only execution mode defined by this draft. |

A producer creates a composition without executing suite content:

1. read the named agent's exact tool provider requirements;
2. resolve each `id` and `version` to exactly one tool-provider-catalog entry;
3. verify the catalog digest against the referenced tool provider manifest;
4. verify that the requirement's execution mode is `shared-sandbox`;
5. select exactly one variant whose platform equals the
   composition platform and record its `variantDigest`;
6. verify the selected build profile contains exactly one runtime-base and
   harness descriptor for that platform;
7. sort the resolved provider records by `id` and `version` and compute the
   composition's JCS digest.

The composition MUST contain no ambient, undeclared, missing, duplicate, or
differently versioned tool provider. For bundled providers, `variantDigest` MUST be present
and select exactly one matching platform variant.

Exact dependencies of a bundled variant are traversed from the selected
variant's digest-bound dependency records. They MUST resolve to provider manifests
and same-platform variants in the suite catalog, but they do not become
additional top-level resolved provider records unless the agent also declares them
directly.

An agent with no tool provider requirements still MUST have an empty composition for
each supported platform. The empty composition explicitly binds the agent to a
platform and build profile and prevents ambient runtime tools from becoming
implicitly authorized.

The same immutable tool provider and variant may appear in several compositions.

### 10.1 ToolProvider compositions

A ToolProvider composition is the closed build input for one standalone ToolProvider Sandbox
Image:

```text
ToolProvider manifest
    |
    v
ToolProvider composition
    |
    | build
    v
ToolProvider Sandbox Image
    |-- selected ToolProvider variant and dependencies
    `-- /.agentsuite/tool-provider-composition.json
```

The ToolProvider manifest defines the available implementations. The ToolProvider composition
selects one exact manifest, platform variant, and build profile. Construction
materializes that selection into an image and embeds the exact ToolProvider
composition unchanged.

```json
{
  "schemaVersion": "1.0.0-draft",
  "mediaType": "application/vnd.agentsuite.tool.provider.composition.v1+json",
  "id": "search",
  "version": "1.0.0",
  "manifestDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "platform": {
    "os": "linux",
    "architecture": "amd64"
  },
  "variantDigest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "buildProfile": "default"
}
```

The ToolProvider manifest MUST resolve from the catalog by exact identity and digest.
`variantDigest` MUST select exactly one matching platform variant. The build
profile MUST contain exactly one runtime-base descriptor for that platform.
The variant dependency closure participates in collision checks and image
construction.

A ToolProvider composition does not authorize any agent, resolve a remote endpoint, or
deploy the resulting image.

## 11. Build profiles

A build profile pins, per platform:

- one OCI runtime-base image manifest descriptor;
- one OCI harness image manifest descriptor;
- one positive source epoch.

Descriptors MUST identify OCI image manifests by digest and size. Tags are not
part of the profile.

The platform named by every agent composition MUST have exactly one matching
runtime-base and harness entry in its selected profile. A ToolProvider composition
requires only the matching runtime-base entry.

OCI platform fields alone do not establish native compatibility. Producers
MUST validate the combined final filesystem against each bundled provider's ABI,
dynamic loader, shared library, interpreter, CA bundle, NSS, shell, required
path, and CPU-baseline requirements.

## 12. Agent Sandbox Image construction

Construction MUST be a pure, offline file-composition operation:

1. resolve the suite, agent, platform, composition, and build profile by
   digest;
2. materialize the pinned runtime-base filesystem;
3. compose the pinned harness filesystem;
4. copy selected provider payloads and exact dependency closures to their declared
   install roots;
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

### 12.1 ToolProvider Sandbox Image construction

ToolProvider Sandbox Image construction MUST:

1. resolve the suite, ToolProvider composition, ToolProvider manifest, exact variant, and build
   profile by digest;
2. materialize the pinned runtime-base filesystem;
3. copy the selected variant and exact dependency closure to their declared
   install roots;
4. reject non-identical destination collisions;
5. set the variant entrypoint and fixed arguments as the image process;
6. create no writable content paths;
7. embed the exact ToolProvider composition at
   `/.agentsuite/tool-provider-composition.json`;
8. emit an OCI image or multi-platform image index.

Construction is subject to the same offline, deterministic, non-root, and
no-installer requirements as Agent Sandbox Image construction. It does not
deploy the image or resolve the ToolProvider's remote endpoint, network, or Secret
references.

The image config MUST contain:

```text
org.agentsuite.tool-provider-composition.digest=sha256:<JCS digest of tool-provider-composition.json>
```

The label, embedded composition, and selected image platform MUST agree. The
OCI image manifest commits to the resulting image contents. Producers that
need a verifiable relationship to the complete AgentSuite SHOULD publish a
signed provenance attestation or OCI referrer.

## 13. Agent sandbox binding

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
- composition descriptor;
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

Tags are discovery names only. A deployment, composition, attestation, or
sandbox binding that requires immutable identity MUST use a digest.

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
| `AS-ID-001` | Every content-addressed identity matches its normative algorithm. |
| `AS-GRAPH-001` | All manifest and invocation references form a closed graph. |
| `AS-COMPOSE-001` | Every agent/platform composition exactly resolves its declared tool providers and contains no ambient providers. |
| `AS-PROVIDER-COMPOSE-001` | Every ToolProvider composition resolves one exact same-platform variant, dependency closure, and runtime base. |
| `AS-PROVIDER-001` | Bundled variants match content inventory and exact platforms; remote ToolProviders contain only supported transports and valid references. |
| `AS-SECRET-001` | Content contains references and names, not credential-shaped literal values. |
| `AS-CAP-001` | Declared capabilities equal derived capabilities. |
| `AS-DRAFT-001` | Deferred and reserved features are rejected. |

A suite validator MUST accept either an extracted content directory or an OCI
image-layout directory. Validation MUST operate offline after the referenced
blobs are present and MUST NOT require a runtime or orchestration control
plane.

The CLI validation report counts `toolProviders` and
`toolProviderCompositions`. These counts describe provider manifests and
standalone provider compositions, respectively; they do not count callable tools.

### 15.2 Sandbox image validator

A conforming sandbox image validator asserts:

| ID | Requirement |
|---|---|
| `ASI-BIND-001` | An Agent image's label, embedded binding, binding digest, and selected platform agree. |
| `ASI-PROVIDER-001` | A ToolProvider image's label, embedded ToolProvider composition, composition digest, and selected platform agree. |
| `ASI-SRC-001` | Suite, agent, composition, and build inputs match binding identities. |
| `ASI-FS-001` | Final inventory, ownership, modes, links, collision rules, and writable-path rules hold. |
| `ASI-ABI-001` | Native loaders, libraries, interpreters, CPU baseline, and required base paths resolve in the final filesystem. |
| `ASI-RUN-001` | Image user, entrypoint, capabilities, and `no_new_privs` contract are valid. |

### 15.3 Runtime conformance

A conforming runtime test asserts:

| ID | Requirement |
|---|---|
| `ASR-TOOLS-001` | The model can invoke only the tools declared by its providers. |
| `ASR-FS-001` | Immutable image paths remain non-writable and declared writable mounts start empty. |
| `ASR-NET-001` | Effective egress is no broader than the union required by the resolved tool providers and model endpoint. |
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
- `tool-provider`: invalid provider, callable tool, variant, inventory, or execution mode;
- `build`: invalid pinned base, harness, or compatibility contract;
- `security`: credentials, unsafe modes, injection variables, or reserved
  capabilities.

A validator MUST fail closed. It MUST NOT report conformance after silently
dropping an unknown field, unsupported critical extension, unavailable tool provider,
or invalid platform.

## 17. Security considerations

AgentSuite content is untrusted input even when obtained from an authenticated
registry. Digest verification, strict parsing, archive safety, and bounded
resource use precede semantic processing.

Signing a suite establishes publisher intent over immutable bytes; it does not
prove that a derived image used those bytes or that a runtime enforces the
declared boundary. Build provenance and runtime policy remain separate claims.

Shared-sandbox tool providers are mutually exposed through their shared process and
filesystem environment. A malicious provider can attempt to inspect another
provider's files, environment, sockets, or runtime credentials. Deployments that
require separation MUST use separate agent sandboxes. A future specification
version MAY define additional execution modes with different isolation
properties.

SBOM and provenance descriptors are evidence references, not trust decisions.
Consumers choose trusted issuers and policies outside this specification.

## Appendix A. Draft exclusions

This draft does not specify:

- session transcripts, checkpoints, snapshots, or mutable memory;
- plaintext credentials or resolved secret values;
- dependency solving inside a packaged suite;
- generic shell tools, OpenAPI tools, or arbitrary HTTP connectors;
- agent composition and deploy-time binding for remote MCP providers;
- standalone non-provider bundles for shared runtimes or libraries;
- Windows, macOS, or non-`amd64`/`arm64` platforms;
- additional execution modes or per-provider isolation boundaries;
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
