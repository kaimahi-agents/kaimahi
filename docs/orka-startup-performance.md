# Orka Task and Agent startup profile

Measured 2026-09-16 on `kind-kaimahi-p1`, Orka v0.1.3. Source review used the
v0.1.3 Task/Agent controllers and worker code. Three sequential real Tasks used
`hello-world-agent` with its configured Kubernetes tool and the prompt:
`Reply with exactly: ready. Do not call tools.` This keeps the normal worker
configuration but isolates lifecycle costs from tool execution and long answers.

## Results

| Client phase | First run | Warm run 2 | Warm run 3 |
| --- | ---: | ---: | ---: |
| Result-session setup | 1.238 s | 1.258 s | 1.223 s |
| Task creation | 291 ms | 331 ms | 256 ms |
| Execution and completion wait | 35.204 s | 5.563 s | 5.401 s |
| Result retrieval and surrounding checks | 612 ms | 512 ms | 502 ms |
| Timing lookup | 6 ms | 4 ms | 16 ms |
| **End to end** | **37.370 s** | **7.678 s** | **7.406 s** |

Within execution (server event timestamps; do not add these to the client phases):

| Server phase | First run | Warm run 2 | Warm run 3 |
| --- | ---: | ---: | ---: |
| TaskCreated → TaskJobCreated | 43 ms | 72 ms | 38 ms |
| Job created → WorkerStarted | 1.069 s | 891 ms | 860 ms |
| Worker preparation → model request | 70 ms | 42 ms | 40 ms |
| Model request, 1,230 input / 3 output tokens | 31.042 s | 387 ms | 365 ms |
| Result submission | 32 ms | 34 ms | 28 ms |
| Worker cleanup | 62 ms | 40 ms | 26 ms |
| WorkerCompleted → TaskSucceeded | 2.442 s | 2.798 s | 3.098 s |

The first model request includes a cold/uncached state; event telemetry cannot
separate model load from prompt evaluation. Earlier native Ollama benchmarks
isolated prompt evaluation as the dominant uncached cost. Warm results make the
fixed orchestration overhead visible: about **7 seconds excluding model time**.

## What “Agent startup” actually does

For this AI Task route, an Orka Agent is configuration, not a dedicated running
agent pod. Its reconciler validates Provider, Secret, tools, skills, and prompt
references, counts active Tasks, and updates Ready/status. It does not load a
model or launch a per-Agent service. The model server is the existing Ollama
Deployment. Each Task starts a new AI-worker Job.

The Task controller's pending path checks optional limits/locks, resolves Agent
and Provider configuration, validates execution settings, ensures worker RBAC,
and creates a Job. All of this is inside the observed 38–72 ms Task-event to
Job-event interval on the warm cluster. Some admission/initial reconciliation
work precedes TaskCreated, so that interval is not the whole create transaction.

The worker loads Tool CRDs, registers the automatic memory tools, fetches memory
context, constructs its model prompt and enters the model/tool loop. Its observed
pre-model preparation was only 40–70 ms. Optimizing these metadata reads is not
the first priority on this cluster.

## Completion tail: Job propagation, not a mandatory five-second sleep

`TaskReconciler.handleRunning` completes the Task when `job.Status.Succeeded > 0`.
The controller schedules a five-second fallback requeue, **but also watches owned
Jobs**, so five seconds is not a compulsory wait on every turn.

Observed worker exit and Job condition times:

| Task suffix | Container finished | Job Complete | Task completion |
| --- | --- | --- | --- |
| `dd1053f5f7fba868` | 17:41:23 | 17:41:25 | 17:41:25 |
| `f686e28fe670e5a1` | 17:41:31 | 17:41:33 | 17:41:33 |
| `414a004d82feb3e4` | 17:41:39 | 17:41:42 | 17:41:42 |

Kubernetes status fields have second precision. They show that most of the
2–3 second tail occurs before Job completion becomes visible; they do not resolve
the exact shares attributable to kubelet, pod-status publication and Job controller.
Task finalization collects the result, updates status/outcome and emits its event.
Agent `lastUsed` is updated after that event, not a prerequisite to reading it.

## KMX client costs

`openOrkaResultSession` performs three serial kubectl operations before opening
the forward: read ServiceAccount, read Service, mint token. Independent timing
samples of the same operations were:

- ServiceAccount read: 234–355 ms.
- Service read: 255–336 ms.
- TokenRequest: 262–310 ms.
- Agent read: 310–331 ms.

Those include process startup and API transport. The full session setup was
1.22–1.26 s. These profiling runs recreated the session every turn; chat now
reuses it as described in [implemented changes](#kmx-changes-implemented-after-profiling).

`waitOrkaTaskResultProgress` does a fresh kubectl status read, then sleeps one
second if not complete. With process/API cost, polling is roughly 1.25–1.35 s
per unsuccessful iteration, not exactly one second. The successful read is
followed by additional identity/state checks around the HTTP result read. Those
checks account for much of the measured 0.50–0.61 s result phase; the earlier
controller result endpoint measurements were milliseconds.

## Where to cut time

| Change | Targeted cost | Location |
| --- | --- | --- |
| Keep a bounded result session across chat turns (implemented) | Historical ~1.2 s setup per warm turn | KMX |
| Watch Task status instead of sleep/poll/subprocess cycles | Up to roughly one polling cycle plus repeated process overhead | KMX |
| Use a persistent Kubernetes transport for reads | ~0.25–0.35 s per subprocess read; overlaps the items above | KMX |
| Earlier authoritative worker completion signal | Observed 2.4–3.1 s Job-completion tail | Orka |
| Persistent worker / worker pool | Observed ~0.9–1.1 s startup; more on cold images | Orka architecture |

Session reuse must refresh bounded credentials before expiry and end on tunnel
loss; it must not reconnect an old bearer to an unverified replacement listener.
Watch/transport work must preserve namespace, UID, generation, deletion and
terminal-state checks. Savings overlap and should not simply be summed.

An early worker signal would need to establish terminal execution success and
immutable result identity, not merely “a result body has been uploaded.” Removing
the Job wait in KMX by accepting an early body would change success semantics.

The profiling budget suggested a native warm path of **3–5 seconds** with client
reuse/watch changes plus an upstream completion improvement. That is a target
derived from the measured budget, not a measured implementation result. A cold
model/prompt still dominates unless separately addressed.

## Relation to the Copilot adapter

The current Copilot adapter bypasses Orka Task/Job execution. Its ~14.7-second
pod question therefore does **not** pay these native worker/completion costs.
It pays repeated Copilot process startup instead; see `copilot-performance.md`.
For that active route, persistent Copilot sessions should come before worker-pool
work. Shared Kubernetes lookup/tunnel improvements help both routes.

## KMX changes implemented after profiling

- Native Orka chat reuses a result session with an eight-minute lifetime. Before
  a new turn, a session with less than five minutes remaining is replaced, so the
  full turn deadline remains covered by its token. Configuration changes, chat
  shutdown and connection loss invalidate the session.
- The successful terminal-state poll serves as the pre-result identity check;
  the post-HTTP identity/state check remains. This eliminates one redundant
  `kubectl get` on the happy path without accepting an early result.
- Copilot turns read the Agent once for instructions and tool references, and
  reuse the tool tunnel until configuration changes or chat ends.

Live tool execution measured 468 ms for the initial tunnel/request, then **30 ms
and 27 ms** through the reused tunnel. Two native short-answer turns measured
7.43 s and 7.11 s; completion/polling variability limits conclusions from that
small sample. Persistent Copilot processes and Kubernetes watches are still
follow-up work; these changes do not claim to remove their overhead.
