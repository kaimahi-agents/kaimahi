# FAQ and troubleshooting

Honest answers, mined from what actually went wrong while building this —
each entry comes from a phase's recorded verification or from something
hit while writing these docs, not from speculation. Commands assume the
[getting started](getting-started.md) setup.

## The agent errors with `'str' object has no attribute 'get'`

You swapped in a smaller model. kagent's python runtime gives every agent
a built-in `ask_user` tool, and small Llamas (`llama3.2:1b`/`3b`) call it
with malformed arguments — the invocation fails with exactly that error,
and telling the model not to use the tool in the system message doesn't
stop it. That's why the pin is `qwen2.5:3b`, which answers plainly.

If you want a different model, invocation-test it before trusting it:
`make model MODEL=<tag>`, edit `model:` in the YAML, and run several fresh
chats. "It's a known model" is not a test.

## The chat came back empty and says `input-required`

The agent asked *you* a question instead of answering. kagent's python
runtime gives every agent a built-in `ask_user` tool — nothing in
`k8s/hello-world.yaml` declares it, and the system message forbidding
questions does not reliably stop a 3B model — and when it is called the A2A
task ends in `input-required` with no artifacts, so the reply is empty. In a
script or in CI nobody is there to answer, so the task simply stops.

`kmx agent chat` re-asks a question like that up to twice, saying so on
stderr each time (`chat re-sample 1 of 2`); a model that asked a question is
being non-deterministic, and a second sample is a fair one. It re-asks only
when the agent did nothing else — no tool call had already run — and never
when you passed `--session`, because the question is pending in that session.
If it still asks, answer it: `kmx agent chat --interactive hello-world` opens
the chat that can.

The same `input-required` also carries a human approval on a real tool call.
That one is never re-asked and never treated as an answer — it is a decision
waiting for a person, and `kmx agent chat --interactive` is where you make it.

## The tool call worked but the answer is wrong

The second small-model failure mode, and the sneakier one: `hello-tools`
calls the tool correctly, gets correct data back, and then garbles or
contradicts it in the summary. While verifying the MCP tool path the model
occasionally received a ConfigMap list and still answered "there are no
configmaps". While writing this FAQ it read the ollama pod list correctly
and replied `olla-854b55bc55-9vk6d` — the real pod is
`ollama-854b55bc55-9vk6d`. Two characters silently gone.

The agent's system message orders it to copy tool output verbatim, which
got the CI probe to 10/10 trials, but a 3B model relaying data is still a
3B model. Treat the A2A task history (which records the actual
`function_call` and `function_response`) as the truth and the prose as a
paraphrase. If you swap models, re-measure *both* failure modes — the
calling side and the relaying side.

## The Copilot preset worked yesterday and fails today

The Copilot token expires, typically within hours. There's no long-lived
key: `make copilot-secret` exchanges your GitHub device login for a
short-lived API token, and that's what lives in the cluster. When auth
starts failing:

```bash
make copilot-secret                 # re-mint (the device login is cached — usually no browser step)
make use PRESET=github-copilot      # restart the pod so it picks up the new Secret
```

On the governed path it's `make plane-copilot-secret` instead, and no
restart — the proxy reads the Secret-mounted file per request. An
in-cluster auto-refresher was deliberately not built with the model path;
token lifecycle is governance-plane territory.

## Why the browser login? I'm already logged into `gh`

The gh CLI's own OAuth token is not Copilot-entitled — GitHub's token
exchange returns 403 for it (verified). The device flow authenticates as
the same OAuth client Copilot's own tooling uses, which is entitled. One
extra browser approval on first run, cached after that.

Also worth knowing: `api.githubcopilot.com` is not part of GitHub's
documented public API surface. It's what GitHub's own clients use, but it
can change without notice, and usage counts against your plan's
premium-request accounting.

## I have a cluster and paths from the tomte era

The project renamed tomte → kaimahi. Two things moved:

- **The kind cluster** is now `kaimahi-p1`. An existing `tomte-p1` keeps
  working if you override every make call (`make up KIND_CLUSTER=tomte-p1`,
  same for `chat`, `down`, …), or start fresh:
  `kind delete cluster --name tomte-p1 && make up`.
- **The Copilot login cache** moved from `~/.config/tomte/` to
  `~/.config/kaimahi/` (override: `KAIMAHI_COPILOT_TOKEN_FILE`). Migrate it
  once — `mkdir -p ~/.config/kaimahi && mv ~/.config/tomte/copilot-oauth-token
  ~/.config/kaimahi/` — or log in again and let the old file rot.

## What "schema-valid only" means

Five presets (`anthropic`, `openai`, `openrouter`, `azure-foundry`,
`openai-compatible`) are marked schema-valid only. That's literal: CI
server-side dry-runs each one against the live kagent CRDs on every PR, so
the YAML is well-formed and the fields exist — but no real completion has
ever been bought through them. A preset graduates only when an actual
`make chat` completes through the endpoint, and nobody has paid to do that
yet for those five. They should work. "Should" is the honest word.

## Ollama is free — why is it budgeted in tokens?

Because $0 is a *classification*, not a fact of nature, and because "free"
still isn't "unlimited". The upstream table marks in-cluster ollama
`free` explicitly, so its ledger rows cost 0 cents — but tokens are still
counted, and a token cap (`make budget CAP_TOKENS=…`) is the only lever
that can exhaust it. That's deliberate: the free tier is where you rehearse
governance before pointing it at money, and a runaway agent burning tokens
is worth noticing even when nobody is billed.

The flip side: Copilot is `metered` with no bundled price, since
subscription usage has no public per-token price and kaimahi never invents
one. Its tokens are still counted, at 0 cents (`source=unpriced`) — so
govern it with a token budget. Under a *cents* budget an unpriced model is denied
outright, because spend that can't be charged against the budget can't be
admitted.

## What the plane's status codes mean

When a governed chat fails, the proxy's status tells you which layer said
no. The agent surfaces it as a failed task with the message text.

- **401 unauthorized** — the request carried no kaimahi token, or one the
  plane doesn't recognize. Usually the agent-side `kaimahi-governed-token`
  Secret is missing or holds a stale token. Re-running `make govern` alone
  won't mint a new one (it sees the existing credential and keeps it);
  recovery is the lost-token procedure below.
- **403 forbidden** — authenticated, but not allowed: an upstream not in
  the table, a path other than the one allowed route, "metering
  unavailable" (budget exists but the ledger can't be read — fail closed),
  or an unpriced model under a cents budget.
- **502 bad gateway** — the upstream was reached and something about its
  answer stopped it being handed over. Either the response was cut, or —
  "reported no usage this protocol can read" — it succeeded and carried
  no token counts the upstream's declared `protocol` could read, so the
  plane refused it rather than recording the call as costing nothing.
  Check the upstream's `protocol` against what it actually speaks
  ([spend.md](spend.md#the-two-protocols)); the row is in the ledger with
  `source=unmetered`.
- **429 too many requests** — "monthly budget reached" / "monthly token
  budget reached". The cap is monthly (UTC). Raise or clear it with
  `make budget`. In our runs each attempt left three denied rows in the
  ledger, because the agent runtime retried the call. The decision is
  exact across the plane's two replicas: calls that arrive together
  against a cap with room for one get one 200 and the rest 429, never
  two 200s ([spend.md](spend.md#enforcement-properties)).
- **503 service unavailable** — the plane protecting its own guarantees:
  the credential store or spend ledger is unreachable (nothing is admitted
  while spend can't be recorded), or "upstream credential unavailable" —
  e.g. the governed Copilot preset before `make plane-copilot-secret` has
  given the proxy a real token. Fix the stated dependency; the proxy
  recovers on its own.

A **502** means the proxy admitted the call but couldn't reach the
upstream; the attempt is still ledgered (zero tokens).

## My governed agent stopped showing up in the ledger

Did something re-apply `k8s/hello-world.yaml`? That file points at the
ungoverned `hello-world-model`, so a plain `kubectl apply -f` of it moves
the agent off its governed preset. The chats still work; they're only no
longer metered (an early draft of this FAQ was nearly published with that
mistake in it).

`make up` used to do exactly this. It no longer does: its `agent` step now
reads the live modelConfig first, re-applies, and restores whatever the
agent was on, printing a `NOTE:` line naming the preserved preset. If it
cannot read the live value it refuses rather than risk un-governing you.
So if you're off the governed preset, something other than `make up` put
you there. Check with
`kubectl --context <context> -n kagent get agents.kagent.dev hello-world -o
jsonpath='{.spec.declarative.modelConfig}'` and re-run `make govern`
(or `make use PRESET=governed-ollama`).

## I lost the governed token

It's shown exactly once at issue time; the plane stores only its hash, so
it cannot be recovered. If the `kaimahi-governed-token` Secret is gone,
`make govern` will refuse with instructions — delete the credential row
with the exact `psql` command it prints, then re-run `make govern` to
issue a fresh one. (Budgets are keyed to the credential, so re-set them.)

## Where did my ledger go?

`make down` deletes the kind cluster, and the plane's Postgres — PVC and
all — goes with it. The ledger survives pod restarts, not cluster
deletion. It's demo-durable, not backup-managed.

## `make use` hangs at "waiting for Ready"

Almost always a missing key Secret: an agent pointed at a ModelConfig
whose Secret doesn't exist never becomes Ready. Create it first, then
switch. Which command depends on the preset: `make model-secret
NAME=<preset>-api-key` for the five key-based presets (`anthropic`,
`openai`, `openrouter`, `azure-foundry`, `openai-compatible`),
`make copilot-secret` for `github-copilot` (its Secret is
`github-copilot-token`), and `make govern` for the governed presets
(their Secret is the issued `kaimahi-governed-token`). Check with
`kubectl --context <context> -n kagent describe agents.kagent.dev hello-world` if it's something else.

## The model I pulled disappeared after a pod restart

The Ollama pod stores models in an `emptyDir`, so a restart re-pulls.
`make model` (or `make model MODEL=<tag>`) puts it back. Deliberate — no
PVC to babysit in a demo.
