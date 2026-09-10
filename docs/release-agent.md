# Release agent — retired guide

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. The release workflow is no longer
> the product's front door. Its code remains for existing users; this page
> preserves consequential limits and source references. Start at the
> [documentation index](README.md) for the current direction.

## Existing workflow

[blueprints/release.yaml](../blueprints/release.yaml) declares the retained
workflow; [workflows](workflows.md) documents `kmx workflow` inspection,
policy application and execution. The native runner is in
[internal/kmx/app/workflow_run.go](../internal/kmx/app/workflow_run.go);
checkout connector setup remains in the [Makefile](../Makefile).
This is not an Orka workflow API.

The agent drafts notes and makes small tool requests. Shell/CLI code polls
builds and handles artifacts; a successful narrative from the model is
not evidence that a branch, build or release exists.

## What an approval actually binds

The [committed table](../k8s/plane/upstreams.yaml) declares:

| Call | Policy fields |
|---|---|
| `create_branch` | `owner`, `repo`, `branch`, `from_branch` |
| `actions_run_trigger` | `method`, `owner`, `repo`, `workflow_id`, `ref` |
| `pipelines_write` | `action`, `orgName`, `project`, `pipelineId`, `previewRun` |
| `release_publish` | `owner`, `repo`, `tag` |

Dispatcher selectors matter: permitting a tool name alone can admit more
operations than starting a build. **The nested ADO build ref is unbound**:
`resources.repositories.<name>.refName` is not a declared top-level policy
field. Neither that ref nor nested template parameters are constrained by
the listed build grant. Release prose, target commit and artifact selection
are likewise not in `release_publish`'s digest; do not claim they are approved.

`kmx workflow govern` applies the blueprint's standing bounds to repository
reads and selected ADO pipelines. Those builds are admitted without approval.
The blueprint marks them `bounded` and rejects a simultaneous consequential
posture. Constraints survive in the operator overlay.

## Publication is outside the gateway

[release-publish.sh](../scripts/release-publish.sh) creates a release and
transfers ADO artifacts with the operator's **own `az` and `gh` credentials**.
`release_publish` is an approval declaration, not an upstream MCP tool.
The workflow records approval of the decision; the gateway neither carries
nor meters the transferred bytes and writes no tool-audit row for them.
Review the candidate assets first (`--list` in the script); filename globs
and artifact selection are not a content-security check. Partially successful
builds are accepted unless `STRICT_BUILDS=1` is set.

## Custody and teardown

`kmx credential capture github-release owner/name` and
`kmx credential capture ado <organization>` retain terminal-only capture.
The hosted ADO seam uses an expiring Entra access token, not a PAT; the
plane cannot renew it itself. The driver refreshes it from the operator's
session. Stale kagent discovery can present expiry as missing tools.

GitHub capture cannot prove the token's exact permissions or single-repo
scope. Contents-write permits destructive operations too; the token is not
an independent deletion guard. Server narrowing headers depend on upstream
behavior, and gateway policy still matters. See [hosted upstreams](hosted-upstreams.md).
When retiring a session, remove unused agent/seam objects and custody Secrets,
close unneeded egress allowances, and revoke tokens at their issuers;
removing a Kubernetes Secret alone does not revoke an external token.
