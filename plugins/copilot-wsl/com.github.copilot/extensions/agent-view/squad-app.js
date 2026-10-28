import { el, runtimeModelControl, friendlyName, oneLine } from "./editor.mjs";
import { renderView } from "./app.js";

async function postAction(input) {
  const response = await fetch("actions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input) });
  return response.json();
}

export function renderSquadView(doc, root, data, action = postAction) {
let { team, proposal } = data;
let activeMember, working = false, notice = data.message ?? "", outcome = data.outcome, creation = data.creation, briefDraft;
let generation = 0, disposed = false, renderedTeam;
let goal = proposal?.brief ?? "", proposalOffer = data.proposalOffer;
const memberScreens = new Map();
const clone = (v) => structuredClone(v);
const button = (label, fn, cls = "sb") => { const b = el(doc, "button", cls, label); b.type = "button"; b.disabled = working; b.addEventListener("click", fn); return b; };
const field = (label, value, change, multiline = false) => {
  const wrap = el(doc, "label", "squad-field"), input = el(doc, multiline ? "textarea" : "input");
  input.value = value; input.disabled = working; input.setAttribute("aria-label", label);
  if (multiline) input.rows = 3;
  input.addEventListener("input", () => change(input.value)); wrap.append(el(doc, "span", "lbl", label), input); return wrap;
};
function overview() {
  activeMember = undefined; generation++; renderedTeam = JSON.stringify(team);
  if (team.createBlocked || creation && creation.status !== "refused") proposalOffer = undefined;
  const content = el(doc, "div", "content squad-content"), header = el(doc, "header", "agent-header");
  const row = el(doc, "div", "title-line"); row.append(el(doc, "h1", "", "Squad"), el(doc, "span", "squad-badge", proposal ? "Proposed team" : "Native Squad 1.1.0"));
  header.append(row, el(doc, "p", "purpose", "Shape your team, create it once, and test each member."));
  const meta = el(doc, "div", "squad-chips");
  for (const label of team.access ?? []) meta.append(el(doc, "span", "squad-chip", `${label} · enforced by kmx`));
  header.append(meta, el(doc, "p", "meta squad-root", team.root)); content.append(header);
  if (notice) { const box = el(doc, "p", "squad-notice", notice); box.setAttribute("role", "status"); content.append(box); }
  if (proposalOffer) {
    const suggested = proposalOffer.proposal, members = [suggested.lead, ...suggested.specialists];
    const offer = el(doc, "section", "squad-offer"), heading = el(doc, "div", "title-line");
    heading.append(el(doc, "h2", "", "Suggested team"), el(doc, "span", "meta", `${members.length} members · ${members.reduce((n, m) => n + m.tests.length, 0)} starter tests`));
    offer.append(heading, el(doc, "p", "meta", proposal ? "Your current draft is kept below. Review this suggestion before replacing it." : "Review the roles and starter tests. You can edit the team before creating it."));
    const goal = el(doc, "div", "squad-offer-goal");
    goal.append(el(doc, "span", "lbl", "Team goal"), el(doc, "p", "", suggested.brief)); offer.append(goal);
    members.forEach((m, index) => {
      const card = el(doc, "details", "squad-card squad-offer-member"), summary = el(doc, "summary", "squad-card-summary");
      const description = el(doc, "div", "squad-offer-role");
      description.append(el(doc, "strong", "", m.displayRole), el(doc, "span", "meta", m.model), el(doc, "p", "squad-offer-preview", oneLine(m.taskInstructions, 150)));
      summary.append(el(doc, "span", "squad-avatar", index === 0 ? "L" : String(index)), description, el(doc, "span", "squad-test-count", `${m.tests.length} ${m.tests.length === 1 ? "test" : "tests"}`));
      const body = el(doc, "div", "squad-card-body"), instructions = el(doc, "details", "squad-offer-instructions");
      instructions.append(el(doc, "summary", "lbl", "Full instructions"), el(doc, "p", "meta", `Member ID: ${m.memberId}`), el(doc, "div", "squad-offer-prose", m.taskInstructions));
      body.append(instructions, el(doc, "h3", "lbl", "Starter tests"));
      for (const test of m.tests) {
        const row = el(doc, "details", "squad-offer-test"), title = el(doc, "summary", "squad-offer-test-summary");
        title.append(el(doc, "strong", "", friendlyName(test.id)), el(doc, "span", "meta", `${test.assertions.length} ${test.assertions.length === 1 ? "check" : "checks"} · Not run`));
        const detail = el(doc, "div", "squad-offer-test-detail"), checks = el(doc, "ul", "squad-offer-checks");
        detail.append(el(doc, "span", "lbl", "The agent gets"), el(doc, "pre", "squad-offer-input", test.input), el(doc, "span", "lbl", "Passes if"));
        for (const check of test.assertions) {
          const item = el(doc, "li"), labels = { contains: "Answer includes", notContains: "Answer does not include", regex: "Answer matches this pattern", llmJudge: "Answer meets these criteria" };
          const value = check.type === "regex" ? check.pattern : check.type === "llmJudge" ? check.rubric : check.value;
          item.append(el(doc, "span", "squad-offer-check-label", labels[check.type] ?? check.type), el(doc, check.type === "llmJudge" ? "p" : "code", "squad-offer-check-value", value));
          if (check.type === "llmJudge") item.append(el(doc, "span", "meta", `Minimum score: ${check.minScore} · Judge: ${check.judgeModel || "run model"} · Version: ${check.judgeVersion}`));
          checks.append(item);
        }
        detail.append(checks); row.append(title, detail); body.append(row);
      }
      card.append(summary, body); offer.append(card);
    });
    const actions = el(doc, "div", "squad-offer-actions");
    for (const [label, operation] of [["Use suggested team", "accept-proposal"], ["Keep my draft", "dismiss-proposal"]]) actions.append(button(label, async () => {
      const result = await action({ operation, offerId: proposalOffer.id });
      if (result.error) notice = result.message;
      else { if (result.acceptedProposal) proposal = clone(result.acceptedProposal); proposalOffer = undefined; }
      overview();
    }, operation === "accept-proposal" ? "squad-primary" : "sb"));
    offer.append(actions, el(doc, "p", "meta", "Choosing a suggestion updates your draft. Create squad saves the team; tests run when you request them."));
    content.append(offer);
  }
  if (creation && creation.status !== "complete") {
    const report = el(doc, "section", "squad-setup");
    report.append(el(doc, "strong", "", `Creation ${creation.status}`), el(doc, "p", "", `Setup: ${outcome.setup || "not attempted"}. Shared brief: ${outcome.brief ? "saved" : "not confirmed saved"}.`));
    for (const row of outcome.members ?? []) report.append(el(doc, "p", "", `${row.id}: ${row.state}`));
    if (outcome.effectsUnknown) report.append(el(doc, "p", "", "Some effects are unknown. Inspect the project before reconciliation."));
    report.append(button("Refresh team", refreshTeam)); content.append(report);
  }
  if (proposalOffer && proposal) {
    const current = el(doc, "details", "squad-current-draft"), body = el(doc, "div", "squad-current-draft-body");
    current.append(el(doc, "summary", "meta", "Your current draft")); renderProposal(body); current.append(body); content.append(current);
  } else if (!proposalOffer) {
    if (proposal) renderProposal(content); else renderTeam(content);
  }
  content.append(el(doc, "p", "meta squad-footnote", "Tests are quick member-level checks. Waza/package-digest evaluation and team runtime execution are separate integrations."));
  root.replaceChildren(content);
}
function renderProposal(content) {
  content.append(field("Shared team brief", proposal.brief, (value) => { proposal.brief = value; }, true));
  const heading = el(doc, "div", "title-line"); heading.append(el(doc, "h2", "", "Proposed team"));
  const controls = el(doc, "div", "squad-actions");
  controls.append(button("Suggest changes", () => askCopilot(proposal.brief, proposal)));
  controls.append(button("+ Add role", () => {
    if (proposal.specialists.length === 7) return;
    let n = 1; const ids = new Set([proposal.lead, ...proposal.specialists].map(m => m.memberId));
    while (ids.has(`specialist-${n}`)) n++;
    proposal.specialists.push({ memberId: `specialist-${n}`, role: "Specialist", displayRole: "Specialist", model: proposal.lead.model, taskInstructions: "", tests: [{ id: "responsibility", input: "", assertions: [{ id: "required-behavior", type: "contains", value: "" }] }] }); overview();
  })); heading.append(controls); content.append(heading);
  [proposal.lead, ...proposal.specialists].forEach((m, index) => {
    const card = el(doc, "details", "squad-card"), summary = el(doc, "summary", "squad-card-summary");
    const title = el(doc, "strong", "", m.displayRole);
    summary.append(el(doc, "span", "squad-avatar", index === 0 ? "L" : String(index)), title, el(doc, "span", "meta", `${m.model} · ${m.tests.length} tests`)); card.append(summary);
    const body = el(doc, "div", "squad-card-body");
    if (index) body.append(field("Role", m.displayRole, (value) => { m.role = value; m.displayRole = value; title.textContent = value; }));
    body.append(field("Member ID", m.memberId, (value) => { m.memberId = value; }), field("Task instructions / persona", m.taskInstructions, (value) => { m.taskInstructions = value; }, true), field("Preferred model", m.model, (value) => { m.model = value; }));
    for (const test of m.tests) {
      const block = el(doc, "div", "squad-test-draft"); block.append(el(doc, "strong", "", test.id.replaceAll("-", " ")), field("The agent gets", test.input, (value) => { test.input = value; }));
      for (const check of test.assertions) { const key = check.type === "regex" ? "pattern" : check.type === "llmJudge" ? "rubric" : "value"; block.append(field(`Passes if · ${check.type}`, check[key], (value) => { check[key] = value; })); }
      body.append(block);
    }
    if (index) body.append(button("Remove proposed member", () => { proposal.specialists.splice(index - 1, 1); overview(); }, "tb squad-remove"));
    card.append(body); content.append(card);
  });
  const setup = el(doc, "div", "squad-setup");
  setup.append(el(doc, "strong", "", "What Create squad will do"), el(doc, "p", "", "Initialize native Squad when needed, including native MCP/skill files and Git attributes, cast the reviewed members, add their task instructions and tests, and save the shared brief in this project."));
  const create = button(working ? "Creating squad…" : "Create squad", async () => {
    if (working) return; working = true; notice = "Creating the reviewed native squad…"; overview();
    try {
      const result = await action({ operation: "create", proposal: clone(proposal) });
      if (result.error) { notice = result.message; if (result.error === "outcome-unknown") { team.uncertain = true; proposalOffer = undefined; } }
      else {
        if (result.team) team = result.team;
        outcome = result.outcome; creation = result.creation;
        if (creation?.status === "complete") proposal = undefined;
        if (creation?.status !== "refused") { team.createBlocked = true; proposalOffer = undefined; }
        if (creation?.status === "unknown") team.uncertain = true;
        notice = result.message || (creation?.status === "complete" ? "Squad created. Open a member to edit its prompt and run its tests." : creation?.status === "refused" ? "Creation was refused before changing the project. Review the proposal and saved state." : "Creation has partial effects. Inspect the outcomes before continuing.");
      }
    } catch { team.uncertain = true; proposalOffer = undefined; notice = "Creation outcome is unknown. Inspect the project before another action."; }
    finally { working = false; overview(); }
  }, "squad-primary"); create.disabled = !!(working || team.uncertain || team.createBlocked); setup.append(create); content.append(setup);
}
async function refreshTeam() {
  const started = ++generation;
  try {
    const result = await action({ operation: "reload" });
    if (disposed || generation !== started) return;
    if (result.team) team = result.team;
    if (result.creation) { creation = result.creation; outcome = result.outcome; }
    if (creation?.status === "complete") proposal = undefined;
    if (result.error || result.refreshUnavailable) notice = result.message;
  } catch { notice = "The team could not be refreshed. Your draft and previous outcomes are still here."; }
  overview();
}
function renderTeam(content) {
  if (!team.members.length) {
    const box = el(doc, "div", "squad-empty");
    const propose = button("Ask Copilot to propose", () => askCopilot(goal), "squad-primary");
    propose.disabled = working || !goal.trim();
    const input = field("What should the team do?", goal, value => { goal = value; propose.disabled = working || !value.trim(); }, true);
    box.append(el(doc, "h2", "", "Build a team from one goal"), input, propose); content.append(box); return;
  }
  const shownBrief = team.brief;
  const briefField = field("Shared team brief", briefDraft?.text ?? shownBrief.text, (value) => {
    briefDraft = value === shownBrief.text ? undefined : { text: value, expectedRevision: briefDraft?.expectedRevision ?? shownBrief.revision };
  }, true);
  briefField.append(button("Save brief", async () => {
    if (working || !briefDraft) return;
    const submitted = { ...briefDraft }; working = true; overview();
    try {
      const result = await action({ operation: "save-brief", ...submitted });
      if (result.error) notice = result.message;
      else {
        if (result.team) team = result.team;
        else if (result.saved && result.brief) team = { ...team, brief: result.brief };
        briefDraft = undefined;
        notice = result.message || "Brief saved. Previous member results are out of date; choose a member to test.";
      }
    } catch { notice = "The brief save could not be confirmed. Your draft is still here; refresh before reconciling."; }
    finally { working = false; overview(); }
  })); content.append(briefField);
  if (briefDraft) content.append(el(doc, "p", "meta", "Shared brief has unsaved edits. Member tests use the saved brief."));
  const heading = el(doc, "div", "title-line"); heading.append(el(doc, "h2", "", "Team members"), button("Refresh team", refreshTeam)); content.append(heading);
  for (const m of team.members) {
    const row = el(doc, "div", "squad-card squad-member-row"), info = el(doc, "div");
    const creation = outcome?.members.find((r) => r.id === m.memberId);
    info.append(el(doc, "strong", "", m.profile?.role || m.memberId), el(doc, "p", "meta", m.state === "ready" ? "Native state validated · individual tests available" : "Not set up by kmx"));
    if (creation) info.append(el(doc, "p", "meta", `Creation: ${creation.state}`));
    const open = button("Open member →", () => openMember(m.memberId)); open.disabled = m.state !== "ready" || team.uncertain;
    row.append(info, open); content.append(row);
  }
}
async function askCopilot(text, draft) {
  try { const result = await action({ operation: "propose", goal: text, ...(draft ? { draft: clone(draft) } : {}) }); notice = result.message; }
  catch { notice = "Copilot could not receive the proposal request. Your draft is still here."; }
  overview();
}
async function openMember(memberId) {
  const started = ++generation, existing = memberScreens.get(memberId), observation = existing?.handle.observation();
  const response = await action({ operation: "member", memberId });
  if (disposed || generation !== started) return;
  if (response.error) { notice = response.message; overview(); return; }
  if (existing) {
    existing.handle.observe(response.view, observation);
    root.replaceChildren(existing.shell); activeMember = existing; return;
  }
  let currentView = response.view;
  const shell = el(doc, "div", "squad-member-shell"), bar = el(doc, "div", "squad-member-bar"), mount = el(doc, "div");
  bar.append(button("← Squad", overview, "tb"), el(doc, "span", "meta", "Native member · local preview"));
  const context = el(doc, "details", "squad-inherited"), briefText = el(doc, "pre", "", response.view.brief.text);
  context.append(el(doc, "summary", "meta", "Shared brief and model"), briefText);
  let model = response.view.model;
  let handle, modelSaving = false;
  const modelMessage = el(doc, "p", "meta"); modelMessage.setAttribute("role", "status");
  const picker = runtimeModelControl(doc, () => ({ authored: currentView.model, selected: model, disabled: modelSaving || !handle?.idle() }), () => action({ operation: "models" }), value => { model = value || currentView.model; picker.update(); });
  context.append(el(doc, "span", "lbl", "Preferred model"), picker.node, button("Save model", async () => {
    if (modelSaving || handle?.dirty() || !handle?.idle()) { modelMessage.textContent = "Save your prompt/test edits before changing the model."; return; }
    modelSaving = true; generation++; picker.update();
    try {
      const result = await action({ operation: "member-action", memberId, action: { operation: "save-model", expectedRevision: currentView.file.revision, model } });
      if (result.error) modelMessage.textContent = result.message;
      else { handle.observe(result.view); modelMessage.textContent = "Model saved. Press Enter in the prompt to test it."; }
    } finally { modelSaving = false; picker.update(); }
  }), modelMessage);
  const native = el(doc, "details", "squad-inherited"), nativeText = el(doc, "pre", "", response.view.nativeInstructions);
  native.append(el(doc, "summary", "meta", "Native context (read-only)"), nativeText);
  context.append(native);
  shell.append(bar, context, mount); root.replaceChildren(shell);
  const execute = async (input) => {
    if (!["progress", "models", "suggest"].includes(input.operation)) generation++;
    return action({ operation: "member-action", memberId, action: input });
  };
  const fetchView = async () => (await action({ operation: "member", memberId })).view;
  handle = renderView(doc, mount, response.view, execute, {
    fetchView,
    onView(view) {
      if (model === currentView.model) model = view.model;
      currentView = view;
      briefText.textContent = view.brief.text; nativeText.textContent = view.nativeInstructions;
      picker.update();
    },
    modelSelection: false,
    toolsSelection: false,
    ...(team.previewControl === "run-id-v1" ? { fetchProgress: runId => execute({ operation: "progress", runId }), interruptOnEdit: true } : {}),
  });
  activeMember = { memberId, shell, handle }; picker.update();
  memberScreens.set(memberId, activeMember);
}

const observation = () => ({ generation, member: activeMember, source: activeMember?.handle.observation() });
async function observe(next, started = observation()) {
  if (disposed || working || generation !== started.generation) return;
  if (next.team) team = next.team;
  if (next.creation) { creation = next.creation; outcome = next.outcome; }
  if (creation?.status === "complete") proposal = undefined;
  if (next.proposalOffer && !team.createBlocked && !team.members.length && next.proposalOffer.id !== proposalOffer?.id) { proposalOffer = next.proposalOffer; overview(); return; }
  const entry = started.member;
  if (entry && activeMember === entry) {
    if (team.members.find(m => m.memberId === entry.memberId)?.state !== "ready") {
      notice = "This member is not set up by kmx. Refresh after its native state is restored; your draft is kept."; overview(); return;
    }
    const response = await action({ operation: "member", memberId: entry.memberId });
    if (disposed || working || generation !== started.generation || activeMember !== entry) return;
    if (response.error) entry.handle.observeFailure(response, started.source);
    else entry.handle.observe(response.view, started.source);
  } else if (JSON.stringify(team) !== renderedTeam && !(root.contains(doc.activeElement) && ["INPUT", "TEXTAREA"].includes(doc.activeElement.tagName))) overview();
}

overview();
return { observation, observe, observeOffer(offer) { if (offer && offer.id !== proposalOffer?.id && !working && !team.createBlocked && !team.members.length) { proposalOffer = offer; overview(); } }, dispose() { disposed = true; generation++; for (const entry of memberScreens.values()) entry.handle.dispose(); memberScreens.clear(); } };
}

async function main() {
  const root = document.getElementById("squad-root");
  try {
    const data = await (await fetch("view.json", { cache: "no-store" })).json();
    if (!data.team) throw new Error(data.message || "Native Squad is unavailable.");
    const app = renderSquadView(document, root, data);
    let reading = false;
    setInterval(async () => {
      if (reading || document.visibilityState !== "visible") return;
      reading = true;
      const started = app.observation();
      try { const next = await (await fetch("view.json", { cache: "no-store" })).json(); await app.observe(next, started); }
      catch { /* The current draft stays visible until a successful read. */ }
      finally { reading = false; }
    }, 2000);
  } catch (error) { root.replaceChildren(el(document, "p", "error", error.message)); }
}
if (typeof document !== "undefined" && document.getElementById("squad-root")) main();
