# Author and publish a one-member native-Orka suite

A workspace is editable source. Its prepared AgentSuite is an immutable OCI
artifact, identified by its manifest digest. The native-Orka execution profile
requires no member images; authoring and publication do not contact Kubernetes,
run a model or invoke Docker/buildx.

**Supported:** offline creation, preparation and publication of one tool-free
member with an exact OpenAI-compatible model. Workspace evaluation and
registry-reference lift are not available. Existing `agent.yaml` creation,
evaluation and lift remain separate and unchanged.

## Create

```sh
mkdir my-agents && cd my-agents
git init
printf '%s\n' 'Answer briefly and in plain text.' > instructions.md

kmx agent create greeter --workspace . \
  --model qwen-3.5-2b --instructions ./instructions.md
```

`--instructions` names a file, not inline text. Its nonblank bytes are copied
unchanged into the member. `--description` is optional and single-line. Instructions use the native block-text
policy: LF and tabs are allowed; CR and other control characters are refused,
not normalized. This mode refuses cluster,
Provider, runtime, output, tools and Task flags, rather than ignoring them.
Creation refuses existing workspace/member directories, including PortableAgent
bundles; it does not overwrite or migrate them.

The source consists of `workspace.yaml`, `agents/greeter/member.yaml`, its
member-relative instruction file, workspace-root `eval/*.yaml`, and a pinned
build profile. The member's model
is exact; there is no deployment-time model substitution. Edit source values,
instructions and authored cases, not computed digest strings. The example case
is public arithmetic using existing evaluation-case syntax, not proof that your
chosen agent/model answers it.

Tools, skills, delegation, additional members and unsupported source fields are
refused. Paths must stay within the workspace, without symlink traversal. Source
reads are bounded, and credential-shaped content is refused.

## Publish

```sh
kmx suite push --workspace . ghcr.io/me/my-agents:v1
```

The command resolves an allowlisted snapshot into the existing AgentSuite format,
validates it, prepares a private local OCI layout under KMX state, then copies
that exact descriptor graph to the registry. It does not re-archive the prepared
artifact during publication. Output includes the suite digest, a digest-pinned
registry reference and `Required member images: []`.

The content includes exact member instructions and authored case files under
`metadata/kmx/`, plus a versioned native execution declaration. Cases are published
content: use deliberately shareable inputs. Git internals, local receipts,
Provider bindings, promotion policies and unrelated workspace files are not
packaged. Changes to model, instructions or cases produce a different suite
identity; old evidence cannot qualify the changed content.

For example, the authored `eval/example.yaml` becomes:

```text
metadata/kmx/native-orka.json
metadata/kmx/eval/example.yaml
```

`native-orka.json` inventories the member, renderer version, required images,
and every packaged case path and digest. These are KMX native-Orka supplemental
files in the OCI content layer, not public AgentSuite record types.

The OCI manifest digest binds all the content-layer bytes, including cases.
The canonical `agentsuite.json` graph digest alone does not bind supplemental
metadata. No outer package envelope is used. The public format still
requires a composition and pinned build-profile records; this tool-free native
profile emits a composition with no ToolProviders. Those profile records
are declared image-build inputs, not dependencies fetched or executed by native
Orka placement, and do not imply sandbox-image conformance.

Authentication, `--plain-http` and non-atomic tag replacement with `--force` use
the same [registry policy](kmx.md#registry-authentication) as existing suite push.
An unchanged prepared suite retains its digest through publication and pull.
An existing different registry tag requires `--force`.

Registry publication also follows the shared [unsigned provenance policy](kmx.md#suite-provenance):
a referrer is attached by default before moving the tag, `--provenance=false` opts
out, and `--require-provenance` makes an attachment failure leave the tag unchanged.
The source commit/path is recorded only when the frozen authored inputs match
HEAD; generated cache files and unrelated files are excluded from that comparison.
Source commit detection requires Git 2.40+ (`check-attr --source`) to inspect HEAD's
rewriting attributes without trusting live overrides. Older Git, dirty or non-Git
source still publishes a source-less claim and explains why.
Unsigned provenance is informational, not evaluation or promotion evidence.

For offline transfer, the local target's parent directory must exist:

```sh
kmx suite push --workspace . --to-layout ../suite-layout my-agents:v1
kmx suite pull my-agents:v1 --from-layout ../suite-layout --output ../extracted
kmx suite validate ../extracted
```

Local layouts receive no provenance. `--plain-http`, `--force`, explicit
`--provenance` and `--require-provenance` are refused with `--to-layout`.
Local layout publication excludes competing KMX writers before opening the target
index, including extracted-directory pushes. A competing invocation refuses;
references from successful publications remain intact. An interrupted writer can
leave a sibling lock directory: remove it only after verifying the publisher has
stopped. Registry tag preflight remains separately non-atomic.
Existing extracted-directory transfer remains `suite push DIRECTORY REFERENCE`;
`--workspace` instead takes only one positional destination reference.

Internally, verified packaged native inputs can be loaded without consulting live
source or the preparation record, and publication can require an expected OCI
manifest digest. This identity check alone is not evaluation evidence or release
authorization; the installed CLI still has no workspace evaluation or registry
release lift.

Preparation state is outside authored source, under the KMX state directory
(`KMX_HOME` overrides it). Cache corruption refuses reuse. Keep this state private;
it is not a receipt, evaluation pass or promotion authorization.
