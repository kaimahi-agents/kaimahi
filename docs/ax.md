# Evaluating Google AX from kmx

[`kmx ax status`](#what-status-answers) makes
[Google AX](https://github.com/google/ax) visible as an **evaluation option**.
It does not make AX a second supported Kaimahi platform: Orka remains the
current platform for installation, native authoring and migration.

That distinction is intentional. AX and Orka occupy the same orchestration
layer. Both own tasks, model configuration, runtime lifecycle and task status;
Orka additionally publishes task results, while AX `v0.3.0` does not publish a
command result through its Task API. AX uses
[Agent Substrate](https://github.com/agent-substrate/substrate) for physical
execution. Installing AX beside Orka is therefore not selecting a new sandbox
under the same platform. It is running a second control plane.

## The command

```console
$ kmx ax status [--namespace ax-system]
support                  evaluation only — kmx does not install AX
source baseline          v0.3.0 (d8ed0fe38bce)
AX deployments           ax-controller=1/1 ready ax-server=1/1 ready ax-redis=1/1 ready
image templates declared ax-controller=registry.example/ax-controller@sha256:… ax-server=registry.example/ax-server@sha256:… ax-redis=redis@sha256:…
Substrate endpoint       api.ate-system.svc.cluster.local:443
Substrate router         atenet-router.ate-system.svc.cluster.local:80
Substrate API Service    ate-system/api present
Substrate router Service ate-system/atenet-router present
AX services              ax-redis=present ax-server=present

Ready components do not prove a sandboxed Task, egress enforcement, suspend/resume,
or AX/Substrate version compatibility. Run a representative AX Task to prove those.
AX must be installed from its reviewed upstream source; there is no `kmx ax install`.
```

The image references are read from the Deployment templates. They are not
called the images running: during a failed rollout a ready pod may still be on
the previous template, and a mutable tag does not reveal the pulled digest.
The source baseline is the AX source reviewed while implementing this view; it
is **not** restated as the version running, because AX images are built and
published by the operator.

An absent `ax-system` namespace is reported as absent. A cluster that cannot be
read, or an RBAC denial, is not converted into absence. Those states have
different fixes and status refuses to invent an answer.

## Why there is no `kmx ax install`

AX release [`v0.3.0`](https://github.com/google/ax/releases/tag/v0.3.0) has no
binary or deployment assets. Its documented installation path requires:

1. a reviewed AX source checkout and Go 1.27+ toolchain (`v0.3.0` declares Go
   1.27.1);
2. [`ko`](https://ko.build/) and a registry the cluster can pull from;
3. locally building and publishing `ax-controller` and `ax-server`;
4. deploying Redis from a manifest that currently references `redis:7-alpine`;
5. a compatible Agent Substrate installation, routing and trust material;
6. a `gvisor-default` SandboxConfig, because AX-created per-Task templates
   select it; and
7. snapshot storage the Substrate workers can use. AX defaults to
   `gs://snapshot-substrate-test-ax-substrate/ate-env/`; set
   `AX_SNAPSHOTS_BUCKET` for an environment that does not own that bucket.

The controller normally creates a per-Task ActorTemplate. Its configured
`default-template` is the fallback if that creation fails, not the normal
prerequisite for every Task.

The AX manifests contain `ko://` image references, not immutable images kmx can
verify and apply. Wrapping that source build would make Kaimahi the owner of
AX's image build, registry publication and AX/Substrate compatibility matrix.
It would not be the thin upstream integration that `kmx orka install` is.

Install AX by following the instructions at the exact upstream revision you
have reviewed, then use `kmx ax status` against that cluster. Do not use AX
`main` as a version: AX warns that its APIs and specifications may change before
a stable release.

## What status answers

The view checks:

- `ax-controller`, `ax-server` and `ax-redis` Deployments in the selected AX
  namespace (`ax-system` by default);
- current-generation rollout state, not ready replicas alone;
- the image references those Deployment templates declare;
- the Substrate endpoint and atenet router configured on `ax-controller`;
- AX's `ax-server` and `ax-redis` Services;
- Kubernetes Services named by the configured Substrate endpoint and router.

`--namespace` follows AX's own configurable namespace. A Substrate endpoint
outside Kubernetes Service DNS is reported as external and not checkable by
this view; kmx does not replace it with an irrelevant `ate-system` default.

It does **not** prove:

- that an AX Task can be created or reaches `Ready`;
- whether that Task ran under gVisor or a microVM;
- that its Gateway blocked a forbidden egress destination;
- that suspend/resume or checkpoint/restore preserved the required state;
- that the installed AX and Agent Substrate revisions are compatible;
- production durability, upgrades, recovery objectives or cost.

Those need a representative workload and a recorded version matrix. A useful
qualification should run the same workload through Orka's existing workspace
backend, Orka with Agent Substrate, and AX with Agent Substrate, then compare
startup, state recovery, egress, credential exposure, cleanup and operator
work. AX's presence alone answers none of those questions.

## Relationship to the existing Substrate evaluation

Kaimahi already records the intended composition boundary in the
[Agent Substrate evaluation](reviews/2026-09-10-substrate-evaluation.md): under
Orka, Substrate is a selectable workspace backend, **not a second orchestrator**.
Orka keeps Task attempts, Sessions, admission, cancellation and result
publication while Substrate changes workload materialization.

`kmx ax status` serves a different purpose. It lets us inspect AX as the
reference orchestrator built directly on Substrate and compare its choices
without building an AX adapter, copying its APIs, or claiming support Kaimahi
has not earned.
