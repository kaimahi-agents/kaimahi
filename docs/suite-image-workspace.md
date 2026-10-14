# Experimental image workspace lifecycle

One tool-free `image-http` member uses the same authored workspace and bounded
preparation as native profiles. Inference requirements are capabilities; the
concrete model, endpoint and Secret references belong to environment bindings.
Native Orka/kagent keep exact models and zero required member images.

```sh
kmx create greeter --workspace ./my-suite --execution image-http \
  --instructions ./instructions.md --context-tokens 8192 --output-tokens 1024
kmx push --workspace ./my-suite "$REGISTRY/team:v1" --builder "$BUILDX_BUILDER"
# Use the immutable release field printed by push, not its source-suite field.
kmx eval --release "$RELEASE_REF" --to-context "$CONTEXT" \
  --environment target.json --out ../private/passing.json
kmx lift "$RELEASE_REF" --to-context "$CONTEXT" \
  --environment target.json --evidence ../private/passing.json --plan
kmx lift "$RELEASE_REF" --to-context "$CONTEXT" \
  --environment target.json --evidence ../private/passing.json
kmx console --workspace ./my-suite --registry "$REGISTRY/team:v1" \
  --builder "$BUILDX_BUILDER"
```

## Portable release

The user tag names a KMX-private OCI image-release index, distinct from the
AgentSuite definition. Its annotation records the exact source-suite
descriptor and member/platform image and root-graph descriptors. Those graphs
are index children in the same repository; copying the full OCI graph retains
suite bytes, image layers and attestation siblings. Locations are not embedded
in the association. The current closed profile requires exactly one amd64 slot.

Publication completes required images, uploads the source under an intermediate
content-addressed source tag, validates the association and only then assigns the
final release tag. Failed builds do not advance that final pointer. Concurrent
tag replacement is still a non-atomic preflight, like existing suite publication.
Artifacts uploaded before a failure may remain. Local workspace publication
records are caches, not the portable association authority.

Suite/image validation remains separate from sandbox conformance and signatures.
The image declaration still carries its original source retrieval hint; full
suite digest and packaged requirements remain the source identity. Case changes
change the full package and image association even when public graph G is stable.

## Evaluation and lift boundary

Image evaluation consumes an immutable release and explicit destination bindings.
It requires absent placement names, performs GET-only inspection before consent,
preflights resources after consent, creates ConfigMap/Deployment/Service and runs
all packaged text cases. No ambiguous request is resent. Existing names are not
adopted or updated. Resources remain after failed/unknown evaluation.

Only full passing metadata is exported into a new private file. The experimental
receipt binds suite/release/image/profile, model/configuration, target namespace
UID, referenced Secret UID/resourceVersion, case/assertion identities and actual
placement UIDs/specs. Request IDs are caller-side observation IDs, not remote
Task identities or independent execution attestation. Text inputs/answers remain
in memory in KMX; runtime/model infrastructure can retain them.

Shared root lift supports absent-resource creation or exact evaluated-placement
reuse for images. It rechecks evidence, bindings and resource proofs, uses the
shared guard and single-use apply loop, then revalidates actual placement UIDs.
It performs no build, publication or evaluation calls. Occupied replacements
refuse; newly created UIDs do not authorize later receipt-bound reuse.

Experimental `--promote` requires absent destination resources. Only context,
cluster/namespace identities and referenced Secret revisions may differ. Release,
suite, image, model, endpoint, generated configuration and all other environment
fields remain exact. Evidence remains source-only; destination readiness is not
destination evaluation. These narrow image transition rules remain subject to
contract review under #356/#350/#348.

## One interactive orchestration

The console uses the source/case-review, consent, plan-review, cancellation and
outcome reporting from #484. `--workspace` preselects the root in that pane;
there is no separate image-console state machine. For an image member, the binding
selection is an environment file matching the selected context/namespace, and
`--registry` supplies candidate publication. The flow builds/publishes, evaluates,
then reviews same-target lift. Cross-target promotion is exposed by root CLI;
source authoring uses root `create` or `new <directory>` in the shared console
workspace selector. The console asks for member name, profile and instructions
file, then the exact native model or `capabilities` for an image member. Image
capacity defaults are 8192/1024 and remain editable in member.yaml. Demo mode
never writes authored files.

Successful image lift prints a private retained placement ID. A fresh process
can re-resolve the release and recheck binding/resource identities with:

```sh
kmx suite status --deployment "$PLACEMENT_ID"
kmx run --deployment "$PLACEMENT_ID" --prompt Hello
kmx chat --deployment "$PLACEMENT_ID"
kmx console --deployment "$PLACEMENT_ID"
```

Chat uses the shared terminal chat renderer and independent HTTP requests, not
a server-side session. The placement record references the original environment
file and release; keep both accessible. This is local rediscovery, not operation-ID
recovery or cluster-shared history (#346). Historical `suite run` remains separate.

## Evidence and remaining work

Hermetic tests cover full OCI graph copying between layouts, association
refusals, exact image evaluation metadata, shared lift reuse without extra writes
or execution, and credential-revision drift refusal. These use synthetic image
metadata and controlled execution; they do not establish live image deployment.
Absent-resource and cross-namespace promotion tests also require all admissions
before creates, preserve source-only evidence and refuse model changes.

The opt-in [smoke runner](../scripts/ci/suite-lift-smoke.py) exercises build/publish,
evaluation, shared lift reuse, model-mismatch refusal and retained status/run. Its
[controlled model](../scripts/ci/suite-model.py) and
[fixture generator](../scripts/sample-suite.py) remain test support. Earlier
kind/private-registry/AKS runs do not qualify this revision. One new controlled
AKS exercise passed image build/publication, full-case evaluation, shared lift,
fresh-process status/run and actual-PTY shared chat. A separate actual-PTY console
exercise completed source/case review, image evaluation/publication and reviewed
Apply/Verify through the shared pane, then exited successfully. It used a synthetic model;
it does not qualify external model quality or cross-target image promotion. #485's native
cross-target qualifier supplies the test pattern, not image coverage.

Open: review of image promotion/create semantics, native/image common deployment
handles, live image qualification, evaluation-bound final promotion publication,
full sandbox inventory/binding (#307) and signer policy (#374). `suitedeploy`
remains unwired proposal scaffolding.

## Integration and review boundaries

Root commands and shared console review follow current main, including removal
of legacy bundle/live-create routes and native AgentSessions authoring/evaluation.
Image lift has no alternative apply engine: the common release guard and
single-use loop own effects. Ownership labels bind suite digest, member, instance
and target; occupied resources still require exact receipt UID/spec proof.
The old name-only image labels are not accepted as that proof.

Cases currently use the existing KMX-private packaged convention. Public optional
agent-bound definitions are proposed in #497/#492; migration and legacy reading
belong to #493. No optional-spec change is inferred to disable the current lift
evidence requirement. Results stay external to source content to avoid digest
cycles. Descriptor-wide migration (#496) and module extraction (#491) remain
separate work; the release association already uses OCI byte descriptors.
