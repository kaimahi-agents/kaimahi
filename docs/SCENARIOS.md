# Agent-first: a scenario document

**Working concept.** The question this document exists to answer:

> "Stop asking users how they want to build an agent. Ask what they want
> their agent to accomplish. Then shim the models, infrastructure, tools,
> identity, governance, and workflows behind an experience that earns trust
> by delivering verified outcomes."

## Provenance

By: Tatsat (Tats), Sajay, Tom, David J.

The genesis of this is doing the leg work for an idea around shimming
implementation-level detail of AI agents against real-world usage. The
original two-pager is not an AI-written document — it came out of interviews
and thinking between a few people. The prose below is theirs; only the
structure and headings were applied to fit it into this repo.

Each scenario has a deeper solution-design document held separately (access
controlled). Those are not linked here — see the note at the end of this
file.

---

## Scenario 1 — Billing: a journey from the customer's point of view

> Can a person delegate a real-world outcome while retaining meaningful
> control at every consequential boundary?

**The trigger.** I open my monthly mobile bill and it is $126 higher than
expected. Today I would download statements, compare usage, call customer
care, authenticate, wait, repeat the problem through transfers, record a
case number, and remember to inspect the next bill. Even a successful call
leaves me responsible for proving that the correction actually happened.

**The delegated outcome.** Instead, I tell an agent: *"Find out why my bill
increased, resolve any incorrect charges, and make sure I do not have to do
this again next month."* The agent reflects the goal back as a proposed
contract: investigate authorized billing and usage records; explain valid
charges; seek correction of errors; ask before any plan, purchase, or
contractual change; and remain active until the next statement verifies the
result.

**The agent flow.** The agent compares six months of bills and isolates $96
in roaming charges that conflict with a recorded roaming block. It presents
the evidence and asks permission to represent me. The provider confirms a
provisioning error and offers a credit. Because the action is within the
agreed boundary, the agent accepts the correction, retains the reference
number, monitors the next bill, and closes the task only after the credit
and restored roaming control are visible.

## Scenario 2 — CNC engineering: a real trust journey from documentation to engineering assistance

> How can an agent earn trust from explanation to code generation without
> becoming an unreviewed path into machine control?

**The backlog.** An engineer had 290 CNC macro files that lacked useful line
comments. Understanding the estate meant opening every file, tracing
variables, branches, machine states, and dependencies, then documenting what
each line and macro appeared to do. The work was important but repetitive,
and the first attempt with Copilot did not work for this corpus or task.

**The first proof.** After Tom recommended trying Claude, the engineer used
it to explain and comment the existing macros. The value did not begin with
autonomous code generation. It began with a bounded, reviewable task: make
legacy logic understandable. As the explanations held up under review, the
engineer gained confidence and avoided what they estimated as dozens, and
potentially more than one hundred, hours of manual notation and analysis.

**Trust expanded through evidence.** The engineer next asked for efficiency
improvements to selected macros, reviewed the proposed edits, and then
trusted the system to draft new macros. The experience later expanded to
programmable logic controller code for the same CNC environment. Each step
increased potential value and potential consequence. The planning insight is
therefore not simply that one model performed better than another; it is
that trust grew from understanding, to recommendation, to controlled
modification, to new creation.

## Scenario 3 — Doctor: a follow-up journey viewed through the patient and nurse experience

**The recurring burden.** A patient managing a long-term illness is told to
return for a follow-up visit or procedure. Today, the next step may require
calling the health centre or hospital, waiting in a queue, confirming which
service is needed, finding a suitable appointment, and calling again if
schedules change. A nurse may need to reserve the slot or coordinate
prerequisites even though the nurse's highest-value time is spent on
assessment, education, and direct patient care.

**The financial uncertainty.** Scheduling alone does not answer the
patient's most practical question: what is the next step likely to cost? The
patient may need to understand whether the clinician and facility are in
network, whether a referral or prior authorization is required, what
deductible or copay may apply, and which parts remain uncertain. Information
can arrive from clinical records, scheduling, and the insurer at different
times, making repeated calls and miscommunication more likely.

**The delegated outcome.** The patient asks an agent: *"Please arrange the
follow-up my care team requested, tell me what I need to do beforehand, and
help me understand what I may have to pay."* With consent, the agent reads
the clinician-approved follow-up instruction, gathers appointment options,
checks network and benefit information, verifies referral and authorization
status, and presents a dated estimate with its sources and limitations. The
patient chooses the slot; the agent books it, sends instructions, tracks
unresolved authorization work, and returns exceptions to the right person.

---

## Questions and observations shared

**Tats — action.** Thinking about the missing part: what was the *experience
of creating* the agentic workflow that made these solution experiences
possible?

Try to wear the hat of the person building the agent:

- Do they need to understand engineering?
- How do they feel empowered and successful?
- How do they tinker and explore?
- How do we make them confident the solution they are building isn't going
  to expose their secrets?

---

## Why this document sits in this repo

*Added for the repo; not part of the original two-pager.*

The three scenarios are outcome stories, but every one of them turns on a
**control boundary** rather than on model capability. Read that way, they
are a requirements document for the governance plane:

| Scenario language | Control it implies |
|---|---|
| "ask before any plan, purchase, or contractual change" | approvals, scoped to exactly what was approved |
| "remain active until the next statement verifies the result" | durable tasks and audit — the outcome, not the response, is the completion criterion |
| "asks permission to represent me" | delegated identity with an explicit, revocable boundary |
| "a bounded, reviewable task" then "controlled modification" | blast-radius permits that widen only as trust is evidenced |
| "without becoming an unreviewed path into machine control" | egress and action enforcement — the hard stop |
| "with consent" / "a dated estimate with its sources and limitations" | provenance and audit: who ran what, on which data, at what cost |

None of this is model selection. It is the plane this project is incubating
(phase 4), and these scenarios are the case for why it comes before more
capability.

Tats's action item points at the other half, and at the gap this repo is
closest to filling: **the builder's experience.** The scenarios describe the
end user delegating an outcome; nothing yet describes the person who has to
assemble, test, and be accountable for the agent that delivers it. The
last question in that list — *how do we make them confident the solution
they are building isn't going to expose their secrets?* — is the same
question that decides the shape of the scaffolder CLI, and is treated at
length in `docs/CLI-PROPOSAL.md` (proposed separately). Today's answer in
this repo is narrow but real: a key is typed and never written down — at a
terminal prompt with the echo off for the three tool upstreams
`kmx credential capture` can check a value against, on stdin for every
other capture script — and it lives in a Kubernetes Secret, never reaching
agent pods, YAML, argv, or logs.

## Note on the source documents

Each scenario has a deeper solution-design document. Those links are
deliberately **not** committed: this repository is public, and the links are
access-controlled share URLs. Anyone who needs them should ask one of the
authors.
