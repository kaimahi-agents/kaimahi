# Quickstart Orka chat latency

Measured on 2026-09-16 in `kind-kaimahi-p1`, Orka v0.1.3,
Ollama 0.11.8, `qwen2.5:3b` on CPU. These are observations, not latency promises.

## Where the time went

Task `hello-world-agent-3ddcac99a863153f`, prompt `hi`:

| Phase | Duration |
| --- | ---: |
| Task event → Job created | 28 ms |
| Job created → worker started | 906 ms |
| Worker preparation | 22 ms |
| Model request | 22.693 s |
| Result submission | 28 ms |
| Worker cleanup | 29 ms |
| Worker completed → Task succeeded | 3.185 s |

Orka's event timestamps measure the server-side phases. Ollama independently
logged a 22.694 s `/v1/chat/completions` request. The result was read approximately
0.8 s after the Task succeeded. Client setup wasn't recorded for this historical
request, so it cannot be inferred from the Task timestamps.

The worker automatically advertised four memory tools (`recall_memory`,
`remember`, `propose_memory`, `search_transcript`) and appended memory reflection
instructions despite this Agent having no explicit tools. Its model request used
**1,035 input tokens and 10 output tokens**. A direct request with only the
configured Agent instructions used **47 input tokens and 10 output tokens**.

## Controlled model comparison

Sequential requests to the same loaded model using Ollama `/api/chat` metrics:

| Request | Total | Prompt evaluation | Output generation | Model load |
| --- | ---: | ---: | ---: | ---: |
| Reconstructed v0.1.3 worker prompt/tools | 23.659 s | 22.876 s | 668 ms | 99 ms |
| Same worker request, cached prefix | 858 ms | 88 ms | 655 ms | 101 ms |
| Agent-only instructions, no tools | 1.039 s | 268 ms | 689 ms | 78 ms |

This isolates **uncached prompt evaluation**, not token generation or model
loading, as the dominant cost. The worker's initial unsupported `/v1/responses`
probe returned 404 immediately; it did not cause a multi-second retry delay.

## Implemented changes

- Chat now reports client result-connection setup, Task creation, execution wait,
  result retrieval, and timing lookup separately. Worker phases and token counts
  come from Orka's filtered execution events. Missing telemetry is reported as
  unavailable, never invented. Worker durations are nested within execution and
  must not be added to the client durations a second time.
- Quickstart loads Ollama weights with an **empty prompt**, preserving an existing
  agent/tool prefix cache. Previously the unrelated `Reply with exactly: ready`
  prompt replaced that prefix on each wizard rerun. Model residency and prefix
  caching are separate: `OLLAMA_KEEP_ALIVE=1h` alone doesn't preserve the prefix
  when another prompt replaces it.
- The same cache-preserving preload is used for detected host Ollama runtimes.

A repeated worker-shaped model request after the new empty-prompt preload took
886 ms (101 ms prompt evaluation). Two subsequent real Orka Tasks through KMX's
profiled path took **7.576 s** and **7.645 s** end to end:

| Client phase | Run 1 | Run 2 |
| --- | ---: | ---: |
| Result connection | 1.133 s | 1.199 s |
| Task creation | 300 ms | 289 ms |
| Execution + completion wait | 5.565 s | 5.566 s |
| Result retrieval | 561 ms | 578 ms |
| Timing lookup | 5 ms | 4 ms |

Within execution, model requests were 1.284 s and 985 ms, worker startup was
1.021 s and 868 ms, and completion reporting was 2.217 s and 3.078 s.
These warm-cache results do not claim to eliminate a first uncached request.

## Further reductions, in priority order

1. **Make memory-tool injection opt-in upstream in Orka.** Avoid processing
   approximately 1,000 unnecessary input tokens for a simple greeting agent.
   Preserve explicit tool and memory behavior for agents that need it; disabling
   the memory-fetch request alone doesn't disable automatic tool advertisement.
2. **Preserve/cache the actual agent prefix.** The implemented preload preserves
   warm state on reruns. Warming the exact prefix on a first run would shift its
   cost into setup, not remove it. Other workloads or prompt changes can evict it.
3. **Shorten upstream completion reporting.** Orka currently waits for Kubernetes
   Job/Pod termination to become visible. A worker completion acknowledgement
   could remove around 2–3 s, but must preserve execution-success semantics.
   KMX must not treat an early result body alone as successful execution.
4. **Reuse result-session setup within a chat.** Roughly 1.1–1.2 s per request is
   spent validating the ServiceAccount/Service, minting a bounded token, and
   establishing a port-forward. Reuse needs bounded token lifetimes and the
   existing connection-loss/cancellation behavior.
5. **Watch status instead of one-second polling.** This can reduce the polling
   tail and repeated `kubectl` process overhead. Identity/generation and final
   result checks still need to surround retrieval.
6. **Persistent workers or a direct streaming chat route** can reduce startup and
   time-to-first-text further, but change the fresh-Task execution architecture.

## Rechecking a slow request

Enable `--verbose` on `kmx quickstart --interactive`, or enter `/verbose-on` in chat,
to show `WORKING` progress and the `TIMING` section. `/verbose-off` hides those
details again and skips the timing lookup. The responding animation with elapsed
seconds and the brief total response time remain visible in either mode.
Correlate the displayed Task with:

```sh
kubectl --context kind-kaimahi-p1 -n orka-system get tasks.core.orka.ai
kubectl --context kind-kaimahi-p1 -n orka-system logs job/<job-name> --timestamps
kubectl --context kind-kaimahi-p1 -n ollama logs deploy/ollama --timestamps --since=5m
kubectl --context kind-kaimahi-p1 -n ollama exec deploy/ollama -- ollama ps
```

For prompt-evaluation versus generation time, Ollama's native non-streaming
`/api/chat` response exposes `load_duration`, `prompt_eval_duration`,
`eval_duration`, `prompt_eval_count`, and `eval_count`. Model requests in the chat
profile include both evaluation and generation; Orka's events do not split them.

## Pod-list tool turn (2026-09-16)

The question `what pods are running?` took 146.684 s from TaskCreated to
TaskSucceeded for Task `hello-world-agent-0e7fc60df61e1047`:

- First model request: 8.095 s, 1,173 input / 24 output tokens.
- Kubernetes tool call: 48 ms, returning 3,309 characters including completed pods.
- Second model request: 134.618 s, 2,546 input / 1,019 output tokens.
- Worker completion to TaskSucceeded: 2.467 s.

The tool now supports a server-side pod `phase` filter and its description asks
the model to select `Running` for this question. Compact JSON also reduces
unnecessary whitespace. A subsequent real Task with the same question took
69.086 s end to end, with two model calls totaling 62.705 s, 2,983 input / 340
output tokens across those calls, and 88 ms between calls. This comparison is
observational: cache state and model generation vary. The smaller response helps,
but CPU model execution still dominates. The generated answer also misspelled a
namespace; model summaries are not a substitute for the authoritative tool data.

Chat keeps an elapsed responding animation even without verbose mode. On Linux,
terminal echo is disabled while waiting for Orka, preserving canonical input and
SIGINT so arrow presses do not print `^[[B` into the transcript and Ctrl-C works.
