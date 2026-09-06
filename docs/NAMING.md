# Naming record

Working notes on the project name: what has been proposed, what is actually
available, and what is still owed before any name is treated as final.

**One thing here is now claimed.** A GitHub organization named for the
project, `kaimahi-agents`, exists and hosts the repo (2026-09-01). No
package or domain has been registered. An organization name is a public
claim on a name that is still provisional — a stronger one than the repo
rename was — and at the time it was made both gates set on the name (a
cultural read and trademark counsel) were still open, which made them more
urgent for it, not less. Claiming anything further is an outward-facing
action that needs the user's explicit approval naming the exact artifact.

## The candidates

All three proposals landed on the same metaphor without coordinating: a
household spirit that does the work while you sleep. That is the right
image for a delegated agent, and it is worth noting the naming instinct was
consistent even when the words were not available.

| Name | Origin | Status |
|---|---|---|
| **kaimahi** | te reo Māori — *worker* | **settled** — cultural read cleared; no trademark opinion taken |
| **hob** | English folklore — hobgoblin/household spirit that does the chores at night | **blocked — taken** on npm, PyPI, the GitHub handle, and `hob.dev` |
| **tomte** | Scandinavian folklore — farm spirit that works at night for a bowl of porridge | previous name; retired. PyPI `tomte` is taken |

Verified live against each registry's API on 2026-09-01. **This decays** —
re-verify before acting on any of it.

## hob — the short name, and why it is gone

**hob** was recommended and is the strongest of the three on ergonomics.
Three letters. An easy binary name you would not mind typing fifty times a
day. Same folklore lineage as *tomte*: feed it, do not insult it, or it
stops helping.

**It is taken**, on exactly the namespaces that matter:

| Namespace | `hob` | Detail |
|---|---|---|
| npm | **TAKEN** | published at 0.0.1 |
| PyPI | **TAKEN** | `hob` 0.3.3 — "A multi-language code generator for the Opera Scope Protocol" |
| GitHub handle | **TAKEN** | |
| `hob.dev` | **TAKEN** | registered 2026-03-12 |
| crates.io | free | |
| `hob.io`, `hob.sh` | free | |
| npm `hob-cli`, `create-hob` | free | |

Short names are the first to go; three letters simultaneously free on npm,
PyPI, GitHub, and `.dev` was never likely. The fallbacks that remain
(`hob-cli`, `create-hob`, `hob.io`) give up precisely the property that made
the name attractive — that it was short and unqualified. A CLI-first tool
whose whole pitch is `npx <name> create agent` cannot afford a qualified
package name as its front door.

So hob was not pursued because the namespace was gone, not because the
metaphor was wrong.

## kaimahi — the current name

**kaimahi** — te reo Māori for *worker*. Chosen as the replacement for
*tomte*, and held provisional until two gates were cleared: a cultural read
by a te reo Māori speaker, and trademark counsel.

FYI, in plain terms:

- **It is a real word in a living language**, not a coined token. That is
  the appeal and the risk: it reads naturally to New Zealand speakers and
  carries meaning that a made-up name would not.
- **The name is settled, and here is exactly on what basis.** Of those
  two gates:
  1. **The cultural read CLEARED.** A te reo Māori speaker was
     comfortable with the use. Te reo Māori is a taonga protected under
     the Treaty of Waitangi and commercial use of Māori words by
     non-Māori projects is a live issue, not a formality — which is why
     this gate came first and why it is recorded rather than assumed.
  2. **Trademark counsel was NOT obtained.** No opinion was taken, and
     none is claimed. This is written plainly so that silence is never
     later mistaken for clearance.
- **What that permits, and what it does not.** Publishing is unblocked
  and free namespaces may be claimed. Nothing here asserts a trademark —
  no ™, no "registered", no claim of exclusivity — and the project stays
  renameable in principle. The residual risk is stated and accepted: a
  later conflict would cost a rename, and every namespace claimed makes
  that rename dearer.
- **`kmx`**, the binary, is settled on the same basis, accepting that PyPI
  `kmx` and the GitHub user `kmx` are already taken and are not ours, and
  that KMX is CarMax's ticker symbol.
- **The repo was renamed ahead of the freeze.** The user renamed it to
  `kaiwahi` — a typo, w/m transposed — which was caught and corrected to
  **gambtho/kaimahi**. It has since moved into a GitHub organization named
  for the project: **kaimahi-agents/kaimahi**. GitHub redirects from the
  old paths (`gambtho/tomte`, `gambtho/kaimahi`) are active.
- **The in-repo rename has landed** (commit `01f5c3c`): README, runbooks,
  Makefile, scripts, `k8s/`, and CI. The kind cluster is now `kaimahi-p1`
  (was `tomte-p1`) and the Copilot token cache moved to
  `~/.config/kaimahi/` — both are one-time local migrations for anyone with
  an existing checkout; see [getting-started.md](getting-started.md) and
  [models.md](models.md). The coordination board was excluded from that
  lane and had its present-tense references renamed separately; historical
  quotes on the board keep the old name verbatim.
- **Renaming the tree was never the same as clearing the name.** The
  rename lane was explicitly scoped to mechanical substitution and kept
  the no-trademark wording; clearance came later and only in part, as
  recorded above.
- **Pronunciation**, for the inevitable meeting: roughly *kigh-MAH-hee*
  (`kai` as in "kite", not "kay").

## Availability, verified 2026-09-01

Checked live against each registry's API, not inferred. **This decays** —
re-verify before acting on it.

| Registry / namespace | `kaimahi` | Notes |
|---|---|---|
| npm `kaimahi` | **free** (404) | |
| npm `create-kaimahi` | **free** (404) | the `npm create` / `npx` convention |
| npm `kaimahi-cli` | **free** (404) | fallback if the bare name is ever contested |
| PyPI `kaimahi` | **free** (404) | |
| crates.io `kaimahi` | **free** (404) | |
| GitHub org/user `kaimahi` | **free** (404) | the bare name; the org actually created is `kaimahi-agents` |
| `kaimahi.dev` | **free** (RDAP 404) | |
| `kaimahi.io` | **free** (RDAP 404) | |
| `kaimahi.ai` | **free** (RDAP 404) | |
| `kaimahi.sh` | **free** (RDAP 404) | |
| `kaimahi.com` | **TAKEN** | registered 2009-04-12, expires 2027-04-12, Tucows; transfer/update locked |

`kaimahi.com` being held since 2009 by an unrelated party is the one
material finding beyond the first availability check, which only looked at
`.dev` and `.io`. It does not block the project name — `.dev` is the
conventional choice here and is free — but it does mean the `.com` is not a fallback, and a
trademark search should account for whoever holds it.

## Previous name: tomte

**tomte** — Scandinavian folklore: a small household spirit that does the
work of the farm at night, in exchange for a bowl of porridge. Apt for the
product; the name was in use through the restart, and through the first
agent on kind and the model path that followed it.

| Registry / namespace | `tomte` | Notes |
|---|---|---|
| npm `tomte` | free (404) | never claimed |
| crates.io `tomte` | free (404) | |
| PyPI `tomte` | **TAKEN** | `tomte` 0.7.2 — a published tooling library, unrelated project |

The PyPI collision is a concrete example of why "the name looked free" is
not a durable position: the same word was already shipping on one of the
three registries a polyglot project would eventually want.

Repo history: the original `gambtho/tomte` was renamed to
[`gambtho/tomte-old`](https://github.com/gambtho/tomte-old) and archived;
the redux was a fresh repo, later renamed to `gambtho/kaimahi`, then moved
into the `kaimahi-agents` organization as
[`kaimahi-agents/kaimahi`](https://github.com/kaimahi-agents/kaimahi).

## Rejected / not pursued

| Name | Why not |
|---|---|
| `kaiwahi` | not a considered candidate — a transposition typo of kaimahi, caught during the repo rename |

## Before claiming anything

1. ~~Clear the two gates~~ — DONE in part: cultural read cleared;
   trademark counsel not taken and not claimed.
2. Re-verify availability the same day (this table decays).
3. Get explicit user approval naming the exact artifact — "register
   kaimahi.dev", "publish npm `create-kaimahi`" — not blanket approval of
   the name.
4. Claim the whole set at once if claiming at all; a half-claimed name is
   worse than an unclaimed one.
