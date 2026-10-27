import { el, runtimeModelControl } from "./editor.mjs";
import { renderView } from "./app.js";
import { fieldServiceProposal } from "./squad-examples.mjs";

async function postAction(input) {
  const response = await fetch("actions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input) });
  return response.json();
}

export function renderSquadView(doc, root, data, action = postAction) {
let { team, proposal } = data;
let memberHandle, working = false, notice = data.message ?? "", outcome = data.outcome, creation = data.creation, briefDraft;
let example = false;
let exampleModel = "fake", goal = proposal?.brief ?? "Create a squad to help field technicians work without internet and synchronize safely later.", proposalOffer = data.proposalOffer;
const memberScreens = new Map();
const clone = (v) => structuredClone(v);
const button = (label, fn, cls = "sb") => { const b = el(doc, "button", cls, label); b.type = "button"; b.disabled = working; b.addEventListener("click", fn); return b; };
const field = (label, value, change, multiline = false) => {
  const wrap = el(doc, "label", "squad-field"), input = el(doc, multiline ? "textarea" : "input");
  input.value = value; input.disabled = working; input.setAttribute("aria-label", label);
  if (multiline) input.rows = 3;
  input.addEventListener("input", () => change(input.value)); wrap.append(el(doc, "span", "lbl", label), input); return wrap;
};
function exampleProposal() {
  const member = (memberId, role, answer, checks) => ({ memberId, role, displayRole: role, model: "fake", taskInstructions: `Reply with exactly \`${answer}\`.`, tests: checks.map(([id, input, value]) => ({ id, input, assertions: [{ id: "required-text", type: "contains", value }] })) });
  return {
    brief: "Add offline mode to our field-service app. Technicians must keep working without connectivity. Preserve local changes and synchronize safely when the connection returns.",
    lead: { ...member("lead", "lead", "Plan offline storage, conflict-safe synchronization and recovery tests.", [["covers-storage", "What should the team deliver?", "offline storage"], ["covers-sync", "What should the team deliver?", "synchronization"]]), displayRole: "Lead / Architect" },
    specialists: [
      member("offline-storage", "Offline storage", "Queue writes locally; synchronize on reconnect.", [["preserves-writes", "A technician saves a job without connectivity.", "Queue writes locally"], ["avoids-loss", "Two edits conflict when reconnecting.", "preserve both edits"]]),
      member("sync", "Synchronization", "Use idempotent sync and preserve both edits when conflicts occur.", [["safe-retry", "The connection drops during synchronization.", "idempotent"], ["conflict-safety", "Two technicians edit the same job.", "preserve both edits"]]),
      member("quality", "Quality / testing", "Test airplane mode, reconnect, duplicate sync and conflict recovery.", [["airplane-mode", "How do we qualify offline behavior?", "airplane mode"], ["recovery", "What happens after a dropped connection?", "conflict recovery"]]),
    ],
  };
}

function overview() {
  memberHandle = undefined;
  const content = el(doc, "div", "content squad-content"), header = el(doc, "header", "agent-header");
  const row = el(doc, "div", "title-line"); row.append(el(doc, "h1", "", example ? "Field Service App Development Team" : "Squad"), el(doc, "span", "squad-badge", proposal ? "Proposed team" : "Native Squad 1.1.0"));
  header.append(row, el(doc, "p", "purpose", example ? "Help technicians work without internet and sync changes when they reconnect." : "Shape your team, create it once, and test each member."));
  const meta = el(doc, "div", "squad-chips");
  for (const label of team.access ?? []) meta.append(el(doc, "span", "squad-chip", `${label} · enforced by kmx`));
  header.append(meta, el(doc, "p", "meta squad-root", team.root)); content.append(header);
  if (notice) { const box = el(doc, "p", "squad-notice", notice); box.setAttribute("role", "status"); content.append(box); }
  if (proposalOffer) {
    const offer = el(doc, "section", "squad-setup");
    offer.append(el(doc, "strong", "", "Copilot proposed a team update"), el(doc, "p", "", "Your current draft is kept. Review the suggested brief, roles and tests before replacing it."));
    const details = el(doc, "details"); details.append(el(doc, "summary", "", "Review suggested team"), el(doc, "pre", "", proposalOffer.proposal.brief));
    for (const m of [proposalOffer.proposal.lead, ...proposalOffer.proposal.specialists]) details.append(el(doc, "h3", "", `${m.displayRole} · ${m.model}`), el(doc, "pre", "", m.taskInstructions), el(doc, "pre", "", JSON.stringify(m.tests, null, 2)));
    offer.append(details);
    for (const [label, operation] of [["Use suggested team", "accept-proposal"], ["Keep my draft", "dismiss-proposal"]]) offer.append(button(label, async () => {
      const result = await action({ operation, offerId: proposalOffer.id });
      if (result.error) notice = result.message;
      else { if (result.acceptedProposal) { proposal = clone(result.acceptedProposal); example = false; } proposalOffer = undefined; }
      overview();
    }));
    content.append(offer);
  }
  if (creation && creation.status !== "complete") {
    const report = el(doc, "section", "squad-setup");
    report.append(el(doc, "strong", "", `Creation ${creation.status}`), el(doc, "p", "", `Setup: ${outcome.setup || "not attempted"}. Shared brief: ${outcome.brief ? "saved" : "not confirmed saved"}.`));
    for (const row of outcome.members ?? []) report.append(el(doc, "p", "", `${row.id}: ${row.state}`));
    if (outcome.effectsUnknown) report.append(el(doc, "p", "", "Some effects are unknown. Inspect the project before reconciliation."));
    report.append(button("Refresh team", refreshTeam)); content.append(report);
  }
  if (proposal) renderProposal(content);
  else renderTeam(content);
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
  if (example) setup.append(el(doc, "p", "meta", exampleModel === "fake" ? "This example uses the deterministic fake model. It demonstrates native authoring and local checks." : `Tests use the real ${exampleModel} model through the configured provider. Testing sends prompts and test inputs to that provider.`));
  const create = button(working ? "Creating squad…" : "Create squad", async () => {
    if (working) return; working = true; notice = "Creating the reviewed native squad…"; overview();
    try {
      const result = await action({ operation: "create", proposal: clone(proposal) });
      if (result.error) { notice = result.message; if (result.error === "outcome-unknown") team.uncertain = true; }
      else {
        if (result.team) team = result.team;
        outcome = result.outcome; creation = result.creation;
        if (creation?.status === "complete") proposal = undefined;
        if (creation?.status !== "refused") team.createBlocked = true;
        if (creation?.status === "unknown") team.uncertain = true;
        notice = result.message || (creation?.status === "complete" ? "Squad created. Open a member to edit its prompt and run its tests." : creation?.status === "refused" ? "Creation was refused before changing the project. Review the proposal and saved state." : "Creation has partial effects. Inspect the outcomes before continuing.");
      }
    } catch { team.uncertain = true; notice = "Creation outcome is unknown. Inspect the project before another action."; }
    finally { working = false; overview(); }
  }, "squad-primary"); create.disabled = !!(working || team.uncertain || team.createBlocked); setup.append(create); content.append(setup);
}
async function refreshTeam() {
  try {
    const result = await action({ operation: "reload" });
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
    box.append(el(doc, "h2", "", "Build a team from one goal"), field("What should the team do?", goal, value => { goal = value; }, true), button("Ask Copilot to propose", () => askCopilot(goal)), button("Use field-service app example", async () => {
      if (team.previewProvider === "copilot") {
        const models = await action({ operation: "models" });
        if (models.error) { notice = models.message; overview(); return; }
        exampleModel = models.defaultModel; proposal = fieldServiceProposal(exampleModel);
      } else { exampleModel = "fake"; proposal = exampleProposal(); }
      example = true; notice = "Example proposal. Review the role cards and tests before creating."; overview();
    }, "squad-primary")); content.append(box); return;
  }
  const briefField = field("Shared team brief", briefDraft?.text ?? team.brief.text, (value) => {
    briefDraft = value === team.brief.text ? undefined : { text: value, expectedRevision: briefDraft?.expectedRevision ?? team.brief.revision };
  }, true);
  briefField.append(button("Save brief", async () => {
    if (working || !briefDraft) return;
    const submitted = { ...briefDraft }; working = true; overview();
    try {
      const result = await action({ operation: "save-brief", ...submitted });
      if (result.error) notice = result.message;
      else { team = result.team; briefDraft = undefined; notice = "Brief saved. Previous member results are out of date; choose a member to test."; }
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
  const response = await action({ operation: "member", memberId });
  if (response.error) { notice = response.message; overview(); return; }
  const existing = memberScreens.get(memberId);
  if (existing) {
    existing.handle.observe(response.view);
    existing.briefText.textContent = response.view.brief.text;
    root.replaceChildren(existing.shell); memberHandle = existing.handle; return;
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
    modelSaving = true; picker.update();
    try {
      const result = await action({ operation: "member-action", memberId, action: { operation: "save-model", expectedRevision: currentView.file.revision, model } });
      if (result.error) modelMessage.textContent = result.message;
      else { currentView = result.view; handle.observe(result.view); modelMessage.textContent = "Model saved. Press Enter in the prompt to test it."; }
    } finally { modelSaving = false; picker.update(); }
  }), modelMessage);
  const native = el(doc, "details", "squad-inherited");
  native.append(el(doc, "summary", "meta", "Native context (read-only)"), el(doc, "pre", "", response.view.nativeInstructions));
  context.append(native);
  shell.append(bar, context, mount); root.replaceChildren(shell);
  const execute = async (input) => { const result = await action({ operation: "member-action", memberId, action: input }); if (result.view) currentView = result.view; return result; };
  const fetchView = async () => (await action({ operation: "member", memberId })).view;
  handle = renderView(doc, mount, response.view, execute, {
    fetchView,
    modelSelection: false,
    toolsSelection: false,
    ...(team.previewControl === "run-id-v1" ? { fetchProgress: runId => execute({ operation: "progress", runId }), interruptOnEdit: true } : {}),
  });
  memberHandle = handle; picker.update();
  memberScreens.set(memberId, { shell, handle, briefText });
}

overview();
return { observeOffer(offer) { if (offer && offer.id !== proposalOffer?.id && !working) { proposalOffer = offer; overview(); } }, dispose() { for (const entry of memberScreens.values()) entry.handle.dispose(); memberScreens.clear(); } };
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
      try { const next = await (await fetch("view.json", { cache: "no-store" })).json(); app.observeOffer(next.proposalOffer); }
      catch { /* The current draft stays visible until a successful read. */ }
      finally { reading = false; }
    }, 2000);
  } catch (error) { root.replaceChildren(el(document, "p", "error", error.message)); }
}
if (typeof document !== "undefined" && document.getElementById("squad-root")) main();
