# kmx tells the truth about the system it is pointed at

Three findings, one shape: kmx states things about a system it has not checked.

## (c) No current context — RULED: refuse the invented default, and never follow current-context

Confirmed cause, `internal/kmx/config/config.go:107-143`. Precedence today:
`--context` > `KUBE_CTX` > `kmx ctx` selection > `kind-$KIND_CLUSTER`, and
`KIND_CLUSTER` itself defaults to `kaimahi-p1`. So with nothing set anywhere
kmx acts on `kind-kaimahi-p1`, labels the source `KIND_CLUSTER` when no such
variable was set, and never prints the source at all — `ContextSource` is
recorded "for the banner" and the banner does not carry it.

**Considered and rejected: adopting the kubeconfig's `current-context`.** It
is the obvious fix and it is wrong here. `app.go:56-61` states why kmx pins an
explicit context: a bare kubectl follows `current-context`, which
`az aks get-credentials` rewrites silently, so a command meant for kind can
quietly aim at a managed cluster. Following it would trade an invented target
for a target someone else's tool chose. The pin stays.

What was actually broken is narrower: the pin was pointing at something
**nobody chose**, and said nothing about it.

1. `Load` labels the bare fallback `default`, not `KIND_CLUSTER`. The
   `KIND_CLUSTER` label is now used only when that variable is really in the
   environment, so CI (`KIND_CLUSTER=kaimahi-p1 bin/kmx ...`) and
   `KIND_CLUSTER=mine kmx up` are byte-identical.
2. The guard **refuses** a `default`-sourced context when the kubeconfig has
   any contexts in it. One rule, no per-command allowlist to decay:

     kmx acts on the cluster you name. On a machine with no clusters there is
     nothing to confuse it with, so the default stands and the quickstart
     works. On a machine that HAS clusters and where nothing chose one, kmx
     refuses.

   That is the reported case exactly — a kubeconfig with clusters and no
   current-context — and it is the dangerous one, because a corporate
   kubeconfig is present and kmx invents a target beside it.
3. The refusal NAMES the current context if there is one, and says kmx does
   not follow it, and gives the one command that chooses it. Information in
   front of the operator; action taken on nothing.
4. `kmx up` and `kmx quickstart` RECORD the cluster they create through the
   existing `kmx ctx` selection file. That is what keeps the fast path fast:
   the first command chooses explicitly and durably, so every later bare
   command resolves through `kmx ctx` rather than through an invention. It is
   also inspectable — `kmx ctx` prints it.
5. The banner carries a `chosen by:` line on every run, so "who picked this
   cluster" is on screen even when nothing is asked.

Impossible to miss by piping stdout: the banner already goes to stderr
(`app.go:115` passes `a.Err`), the refusal is a nonzero exit, and a test pins
that the guard writes nothing to stdout.

## (b) Version handshake — RULED: an endpoint, a contract integer, per-operation refusal

The plane has no version surface at all: 16 `/admin/*` routes plus an
unauthenticated `/healthz`, and `metrics.Version()` is published only as a
Prometheus label on the ops port. So kmx infers age from response shape, in
one place, for one field.

- **How kmx learns the version: an endpoint the plane serves.**
  `GET /admin/version` (authed, like every other `/admin/*` route) returns
  `{"version": "...", "admin_contract": N}`. Chosen over "a version in an
  existing response" (every response would have to carry it, and the field
  itself becomes the thing that can be missing — the failure we are fixing)
  and over "infer from a 404" (a 404 tells you a route is absent, never which
  version you have).
- **Both ends.** A plane too old to serve the route 404s; that is contract 0
  and it is a definite fact, not a guess, because the handshake runs only
  after `/healthz` on our own proven forward. A plane NEWER than kmx reports a
  contract kmx does not know; kmx proceeds and says so on stderr.
- **Compatibility policy: per operation, not global.** kmx N refuses only the
  operations plane N-1 cannot serve, before sending them; every other command
  works normally. A blanket refusal would strand a working cluster over one
  unreachable feature; a blanket warning is the 404 with extra words. The
  refusal names both versions and the fix.
- **Newer plane than kmx: proceed with a note.** The state lives in the plane,
  so a CLI that is merely behind must not strand it. Safe because the policy
  this lane writes down makes it safe: the admin surface only ever GROWS
  within a major version.
- **W35's `table_declared == nil` message is replaced, not doubled.** The
  preflight catches an old plane first and with a better message. The
  shape check stays as a fail-closed backstop but its diagnosis changes: a
  plane that declares the contract and omits the field is a plane BUG, not an
  old plane, and it now says that.
- Accepted cost: a plane built from a commit between v0.1.0 and the release
  that adds the endpoint serves `table_declared` but reports contract 0, so
  it is refused conservatively. Those are unreleased shas; the fix is one
  command.

## (d) The credential lag — RULED: a verdict has a time, and a stale verdict is `unknown`

`Accepted` is a kagent CRD condition: a CACHED reconcile result, not a live
check. The plane reads its credential file per request, so the plane is not
the liar — kagent's cached verdict is, and the projected Secret refresh means
it stays stale for minutes.

The repository already knows this, in exactly one place and untested:
`workflow_run.go`'s `reconnectSeam` waits `secretProjectionWait` (45s) and
annotates the seam to force a reconcile. Everything else — `create.go`'s
`validateToolServer`, `chat_interactive.go`'s ModelConfig check, `kmx status`'s
ACCEPTED column, the `kubectl wait` in `tools.go`/`up.go` — reads the cached
verdict as truth.

Rule, in the shipped `none` / `unknown` vocabulary:

  A verdict reached BEFORE the credential it is being asked about is not a
  verdict. It is `unknown`.

- Where kmx just wrote a credential it knows the write time exactly, so a
  verdict older than that write is never reported as accepted. kmx waits for a
  newer verdict, bounded, and on timeout says `unknown` and what to do.
- Where kmx did not write anything (`kmx status`), it cannot know what the
  verdict was reached against, so it publishes WHEN, and the legend says the
  column is kagent's last reconcile result rather than a live check.
- The classifier is promoted out of `workflow_run.go` so there is one
  implementation, and it gets the tests that path never had.

## Guardrails

Credential handling touched: none. (d) reads condition TIMESTAMPS and never a
Secret value; status keeps reading only Secret NAMES. The terminal-only
capture prompt is untouched and stays the only accepting path.
