# AgentSuite

AgentSuite is a portable, content-addressed definition of one or more related
agents and the exact ToolProviders used to construct their runnable sandboxes.
Suites are distributed as OCI artifacts and contain a closed graph of agent,
ToolProvider, composition, and build-profile manifests.

This package provides the format's public Go model within Kaimahi's root Go
module; it does not have a separate module or release version:

- object types for suites, agents, ToolProviders, callable Tools, compositions,
  and build profiles;
- media types and specification version constants;
- strict JSON decoding with closed-field and duplicate-key enforcement;
- JCS canonical encoding and digest calculation;
- offline validation of extracted content and OCI image-layout directories;
- optional agent-bound evaluation indexes and ordered authored cases, with
  strict digest/ownership validation and KMX-compatible assertion fields;
- closed JSON Schema 2020-12 documents in [`schema/`](schema/).

The normative format and conformance requirements are defined in
[`spec.md`](spec.md).

## Go package

```go
import "github.com/kaimahi-agents/kaimahi/agentsuite"

data, err := agentsuite.Marshal(suite)
if err != nil {
    return err
}

digest, err := agentsuite.Digest(data)
```

`Unmarshal` should be used for AgentSuite JSON rather than `encoding/json`
directly when strict format enforcement is required.

`ValidatePath` validates either an extracted AgentSuite content directory or
an OCI image-layout directory and returns a conformance report. Consumers that
read loose manifests should use `ValidateDirectory` to validate extracted
content explicitly, without OCI layout auto-detection.

Producers SHOULD embed authored evaluations under `evals/<agent>/`, bound by
the agent manifest and JCS digests. Suites without evaluations remain conformant.
Validation never executes assertions or proves a pass. Results stay external,
optionally published as attestations or OCI referrers bound to the suite manifest
digest; KMX's private authoring and receipt plumbing are unchanged. See
[agent evaluations](spec.md#82-agent-evaluations).

Builders, packers, registry transfer, sandbox-image construction, and
builder-owned image metadata are implementation concerns and are not part of
this package.
