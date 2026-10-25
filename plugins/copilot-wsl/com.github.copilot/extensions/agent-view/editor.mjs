// Inert DOM editing and exact source serialization. Loaders and actions are caller-owned.
import { effectiveModel, preRouteRefusal, DEFAULT_MODEL, agentName, fileLocation, same, uniqueIndex, operandKey, EDITABLE_TYPES, quoted } from "./authoring.mjs";
export { clone, same, uniqueIndex, operandKey, EDITABLE_TYPES, quoted, checkIssue, checkComplete, convertCheck } from "./authoring.mjs";
export const el = (doc, tag, className, text) => {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};
export const checkKey = (a) => JSON.stringify([a.id, a.type]);
export function inspectCases(payload) {
  const r = payload.latest.status === "current" ? payload.latest.result : undefined;
  const authored = uniqueIndex(payload.examples, (e) => e.id);
  const observed = uniqueIndex(r?.examples ?? [], (e) => e.id);
  const selected = uniqueIndex(r?.selection.caseIds ?? [], (id) => id);
  return payload.examples.map((e) => {
    const base = { example: e, state: "waiting", label: payload.latest.status === "stale" ? "Out of date" : "No current result" };
    if (!r) return base;
    if (!selected.has(e.id) && r.selection.scope !== "full") return { ...base, label: "Not in this run" };
    const row = authored.get(e.id) === e && selected.get(e.id) === e.id ? observed.get(e.id) : undefined;
    const unknown = { ...base, state: "unknown", label: "Incomplete", row };
    if (!row) return unknown;
    const expected = uniqueIndex(e.assertions, checkKey);
    const checks = uniqueIndex(row.assertions, checkKey);
    const ambiguous = [...expected.values(), ...checks.values()].some((v) => v === null);
    const matched = e.assertions.every((a) => checks.get(checkKey(a))?.verdict === "pass");
    const covered = expected.size === checks.size && [...checks.keys()].every((key) => expected.has(key));
    const complete = row.state === "complete";
    let state = "unknown";
    if (!ambiguous && complete && row.verdict === "fail") state = "fail";
    if (!ambiguous && complete && row.verdict === "pass" && covered && matched) state = "pass";
    return { ...unknown, state, label: state === "pass" ? "Passed" : state === "fail" ? "Failed" : "Incomplete", checks, expected };
  });
}

export const friendlyName = (id) => {
  const text = id.replace(/[-_]+/g, " ");
  return text ? text[0].toUpperCase() + text.slice(1) : "Unnamed test";
};
const fieldValue = (a) => new Map(a.fields).get(operandKey(a.type)) ?? "";
export const checkLabel = (a) => {
  const value = fieldValue(a);
  return a.type === "contains" ? `Contains ${quoted(value)}` : a.type === "notContains" ? `Not contains ${quoted(value)}`
    : a.type === "regex" ? `Matches regex ${quoted(value)}` : a.type === "llmJudge" ? `${value} · judge` : `Condition: ${a.type}`;
};
// Repair suggestions use only the actual excerpt. A differing reference is
// named only when the answer holds exactly one same-anchored phrase, e.g. the
// expected "Fixes #218" against an answer that literally says "Closes #218".
export const oneLine = (text, max = 80) => {
  const flat = text.replace(/\s+/g, " ").trim(), chars = Array.from(flat);
  return chars.length > max ? chars.slice(0, max - 1).join("") + "…" : flat;
};
export function provenActual(expected, excerpt) {
  const m = /^(\S+)\s+(\S*\d\S*)$/.exec(expected ?? "");
  if (!m || typeof excerpt !== "string") return;
  const anchor = m[2].replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const found = new Set();
  for (const hit of excerpt.matchAll(new RegExp(`(?:^|\\s)(\\S+)\\s+${anchor}(?=$|[\\s.,;:!?)\\]])`, "g"))) {
    const word = hit[1].replace(/^[^\p{L}\p{N}]+/u, "");
    if (word) found.add(`${word} ${m[2]}`);
  }
  const [only] = found;
  return found.size === 1 && only !== expected && excerpt.split(only).length === 2 ? only : undefined;
}
export function failureFacts(c, assertion) {
  if (c.state !== "fail") return;
  for (const a of assertion ? [assertion] : c.example.assertions) {
    const got = c.expected?.get(checkKey(a)) === a ? c.checks?.get(checkKey(a)) : undefined;
    if (got?.verdict !== "fail" || a.type === "llmJudge") continue;
    const value = fieldValue(a), excerpt = c.row?.answerExcerpt;
    const actual = a.type === "contains" ? provenActual(value, excerpt) : undefined;
    const text = a.type === "contains" ? `Missing "${value}"` : a.type === "notContains" ? `Found "${value}"`
      : a.type === "regex" ? `No match for ${value}` : `Not met: ${checkLabel(a)}`;
    return { assertion: a, text, actual, value };
  }
}

// Highlight only an observed literal, never a guessed regex span or an
// ambiguous repeated occurrence. Attribution is the same exact check identity.
export function matchedAnswer(c) {
  const excerpt = c?.row?.answerExcerpt;
  if (c?.state !== "pass" || c.row?.state !== "complete" || typeof excerpt !== "string") return;
  for (const a of c.example.assertions) {
    if (a.type !== "contains" || c.expected?.get(checkKey(a)) !== a || c.checks?.get(checkKey(a))?.verdict !== "pass") continue;
    const value = fieldValue(a), start = excerpt.indexOf(value);
    if (value && start >= 0 && excerpt.indexOf(value, start + 1) < 0) return value;
  }
}

// Engine diagnostics are copy, never instructions or execution inputs. Unknown
// codes cannot smuggle raw provider prose into the canvas.
const PLAIN_REASONS = {
  checks_passed: "All selected tests passed.", assertion_satisfied: "Check met.", completion_ready: "The answer is ready.",
  assertion_failed: "Check not met.", judge_unknown: "The judge could not decide.", not_run: "This test did not run.",
  cancelled: "The run was cancelled.", deadline_exceeded: "The run ran out of time.",
  invalid_file: "This agent file needs fixing before it can run.", no_tests: "No tests yet — add or suggest one",
  invalid_selection: "This test is no longer in the agent file.", invalid_options: "This run's settings are not supported.",
  model_unavailable: "The selected model is not available for this run.", tools_unavailable: "This run cannot use the agent's tools.",
  unsafe_tool_content: "Tool content was withheld before it could be saved.", credential_content: "Content containing credentials was withheld.",
  runner_unavailable: "The agent could not start. No model was called.", execution_failed: "The agent could not finish this run.",
  invalid_completion: "The answer could not be confirmed for this run.", diagnostics_failed: "Run details could not be saved.",
  journal_failed: "The run could not be saved, so its result is unconfirmed.",
  recording_mismatch: "Recording mismatch: tool arguments differ from the saved call. Update the saved call before retrying.",
  copilot_auth_required: "Sign in to GitHub Copilot before running this agent.", copilot_auth_revoked: "Sign in to GitHub Copilot again before running this agent.",
  copilot_no_subscription: "An active GitHub Copilot subscription is needed for this run.", copilot_policy_denied: "Your Copilot policy does not allow this run.",
  copilot_quota: "Copilot's usage limit was reached.", copilot_model_unavailable: "Choose an available Copilot model.",
  copilot_malformed: "Copilot returned an answer that could not be used.", copilot_keychain_unavailable: "Copilot sign-in could not be read. Use an existing GitHub sign-in.",
  copilot_unavailable: "Copilot could not complete this run.",
};
export function plainReason(row) {
  if (row?.reasonCode === "copilot_model_unavailable" && row.reason?.startsWith("Available Copilot models: ")) {
    const ids = row.reason.slice("Available Copilot models: ".length).split(", ");
    if (ids.every(id => /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$/.test(id) && !id.includes("://") && id.toLowerCase() !== "auto")) return row.reason;
  }
  return Object.hasOwn(PLAIN_REASONS, row?.reasonCode) ? PLAIN_REASONS[row.reasonCode] : "The result could not be confirmed.";
}

// Copilot's owned service refusals use state:error even before an answer exists.
// Empty results alone say nothing about whether execution was attempted.
export function isRefused(result) {
  if (!result || result.verdict !== "unknown" || result.fullPass !== false || result.examples?.some(row => row.state === "complete") || result.adHoc?.state === "complete") return false;
  return result.state === "refused" || result.state === "error" && [
    "model_unavailable", "copilot_auth_required", "copilot_auth_revoked", "copilot_no_subscription",
    "copilot_policy_denied", "copilot_quota", "copilot_model_unavailable", "copilot_keychain_unavailable",
  ].includes(result.reasonCode);
}

// One line per authored test. A local edit keeps the known shape and count but
// marks it old; missing, unselected or stale observations never become verdicts.
export const resultTone = kind => ["pass", "fail", "error", "running"].includes(kind) ? kind : "none";
export function testRow(c, local, blocked, malformed = false) {
  if (malformed) return { kind: "none", tone: resultTone("none"), base: "none", label: "Incomplete", note: "Incomplete result", count: "incomplete" };
  if (blocked) return { kind: "none", tone: resultTone("none"), base: "none", label: "Result unconfirmed", note: "" };
  if (!c) return { kind: "none", tone: resultTone("none"), base: "none", label: "Not run", note: "", count: "" };
  let base = "none", label = c.label, note = "", count = "";
  if (c.state === "pass") [base, label, count] = ["pass", "Passed", "passing"];
  else if (c.state === "fail") {
    const judgeFailed = c.example.assertions.some(a => a.type === "llmJudge" && c.expected?.get(checkKey(a)) === a && c.checks?.get(checkKey(a))?.verdict === "fail");
    [base, label, note, count] = ["fail", "Failed", failureFacts(c)?.text || (judgeFailed ? "Judge check not met" : "Check not met."), "failing"];
  }
  else if (c.state === "refused") [base, label, note, count] = ["error", "Refused", plainReason(c.row), "refused"];
  // Only an observed unfinished row earns the amber "!"; ambiguous or uncovered complete rows stay neutral.
  else if (c.state === "unknown" && c.row?.state !== undefined && c.row.state !== "complete") {
    [base, label, count] = ["error", "Didn't finish", "didn't finish"];
    note = plainReason(c.row);
  } else if (c.state === "unknown") [label, note, count] = ["Incomplete", "Incomplete result", "incomplete"];
  else if (c.label === "Not in this run") count = "not run";
  const old = local && base !== "none", kind = old ? `old-${base}` : base;
  return { kind, tone: resultTone(kind), base, label: old ? `${label} last run` : label, note, count };
}
export function coherentSelection(payload) {
  const result = payload.latest.result;
  if (!result) return false;
  const selected = result.selection.caseIds, known = new Set(payload.examples.map((e) => e.id));
  const observed = result.examples.map((e) => e.id);
  return result.selection.totalCases === payload.examples.length && known.size === payload.examples.length
    && new Set(selected).size === selected.length && selected.every((id) => known.has(id))
    && new Set(observed).size === observed.length && observed.every((id) => selected.includes(id))
    && ["full", "subset"].includes(result.selection.scope);
}
export function ambiguousPassSet(payload) {
  return payload.latest.status === "current" && !coherentSelection(payload)
    && inspectCases(payload).every((c) => c.state === "pass");
}
export function countText(rows) {
  const counts = new Map();
  for (const r of rows) counts.set(r.count, (counts.get(r.count) ?? 0) + 1);
  return ["failing", "passing", "refused", "didn't finish", "incomplete", "not run"]
    .filter((k) => counts.get(k)).map((k) => `${counts.get(k)} ${k}`).join(" · ");
}
export const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
// Last-run display is controller-local, never a current resource or write input.
export function resultHistory(initial) {
  let last, preferred, focused;
  const records = new Map(), models = new Map(), attempts = new Map(), timings = new Map(), modelCases = new Map();
  const modelId = view => view.latest.result?.modelRoute.slice(view.latest.result.modelRoute.indexOf("/") + 1);
  const pruneAttempts = () => {
    // Keep newer attempts for both full-result slots, plus one transient model.
    const retained = new Set([...models.values()].map(modelId));
    while (attempts.size > 3) attempts.delete([...attempts.keys()].find(id => !retained.has(id)));
  };
  const remember = (view, executed = false, elapsedMs, attemptedModel) => {
    const definitions = uniqueIndex(view.authoring?.examples ?? [], e => e.id);
    // Bound memory to this file's remaining case IDs, not every past revision.
    for (const id of records.keys()) if (!definitions.get(id)) records.delete(id);
    for (const [route, prior] of models) if (prior.authoring?.examples.some(e => !same(definitions.get(e.id), e))) models.delete(route);
    if (view.latest.status !== "current" || !coherentSelection(view)) return;
    if (view.latest.result.modelRoute === "/") {
      if (executed && attemptedModel && preRouteRefusal(view.latest.result)) { attempts.set(attemptedModel, { view }); pruneAttempts(); }
      return;
    }
    const id = modelId(view), stopped = attempts.get(id);
    if (executed || stopped?.view && !stopped.run && !same(stopped.view.latest.result, view.latest.result)) attempts.delete(id);
    else if (stopped?.run?.revision === view.file.revision) return;
    const route = view.latest.result.modelRoute, changed = !same(models.get(route)?.latest.result, view.latest.result);
    if (!models.has(route) && models.size === 2 && (view.latest.result.selection.scope !== "full" || view.latest.result.state !== "complete")) {
      attempts.set(id, { view });
      pruneAttempts();
    } else {
      models.set(route, view);
      if (models.size > 2) models.delete([...models.keys()].find(key => key !== route && modelId(models.get(key)) !== (focused === undefined || id === focused ? preferred : focused))
        ?? [...models.keys()].find(key => key !== route));
    }
    if (executed && Number.isFinite(elapsedMs)) timings.set(route, elapsedMs);
    else if (executed || changed) timings.delete(route);
    for (const key of timings.keys()) if (!models.has(key)) timings.delete(key);
    for (const c of inspectCases(view)) {
      const definition = definitions.get(c.example.id);
      if (definition && c.row) {
        const record = { definition, c, model: route };
        records.set(c.example.id, record); modelCases.set(`${route}\0${c.example.id}`, record);
      }
    }
    for (const [key, record] of modelCases) if (!definitions.get(record.c.example.id) || !models.has(record.model) && ![...attempts.values()].some(a => a.view.latest.result?.modelRoute === record.model)) modelCases.delete(key);
    if (view.latest.result.examples.length) last = view;
  };
  remember(initial);
  return {
    get last() { return last; }, remember,
    selected(view, model) {
      const attempt = attempts.get(model);
      if (attempt?.run?.revision === view.file.revision) return;
      if (attempt?.view.file.revision === view.file.revision) return { ...view, latest: attempt.view.latest };
      const matches = [...models.values()].filter(v => modelId(v) === model && v.file.revision === view.file.revision);
      return matches.length === 1 ? { ...view, latest: matches[0].latest } : undefined;
    },
    pair: () => [...models.values()],
    prefer: model => { preferred = model; },
    focus: model => { focused = model; },
    started: model => { attempts.delete(model); },
    elapsed: view => timings.get(view.latest.result?.modelRoute),
    attempt: model => attempts.get(model),
    stop(run, view) {
      attempts.set(run.model, { run, view });
      pruneAttempts();
    },
    reset() { attempts.clear(); models.clear(); records.clear(); modelCases.clear(); timings.clear(); last = undefined; },
    cases(view, selected) {
      const current = view.latest.status === "current", coherent = coherentSelection(view);
      const definitions = uniqueIndex(view.authoring?.examples ?? [], e => e.id), identities = uniqueIndex(view.examples, e => e.id);
      return inspectCases(view).map(c => {
        if (identities.get(c.example.id) !== c.example || current && (!coherent || c.label !== "Not in this run")) return c;
        const route = [...models.keys()].find(route => route.slice(route.indexOf("/") + 1) === selected);
        const prior = modelCases.get(`${route}\0${c.example.id}`) ?? records.get(c.example.id), definition = definitions.get(c.example.id);
        return prior && definition && same(prior.definition, definition)
          ? { ...prior.c, example: c.example, expected: uniqueIndex(c.example.assertions, checkKey), previous: true, previousModel: prior.model } : c;
      });
    },
  };
}
// Presentation inferred from familiar ID shapes, never catalog metadata or
// execution aliases. Unknown IDs remain literal, including provider prefixes.
export function modelPresentation(id) {
  let match = /^(gpt-\d+(?:\.\d+)*)(?:-(terra|sol|mini|nano|codex))?$/.exec(id);
  if (match) return { name: match[1].replace("gpt", "GPT") + (match[2] ? " " + friendlyName(match[2]) : ""), group: "OpenAI" };
  match = /^claude-(sonnet|haiku|opus)-(\d+(?:\.\d+)*)$/.exec(id);
  if (match) return { name: `Claude ${friendlyName(match[1])} ${match[2]}`, group: "Claude" };
  match = /^gemini-(\d+(?:\.\d+)*)-(pro|flash)$/.exec(id);
  return match ? { name: `Gemini ${match[1]} ${friendlyName(match[2])}`, group: "Gemini" } : { name: id, group: "Other models" };
}
export const modelLabel = route => modelPresentation(route?.slice(route.indexOf("/") + 1) ?? "").name;
export function compareLabels(routes) {
  const names = routes.map(modelLabel);
  const labels = names.map((name, i) => {
    if (modelPresentation(routes[i].slice(routes[i].indexOf("/") + 1)).group === "Other models") return name;
    const other = names[1 - i] ?? "", family = /\b(Terra|Sol|Mini|Nano|Codex|Sonnet|Haiku|Opus|Pro|Flash)\b/.exec(name)?.[0];
    if (family) return other.includes(family) ? name.replace(/^Claude /, "") : family;
    return name;
  });
  return labels[0] === labels[1] ? names[0] === names[1] ? routes : names : labels;
}
export function resultPill(doc, settings) {
  const node = el(doc, "span", "result-pill"), dot = el(doc, "span", "result-dot"), text = el(doc, "span");
  dot.setAttribute("aria-hidden", "true"); node.append(dot, text);
  const update = () => { const state = settings(); node.setAttribute("data-tone", resultTone(state.tone)); text.textContent = state.text; };
  update(); return { node, update };
}
// The controller supplies proven substrings; this renderer never rewrites answers.
export function answerBlock(doc, row, local, actual, updates, passed = false) {
  if (row.answerExcerpt === undefined) return el(doc, "p", "quiet", "Answer content not included.");
  const box = el(doc, "div", "answer"), truncated = row.truncated || /…\s*\[truncated\]$/.test(row.answerExcerpt ?? "");
  if (truncated) box.append(el(doc, "span", "label", "Answer truncated"));
  const pre = el(doc, "pre", "answer-excerpt", actual === undefined ? row.answerExcerpt : undefined);
  if (actual !== undefined) {
    const [before, after] = row.answerExcerpt.split(actual), mark = el(doc, "mark", "", actual);
    updates.push(() => { mark.className = local() ? "old" : passed ? "matched" : ""; });
    pre.append(before, mark, after);
  }
  box.append(pre); return box;
}
export function refusalContext(view, selected) {
  if (!["model_unavailable", "copilot_model_unavailable"].includes(view.latest.result?.reasonCode)) return "";
  const pins = [...new Set((view.authoring?.examples ?? []).flatMap(e => e.assertions.filter(a => a.type === "llmJudge" && a.judgeModel).map(a => a.judgeModel)))];
  return ` Run model: ${modelPresentation(effectiveModel(view.model, selected, view.chatModel)).name}.${pins.length ? ` Pinned Judge models: ${pins.join(", ")}.` : ""}`;
}
export function growTextarea(input) {
  input.style.height = "auto";
  input.style.height = `${input.scrollHeight + 2}px`;
}
// A check's fields own only their input values; source mutation and key actions stay caller-owned.
export function checkFields(doc, initial, index, change, fieldKeys, modelControl) {
  const node = el(doc, "div", "check-fields");
  let inputs = [], first, targets = new Map(), advanced, judgeModel, disabled = false;
  const markIssue = issue => {
    for (const { input, hint } of targets.values()) {
      input.setAttribute("aria-invalid", "false"); input.setAttribute("aria-describedby", "");
      hint.hidden = true; hint.textContent = "";
    }
    const target = targets.get(issue?.field);
    if (!target) return;
    const { input, hint } = target;
    hint.textContent = issue.message; hint.hidden = false;
    input.setAttribute("aria-invalid", "true"); input.setAttribute("aria-describedby", hint.id);
    if (advanced && issue.field === "judgeVersion") advanced.open = true;
    return input;
  };
  const show = assertion => {
    inputs = []; targets = new Map(); judgeModel = undefined; node.replaceChildren();
    const judge = assertion.type === "llmJudge";
    node.className = judge ? "check-fields judge-fields" : "check-fields";
    advanced = judge ? el(doc, "details", "judge-advanced") : undefined;
    if (advanced) { advanced.append(el(doc, "summary", "", "Advanced")); advanced.open = !String(assertion.judgeVersion ?? "").trim(); }
    const definitions = judge ? [["rubric", "Rubric"], ["minScore", "Minimum score (0 to 100)"], ["judgeModel", "Judge model"], ["judgeVersion", "Judge version"]] : [[operandKey(assertion.type), "text"]];
    for (const [key, label] of definitions) {
      if (key === "judgeModel") {
        // Picked from the canvas model catalog, never typed; the menu button keeps its own keys (no form Enter/Escape).
        let value = assertion.judgeModel;
        modelControl ??= runtimeModelControl(doc, () => ({ authored: "" }), async () => ({ error: "unavailable" }), () => {});
        judgeModel = modelControl.judge(() => ({ value, disabled, label: `Check ${index + 1} judge model` }), chosen => {
          if (chosen === value || value === undefined && chosen === "") return;
          value = chosen; judgeModel.update(); change(key, chosen);
        });
        const hint = el(doc, "span", "field-hint"); hint.id = `check-${index + 1}-${key}-issue`; hint.hidden = true;
        targets.set(key, { input: judgeModel.trigger, hint });
        const wrap = el(doc, "div", "check-field", label); wrap.append(judgeModel.node, hint); node.append(wrap);
        continue;
      }
      const input = el(doc, judge && key === "rubric" ? "textarea" : "input", `field check-value${key === "rubric" ? " judge-rubric" : ""}`);
      input.value = assertion[key] ?? "";
      input.setAttribute("aria-label", `Check ${index + 1} ${key === "minScore" ? "minimum score" : label.toLowerCase()}`); input.setAttribute("spellcheck", "false");
      if (key === "minScore") { input.type = "number"; input.min = "0"; input.max = "100"; input.step = "1"; }
      else if (input.tagName?.toLowerCase() !== "textarea" && key !== "rubric") input.type = "text";
      if (!judge) input.setAttribute("placeholder", assertion.type === "contains" ? "Text the answer must contain" : assertion.type === "notContains" ? "Text the answer must not contain" : "Pattern the answer must match");
      if (key === "rubric") input.rows = 2;
      input.addEventListener("input", () => {
        if (key === "judgeVersion" && !input.value.trim()) advanced.open = true;
        change(key, key === "minScore" && input.value !== "" ? Number(input.value) : input.value);
      });
      fieldKeys(input); inputs.push(input);
      const hint = el(doc, "span", "field-hint"); hint.id = `check-${index + 1}-${key}-issue`; hint.hidden = true;
      targets.set(key, { input, hint });
      if (judge) {
        const wrap = el(doc, "label", `check-field ${key === "rubric" ? "rubric-field" : ""}`, label); wrap.append(input, hint);
        (key === "judgeVersion" ? advanced : node).append(wrap);
      } else node.append(input, hint);
    }
    if (advanced) node.append(advanced);
    first = inputs[0];
  };
  show(initial);
  return { node, show, markIssue, focus() { first.focus(); }, update(off) {
    disabled = off; judgeModel?.update();
    for (const input of inputs) { input.disabled = off; if (input === first && first.rows) growTextarea(first); }
  } };
}
// Unknown is an observation, not a passing/failing check. Identity must still be unambiguous.
export function inconclusiveJudge(c, observed) {
  return observed?.verdict === "unknown" && ["unknown", "fail"].includes(c?.state) && c.expected?.size === c.checks?.size
    && [...c.expected.values(), ...c.checks.values()].every(value => value !== null)
    && [...c.checks.keys()].every(key => c.expected.has(key));
}
// Judge diagnostics are display-only and stay out of the list's status copy.
export function judgeDetails(doc) {
  const binding = el(doc, "div", "meta judge-binding"), node = el(doc, "div", "judge-result");
  node.style.gridColumn = "1 / -1"; // Also span inspection-only check grids.
  const score = el(doc, "div", "judge-score"), reason = el(doc, "div", "judge-reason");
  return { binding, node, score, reason, update(assertion, observed, visible, old) {
    const model = assertion?.judgeModel ?? assertion?.fields?.find(([key]) => key === "judgeModel")?.[1];
    binding.textContent = model ? `Judge model: ${model} · pinned` : ""; binding.hidden = !model;
    const detail = visible ? observed?.judge : undefined;
    const reasons = [...new Set(detail?.reasons ?? [])];
    const equalScores = detail?.scores.length === 2 && detail.scores[0] === detail.scores[1];
    score.textContent = detail ? `${equalScores ? `Score ${detail.scores[0]}` : `Scores ${detail.scores.join(", ")}`} · needs ${detail.minScore}${equalScores ? " · 2 samples" : ""}` : "";
    const fallback = observed && (!observed.reasonCode || observed.reasonCode === "judge_verdict") ? observed.reason ?? "" : plainReason(observed);
    reason.textContent = visible ? reasons.length ? reasons.join("\n\n") : fallback : "";
    node.hidden = !visible; node.className = old ? "judge-result old" : "judge-result";
  } };
}
export function checkTypeControl(doc, initial, change, cancel) {
  const node = el(doc, "div", "check-type"), trigger = el(doc, "button", "check-type-button"), menu = el(doc, "div", "check-type-menu");
  trigger.type = "button"; trigger.setAttribute("aria-haspopup", "menu"); menu.setAttribute("role", "menu");
  const open = value => { menu.hidden = !value; trigger.setAttribute("aria-expanded", String(value)); };
  const show = type => {
    trigger.replaceChildren(EDITABLE_TYPES.find(([id]) => id === type)[1], icon(doc, "down", 12, 2));
    trigger.setAttribute("aria-label", `Check type: ${trigger.textContent}`);
  };
  const choices = EDITABLE_TYPES.map(([type, label], index) => {
    const option = el(doc, "button", "check-type-option", label); option.type = "button"; option.setAttribute("role", "menuitemradio");
    option.setAttribute("aria-checked", String(type === initial));
    option.addEventListener("click", () => {
      show(type); open(false);
      choices.forEach((item, i) => { item.setAttribute("aria-checked", String(i === index)); });
      trigger.focus(); change(type);
    });
    option.addEventListener("keydown", event => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault(); choices[(index + (event.key === "ArrowDown" ? 1 : choices.length - 1)) % choices.length].focus();
      } else if (event.key === "Escape") { event.preventDefault(); open(false); trigger.focus(); }
    });
    return option;
  });
  trigger.addEventListener("click", () => open(menu.hidden));
  trigger.addEventListener("keydown", event => {
    if (event.key === "Escape") { event.preventDefault(); if (menu.hidden) cancel(); else open(false); }
    else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault(); open(true); choices[event.key === "ArrowDown" ? 0 : choices.length - 1].focus();
    }
  });
  node.addEventListener("focusout", event => { if (!node.contains(event.relatedTarget)) open(false); });
  menu.append(...choices); node.append(trigger, menu); show(initial); open(false);
  return { node, update(disabled) { for (const control of [trigger, ...choices]) control.disabled = disabled; if (disabled) open(false); } };
}

// One lazy catalog read per canvas: concurrent opens share it, success is cached and failure retries on the next open.
function modelCatalog(load) {
  let pending;
  const catalog = { models: [], defaultModel: DEFAULT_MODEL, state: "idle", discover() {
    if (catalog.state === "ready") return Promise.resolve();
    if (pending) return pending;
    catalog.state = "loading";
    pending = Promise.resolve().then(load).then(response => {
      if (response?.error || !Array.isArray(response?.models)) throw new Error("unavailable");
      catalog.models = response.models; catalog.defaultModel = response.defaultModel ?? DEFAULT_MODEL; catalog.state = "ready";
    }).catch(() => { catalog.state = "failed"; }).finally(() => { pending = undefined; });
    return pending;
  } };
  return catalog;
}
// Runtime selection only emits values to its owner; it cannot read or write an agent.
export function runtimeModelControl(doc, settings, load, change) {
  const catalog = modelCatalog(load), control = modelMenu(doc, settings, catalog, change);
  // Judge menus offer only catalog IDs or "" (follow the run model); an authored pin outside the catalog stays read-only.
  return { ...control, judge: (judgeSettings, judgeChange) => modelMenu(doc, judgeSettings, catalog, judgeChange, true) };
}
function modelMenu(doc, settings, catalog, change, judge) {
  const node = el(doc, "div", judge ? "runtime-model judge-model" : "runtime-model"), trigger = el(doc, "button", "model-picker"), menu = el(doc, "div", "model-menu");
  const status = el(doc, "div", "quiet model-status"); status.setAttribute("role", "status");
  trigger.type = "button"; trigger.setAttribute("aria-haspopup", "menu");
  menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", judge ? "Judge models" : "Runtime models");
  let choices = [], opened = false, renderedModels;
  const open = value => { opened = value; menu.hidden = !value; trigger.setAttribute("aria-expanded", String(value)); };
  const bind = (item, value) => {
    item.addEventListener("click", () => { if (trigger.disabled || item.disabled) return; open(false); change(value); trigger.focus(); });
    item.addEventListener("keydown", event => {
      const index = choices.indexOf(item);
      if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
        event.preventDefault(); choices[event.key === "Home" ? 0 : event.key === "End" ? choices.length - 1 : (index + (event.key === "ArrowDown" ? 1 : choices.length - 1)) % choices.length].focus();
      } else if (event.key === "Escape") { event.preventDefault(); open(false); trigger.focus(); }
      else if (event.key === "Tab") open(false);
    });
    if (!item.disabled) choices.push(item);
  };
  const update = () => {
    renderedModels = catalog.models;
    if (judge) return updateJudge();
    const { models, defaultModel, state } = catalog;
    const { authored, selected, chatModel, chatModelName, disabled } = settings();
    const fallback = effectiveModel(authored, "", chatModel), current = selected || fallback;
    const origin = selected ? selected === defaultModel ? "default" : "this canvas" : authored ? "from the agent file" : "default";
    trigger.replaceChildren(el(doc, "span", "model-name", modelPresentation(current).name), el(doc, "span", "model-origin", ` · ${origin}`), icon(doc, "down", 12, 2));
    trigger.setAttribute("aria-label", `Runtime model: ${modelPresentation(current).name} · ${origin}`);
    trigger.setAttribute("title", `${current} · ${origin}`); trigger.disabled = !!disabled;
    const focused = choices.find(option => option === doc.activeElement)?.getAttribute("data-model"), wasOpen = opened;
    const chatUnavailable = state === "ready" && chatModel && !models.includes(chatModel);
    const ids = [...new Set([chatModel, fallback, selected, ...models].filter(Boolean))];
    const groups = new Map();
    for (const id of ids) {
      const group = modelPresentation(id).group;
      if (!groups.has(group)) groups.set(group, []);
      groups.get(group).push(id);
    }
    choices = []; menu.replaceChildren();
    // Following is a mode, not a duplicate entry for the fallback's model ID.
    const follow = el(doc, "button", "model-default", authored ? "Use agent file model" : chatModel ? "Follow chat model" : "Use default model");
    follow.type = "button"; follow.disabled = !!disabled || !!(!authored && chatUnavailable);
    follow.setAttribute("role", "menuitem"); follow.setAttribute("data-model", "");
    bind(follow, ""); menu.append(follow);
    if (chatModelName && !chatModel) {
      const group = el(doc, "div", "model-options"), option = el(doc, "button", "model-option", chatModelName);
      group.setAttribute("role", "group"); group.setAttribute("aria-label", "Chat");
      option.type = "button"; option.disabled = true; option.setAttribute("role", "menuitemradio"); option.setAttribute("aria-checked", "false");
      option.append(el(doc, "span", "model-chat", "Chat model · not available here"));
      group.append(el(doc, "div", "model-group", "Chat"), option); menu.append(group);
    }
    for (const [name, ids] of groups) {
      const group = groupOf(name);
      for (const id of ids) {
        const option = el(doc, "button", `model-option${name === "Other models" ? " model-id" : ""}`, modelPresentation(id).name); option.type = "button";
        option.setAttribute("role", "menuitemradio"); option.setAttribute("aria-checked", String(id === current));
        option.setAttribute("data-model", id); option.setAttribute("title", id); option.disabled = !!disabled || !!(id === chatModel && chatUnavailable);
        if (id === chatModel) option.append(el(doc, "span", "model-chat", chatUnavailable ? "Chat model · not available here" : "Current chat model"));
        bind(option, id); group.append(option);
      }
    }
    finish(disabled, focused, wasOpen);
  };
  const finish = (disabled, focused, wasOpen) => {
    const { state } = catalog;
    status.textContent = state === "loading" ? "Loading models…" : state === "failed" ? "Choices unavailable; model selection unchanged." : "";
    menu.append(status);
    if (disabled) open(false);
    if (focused !== undefined && !disabled) { open(wasOpen); choices.find(option => option.getAttribute("data-model") === focused)?.focus(); }
  };
  const groupOf = name => {
    const group = el(doc, "div", "model-options"); group.setAttribute("role", "group"); group.setAttribute("aria-label", name);
    group.append(el(doc, "div", "model-group", name)); menu.append(group); return group;
  };
  const updateJudge = () => {
    const { value, disabled, label } = settings(), pin = value ?? "", ready = catalog.state === "ready";
    const name = pin === "" ? "Run model" : modelPresentation(String(pin)).name, origin = pin === "" ? "follows selection" : "pinned";
    trigger.replaceChildren(el(doc, "span", "model-name", name), el(doc, "span", "model-origin", ` · ${origin}`), icon(doc, "down", 12, 2));
    trigger.setAttribute("aria-label", `${label}: ${name} · ${origin}`); trigger.setAttribute("title", `${pin || name} · ${origin}`); trigger.disabled = !!disabled;
    const focused = choices.find(option => option === doc.activeElement)?.getAttribute("data-model"), wasOpen = opened;
    choices = []; menu.replaceChildren();
    const follow = el(doc, "button", "model-default", "Run model · follows selection");
    follow.type = "button"; follow.disabled = !!disabled; follow.setAttribute("role", "menuitemradio"); follow.setAttribute("aria-checked", String(pin === ""));
    follow.setAttribute("data-model", ""); bind(follow, ""); menu.append(follow);
    const models = ready ? [...new Set(catalog.models)] : [], ids = pin !== "" && !models.includes(pin) ? [...models, pin] : models, groups = new Map();
    for (const id of ids) { const group = modelPresentation(String(id)).group; groups.set(group, [...groups.get(group) ?? [], id]); }
    for (const [group, members] of groups) {
      const node = groupOf(group);
      for (const id of members) {
        const option = el(doc, "button", `model-option${group === "Other models" ? " model-id" : ""}`, modelPresentation(String(id)).name); option.type = "button";
        option.setAttribute("role", "menuitemradio"); option.setAttribute("aria-checked", String(id === pin));
        option.setAttribute("data-model", id); option.setAttribute("title", id); option.disabled = !!disabled || !models.includes(id);
        // Unknown until the catalog is read; afterwards an authored pin outside it is shown, never offered.
        if (ready && !models.includes(id)) option.append(el(doc, "span", "model-chat", "From the agent file · not in the model list"));
        bind(option, id); node.append(option);
      }
    }
    finish(disabled, focused, wasOpen);
  };
  const discover = async () => {
    if (catalog.state === "ready") { if (renderedModels !== catalog.models) update(); return; }
    const read = catalog.discover(); update(); await read; update();
  };
  trigger.addEventListener("click", async () => { if (trigger.disabled) return; open(!opened); if (opened) await discover(); });
  trigger.addEventListener("keydown", event => {
    if (event.key === "Escape") { event.preventDefault(); open(false); }
    else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault(); open(true); discover(); choices[event.key === "ArrowDown" ? 0 : choices.length - 1]?.focus();
    }
  });
  node.addEventListener("focusout", event => { if (!node.contains(event.relatedTarget)) open(false); });
  node.append(trigger, menu); open(false); update(); return { node, trigger, update };
}

// Static icon geometry copied from the approved board builder; never derived from resource text.
const SVG_NS = "http://www.w3.org/2000/svg";
const RING = ["circle", { cx: "12", cy: "12", r: "9" }];
const ICONS = {
  fail: [["path", { d: "M6 6l12 12M18 6L6 18" }]],
  pass: [["path", { d: "M5 12.5l4.5 4.5L19 7.5" }]],
  error: [RING, ["path", { d: "M12 7.6v5.6" }], ["path", { d: "M12 16.4h.01" }]],
  none: [["path", { d: "M8 12h8" }]],
  running: [["path", { d: "M12 3a9 9 0 0 1 9 9" }]],
  enter: [["path", { d: "M20 5v7a3 3 0 0 1-3 3H5" }], ["path", { d: "M9 11l-4 4 4 4" }]],
  down: [["path", { d: "M6 9l6 6 6-6" }]],
  back: [["path", { d: "M15 6l-6 6 6 6" }]],
  close: [["path", { d: "M6 6l12 12M18 6L6 18" }]],
  plus: [["path", { d: "M12 5v14M5 12h14" }]],
  more: [["circle", { cx: "5", cy: "12", r: "1" }], ["circle", { cx: "12", cy: "12", r: "1" }], ["circle", { cx: "19", cy: "12", r: "1" }]],
  trash: [["path", { d: "M4 7h16" }], ["path", { d: "M10 11v6M14 11v6" }], ["path", { d: "M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12" }], ["path", { d: "M9 7V4h6v3" }]],
  sync: [["path", { d: "M21 12a9 9 0 0 1-15.5 6.2" }], ["path", { d: "M3 12A9 9 0 0 1 18.5 5.8" }], ["path", { d: "M18 2v4h-4" }], ["path", { d: "M6 22v-4h4" }]],
  warn: [["path", { d: "M12 3.5l9.5 16.5h-19z" }], ["path", { d: "M12 10v4.5" }], ["path", { d: "M12 17.4h.01" }]],
};
export function icon(doc, name, size, strokeWidth, className) {
  const svg = doc.createElementNS(SVG_NS, "svg");
  for (const [key, value] of Object.entries({ width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor",
    "stroke-width": strokeWidth, "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true", focusable: "false" })) {
    svg.setAttribute(key, String(value));
  }
  if (className) svg.setAttribute("class", className);
  for (const [tag, attrs] of ICONS[name]) {
    const shape = doc.createElementNS(SVG_NS, tag);
    for (const [key, value] of Object.entries(attrs)) shape.setAttribute(key, value);
    svg.append(shape);
  }
  return svg;
}
export function statusIcon(doc, kind, label) {
  const base = kind.replace(/^old-/, "");
  const mark = el(doc, "span", `status-icon ${kind}`);
  mark.setAttribute("role", "img"); mark.setAttribute("aria-label", label); mark.setAttribute("title", label);
  mark.append(icon(doc, base === "running" ? "running" : base, 14, 2.2, base === "running" ? "spin" : ""));
  return mark;
}

export function canvasResultSummary(view, { n, testing, one, blocked, dirty, last }) {
  const result = view.latest.result;
  if (testing) return { tone: "running", text: `Running ${one ? "1 test" : plural(n, "test")}…` };
  if (blocked) return { tone: "none", text: "Result unconfirmed" };
  if (view.latest.status === "stale" || (dirty || view.latest.status !== "current") && last) return { tone: "none", text: "Out of date" };
  if (!n || !result || result.reasonCode === "no_tests") return { tone: "none", text: "Not run yet" };
  if (isRefused(result)) return { tone: "error", text: "Refused" };
  if (result.state !== "complete" || !coherentSelection(view)) return { tone: "error", text: result.state === "complete" ? "Incomplete run" : "Didn't finish" };
  const cases = inspectCases(view), passing = cases.filter(c => c.state === "pass").length;
  const full = result.fullPass && result.verdict === "pass" && result.selection.scope === "full" && passing === n;
  if (!full && (passing === n || cases.some(c => c.state === "unknown"))) return { tone: "error", text: "Incomplete run" };
  return { tone: full ? "pass" : cases.some(c => c.state === "fail") ? "fail" : "none", text: result.selection.scope === "subset" ? countText(cases.map(c => testRow(c, false, false))) : `${passing} of ${n} passing` };
}

export function canvasFooterText({ editable, n, busy, pending, blocked, savedUntested, invalid, candidate, scope, promptDirty, testDirty, dirty, runAlert }) {
  if (!editable) return "Read-only inspection. This engine or agent file does not provide a complete editable snapshot.";
  const tests = plural(n, "test");
  if (busy) {
    if (!pending?.operation || !["save-and-test", "test"].includes(pending.operation)) return "Working…";
    const what = pending.one ? "this test" : tests;
    return pending.operation === "save-and-test" ? `Saving and running ${what}…` : `Saved · running ${what}`;
  }
  if (blocked) return savedUntested ? "Saved · test result unconfirmed" : "Reload required · result unconfirmed";
  if (invalid) return "Edited · unsupported formatting, reload before saving";
  if (candidate) return "Edited · settle the outside change before saving";
  if (scope === "one") return promptDirty || testDirty
    ? "Enter saves and runs this test" : "Saved · Enter runs this test";
  if (!n) return dirty ? `${promptDirty ? "Edited" : "Prompt saved"} · Enter saves` : "Saved · add a test to run it";
  if (dirty) return `${promptDirty ? "Edited" : "Prompt saved"} · Enter saves and runs ${tests}`;
  if (runAlert?.saved && !runAlert.finished) return "Saved · tests didn't run";
  return `Saved · Enter runs ${tests}`;
}

export function canvasHeader(doc, view, actionNodes, readOnly) {
  const displayName = agentName(view.title), file = fileLocation(view.file.path);
  const source = el(doc, "details", "source-details"), info = el(doc, "div", "source-info");
  const sourceSummary = el(doc, "summary", "meta agent-meta", `${file.name}${file.folder ? ` · in ${file.folder}` : ""}`);
  sourceSummary.setAttribute("title", "Source details"); source.append(sourceSummary, info);
  info.append(el(doc, "p", "source-title", view.title), pathControls(doc, view.file.path), el(doc, "code", "revision", view.file.revision));
  const head = el(doc, "header", "agent-header"), identity = el(doc, "div", "agent-identity"), actions = el(doc, "div", "header-controls");
  identity.append(el(doc, "span", "agent-eyebrow", "Your agent"), el(doc, "h1", "", displayName));
  actions.append(...actionNodes);
  if (readOnly) {
    const id = effectiveModel(view.model, "", view.chatModel);
    const model = el(doc, "span", "model-readonly", `${modelPresentation(id).name} · ${view.model ? "from the agent file" : "default"}`);
    model.setAttribute("title", id); actions.append(model);
  }
  const purpose = purposeText(view), summary = el(doc, "p", "purpose");
  const chars = Array.from(purpose); summary.textContent = chars.length > 160 ? chars.slice(0, 159).join("") + "…" : purpose;
  identity.append(summary, source); head.append(identity, actions);
  if (chars.length > 160) info.append(el(doc, "p", "", purpose));
  return { head, info };
}

export function saveButton(doc, button, label, action, className = "ib save-run") {
  const node = button("", action, className, "save");
  node.setAttribute("aria-label", label); node.setAttribute("title", `${label} (Enter)`);
  node.append(icon(doc, "enter", 16, 2.2));
  return node;
}
export function runNotice(doc, button, alert, dirty, modelContext, retry) {
  const box = el(doc, "div", "notice notice-warn run-alert"); box.setAttribute("role", "alert");
  const reason = alert.text.replace(/\.\s*$/, "");
  const text = alert.refused ? `Refused: ${reason}.` : alert.finished ? `The run didn't finish: ${reason}.` : `The run didn't start: ${reason}.`;
  const tail = alert.saved ? " Your changes are saved." : dirty ? " Your edits are kept." : "";
  const line = el(doc, "span", "notice-text"); line.append(icon(doc, "warn", 15, 2), el(doc, "span", "", text + tail + (alert.refused ? modelContext : "")));
  box.append(line);
  if (!alert.refused) box.append(button("Try again", retry, "sb", "save"));
  return box;
}

export function conflictNotice(doc, button, items, compareOpen, toggleCompare, keepMine, useTheirs) {
  const box = el(doc, "div", "notice notice-info"); box.setAttribute("role", "alert");
  const parts = [];
  if (items.some(([name]) => name === "Prompt")) parts.push("the prompt");
  const tests = items.filter(([name]) => name.startsWith("Test ")).length;
  if (tests) parts.push(plural(tests, "test"));
  const others = items.length - tests - (parts[0] === "the prompt" ? 1 : 0);
  if (others) parts.push(plural(others, "other part"));
  const head = el(doc, "span", "notice-text"); head.append(icon(doc, "sync", 15, 2),
    el(doc, "span", "", `This agent changed outside the panel${parts.length ? `: ${parts.join(", ")}` : ""}. You have unsaved edits.`));
  const actions = el(doc, "div", "notice-actions");
  const compare = button("Compare", toggleCompare, "sb", "nav");
  compare.setAttribute("aria-expanded", String(compareOpen));
  actions.append(compare, button("Keep my edits", keepMine, "sb"), button("Use the new version", useTheirs, "sb"));
  box.append(head, actions);
  if (compareOpen) {
    const list = el(doc, "div", "compare");
    for (const [name, b, m, t] of items) {
      const item = el(doc, "section", "compare-item");
      item.append(el(doc, "h3", "lbl", name));
      const cols = el(doc, "div", "compare-cols");
      for (const [label, text] of [["Before your edits", b], ["Your edits", m], ["New version", t]]) {
        const col = el(doc, "div", "compare-col");
        col.append(el(doc, "span", "meta", label), el(doc, "pre", "compare-text", text === undefined ? "(not present)" : text));
        cols.append(col);
      }
      item.append(cols); list.append(item);
    }
    if (!items.length) list.append(el(doc, "p", "quiet", "No differences in the prompt or tests."));
    box.append(list);
  }
  return box;
}

// Event and refresh hooks read live owner state; mutations remain caller-owned.
export function instructionEditor(doc, label, initialText, hooks) {
  const node = el(doc, "div", "instruction-body");
  fillEditor(doc, node, initialText);
  node.setAttribute("role", "textbox"); node.setAttribute("aria-multiline", "true");
  node.setAttribute("aria-label", label); node.setAttribute("contenteditable", "plaintext-only");
  node.setAttribute("spellcheck", "false");
  const changed = () => hooks.change(node);
  const lineBreak = () => { if (!hooks.disabled()) { insertPlainText(doc, node, "\n"); changed(); } };
  node.addEventListener("input", changed);
  node.addEventListener("beforeinput", event => {
    if (event.inputType === "insertLineBreak" && event.cancelable && !event.isComposing) { event.preventDefault(); lineBreak(); }
  });
  node.addEventListener("keydown", (event) => {
    if (event.isComposing) return;
    if (event.key === "Enter" && !event.ctrlKey && !event.altKey && !event.metaKey) {
      event.preventDefault();
      if (event.shiftKey) lineBreak(); else hooks.submit();
    } else if (event.key === "Escape" && hooks.hasDraft()) {
      event.preventDefault(); hooks.cancel();
    }
  });
  for (const kind of ["paste", "drop"]) node.addEventListener(kind, (event) => {
    event.preventDefault(); if (hooks.disabled()) return;
    const transfer = kind === "paste" ? event.clipboardData : event.dataTransfer;
    insertPlainText(doc, node, transfer?.getData("text/plain") ?? ""); changed();
  });
  const update = () => {
    node.setAttribute("contenteditable", hooks.disabled() ? "false" : "plaintext-only");
    node.setAttribute("aria-readonly", String(hooks.disabled()));
  };
  return { node, update };
}

export function testNameControl(doc, settings, change) {
  const input = el(doc, "input", "test-title-input"), hint = el(doc, "p", "name-hint", "Add a name to save and run this test.");
  input.type = "text"; input.value = settings().name;
  input.setAttribute("aria-label", "Test name"); input.setAttribute("placeholder", "Name this test");
  input.setAttribute("spellcheck", "false"); input.setAttribute("aria-describedby", "test-name-hint");
  hint.setAttribute("id", "test-name-hint");
  input.addEventListener("input", () => change(input.value));
  const update = () => {
    const { disabled, invalid } = settings();
    input.disabled = disabled; input.setAttribute("aria-invalid", String(invalid)); hint.hidden = !invalid;
  };
  update(); return { input, hint, update };
}

export function pathControls(doc, path) {
  const box = el(doc, "div", "source-path");
  const button = el(doc, "button", "copy-path", "Copy path");
  button.setAttribute("type", "button");
  button.setAttribute("aria-label", "Copy agent path");
  button.setAttribute("data-action", "copy-path");
  const feedback = el(doc, "span", "copy-status");
  feedback.setAttribute("role", "status");
  feedback.setAttribute("aria-live", "polite");
  button.addEventListener("click", async () => {
    const unavailable = "Copy unavailable. Select the path and copy it manually.";
    try {
      const clipboard = doc.defaultView?.navigator?.clipboard;
      if (typeof clipboard?.writeText !== "function") {
        feedback.textContent = unavailable;
        return;
      }
      await clipboard.writeText(path);
      feedback.textContent = "Path copied.";
    } catch {
      feedback.textContent = unavailable;
    }
  });
  box.append(el(doc, "p", "path-label", "Agent file path"), el(doc, "code", "path-value", path), button, feedback);
  return box;
}

// The resource already bounds instructions. Keep fences opaque before looking
// for duplicate metadata, so quoted headings never hide authored content.
export function instructionBlocks(source) {
  const lines = source.split("\n");
  const blocks = [];
  for (let i = 0; i < lines.length; i++) {
    const open = lines[i].match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
    if (!open || (open[1][0] === "`" && open[2].includes("`"))) {
      blocks.push({ raw: lines[i] });
      continue;
    }
    let end = i + 1;
    for (; end < lines.length; end++) {
      const close = lines[end].match(/^ {0,3}(`{3,}|~{3,})[ \t]*$/);
      if (close && close[1][0] === open[1][0] && close[1].length >= open[1].length) break;
    }
    const closed = end < lines.length;
    blocks.push({
      code: true,
      raw: lines.slice(i, closed ? end + 1 : end).join("\n"),
      // An incomplete block keeps even its opener visible, including truncation.
      text: lines.slice(closed ? i + 1 : i, end).join("\n"),
    });
    i = closed ? end : end - 1;
  }
  return blocks;
}

const inlineNodes = new WeakSet();
const caretPlaceholders = new WeakSet();
export function inlineText(doc, node, text) {
  for (const [index, line] of text.split("\n").entries()) {
    if (index) node.append("\n");
    // Unsupported syntax and indentation stay wholly literal. Do not pair
    // delimiters across lines or partially format a line with unmatched ones.
    const parts = line.split(/(`[^`\n]+`|\*\*[^*`\n]+\*\*)/g);
    if (/^(?: {4}|\t)/.test(line) || /[<>\[\]|]|``|\*{3}/.test(line) || parts.some((part, i) => i % 2 === 0 && /`|\*\*/.test(part))) {
      node.append(line);
      continue;
    }
    for (const [i, part] of parts.entries()) {
      if (i % 2 === 0) node.append(part);
      else {
        const code = part.startsWith("`");
        const span = el(doc, code ? "code" : "strong", "", part.slice(code ? 1 : 2, code ? -1 : -2));
        inlineNodes.add(span); node.append(span);
      }
    }
  }
}

// Only our inline spans and browser plain-text line containers are accepted.
// Unknown formatting blocks saving rather than silently losing source content.
export function editorValue(root) {
  let count = 0, size = 0;
  const read = (node, depth) => {
    if (++count > 8192 || depth > 32) throw new Error("Editor structure is too large.");
    if (node.nodeType === 3) {
      size += node.textContent.length;
      if (size > 262144) throw new Error("Editor text is too large.");
      return node.textContent;
    }
    const tag = node.tagName?.toLowerCase();
    if (node !== root && !["br", "div", "p"].includes(tag) && !inlineNodes.has(node)) throw new Error("Unsupported editor formatting.");
    if (tag === "br") return caretPlaceholders.has(node) ? "" : "\n";
    const children = [...node.childNodes];
    if (["div", "p"].includes(tag) && node !== root && children.length === 1 && children[0].tagName?.toLowerCase() === "br") return "";
    if (inlineNodes.has(node) && children.some((child) => child.nodeType !== 3 && !caretPlaceholders.has(child))) throw new Error("Unsupported inline formatting.");
    let value = "", previousBlock = false;
    for (const [index, child] of children.entries()) {
      const block = ["div", "p"].includes(child.tagName?.toLowerCase());
      if (index && (block || previousBlock) && (previousBlock || !value.endsWith("\n"))) value += "\n";
      value += read(child, depth + 1); previousBlock = block;
    }
    return inlineNodes.has(node) ? (tag === "code" ? "`" + value + "`" : "**" + value + "**") : value;
  };
  return read(root, 0);
}

export function insertPlainText(doc, node, text) {
  const selection = doc.getSelection();
  let range = selection?.rangeCount ? selection.getRangeAt(0) : undefined;
  if (!range || !node.contains(range.commonAncestorContainer)) {
    range = doc.createRange(); range.selectNodeContents(node); range.collapse(false);
  }
  range.deleteContents();
  // A collapsed root boundary belongs inside its adjacent line, not between
  // block containers (which the serializer already separates with an LF).
  if (range.startContainer === node && range.collapsed && node.childNodes.length) {
    const index = range.startOffset, line = node.childNodes[index === 0 ? 0 : index - 1];
    if (["div", "p"].includes(line.tagName?.toLowerCase())) range.setStart(line, index === 0 ? 0 : line.childNodes.length);
  }
  const container = range.startContainer;
  if (["div", "p"].includes(container?.tagName?.toLowerCase()) && container.childNodes.length === 1 && container.childNodes[0].tagName?.toLowerCase() === "br") {
    container.replaceChildren(); range.setStart(container, 0);
  }
  // Keep insertion before an existing terminal caret placeholder, including
  // selections supplied at the line/root end rather than by the browser.
  const tail = container.childNodes?.[container.childNodes.length - 1];
  if (caretPlaceholders.has(tail) && range.startOffset === container.childNodes.length) range.setStart(container, range.startOffset - 1);
  const inserted = doc.createTextNode(text);
  range.insertNode(inserted); range.setStartAfter(inserted);
  // Chromium collapses a terminal LF without a following rendered caret row.
  // This owned BR supplies that row without contributing authored source text.
  const following = [];
  let cursor = inserted;
  for (;;) {
    const parent = cursor.parentNode, siblings = [...parent.childNodes];
    following.push(...siblings.slice(siblings.indexOf(cursor) + 1));
    if (!inlineNodes.has(parent)) break;
    cursor = parent;
  }
  if (text.endsWith("\n")
      && following.every(child => caretPlaceholders.has(child) || child.nodeType === 3 && !child.textContent)
      && !following.some(child => caretPlaceholders.has(child))) {
    const placeholder = el(doc, "br"); caretPlaceholders.add(placeholder);
    range.insertNode(placeholder); range.setStartAfter(inserted);
  }
  range.collapse(true);
  selection?.removeAllRanges(); selection?.addRange(range);
}
// Each source line is its own block so a blank line can be drawn as a short
// paragraph gap; the serializer still reads it back as exactly one LF.
export function fillEditor(doc, node, text) {
  node.replaceChildren();
  for (const block of instructionBlocks(text)) {
    const line = el(doc, "div", block.code ? "line code-line" : "line");
    if (block.code) line.append(block.raw);
    else if (block.raw === "") line.append(el(doc, "br"));
    else inlineText(doc, line, block.raw);
    node.append(line);
  }
}

export function purposeText(payload) {
  const blocks = instructionBlocks(payload.instructions);
  for (const b of blocks) {
    if (b.code) continue;
    const match = b.raw.match(/^ {0,3}[-+*][ \t]+(?:\*\*Purpose:\*\*|Purpose:)[ \t]+(.+)$/);
    if (match) return match[1];
  }
  let inModel = false;
  for (const b of blocks) {
    if (b.code) continue;
    const heading = b.raw.match(/^ {0,3}(#{1,6})[ \t]+(.+?)\s*$/);
    if (heading) {
      if (heading[1].length <= 2) inModel = heading[2] === "Model";
      continue;
    }
    const line = b.raw.trim();
    if (inModel || !line || /^(?: {4}|\t)/.test(b.raw) ||
      /^(?:[-+*]>?|>|[\[<!]|(?:ID|Model|Preferred|Authored|Created|Written|By|Attribution|Co-authored)(?:\s|:))/i.test(line)) continue;
    return b.raw;
  }
  return "";
}

export function instructionsContent(doc, payload) {
  const blocks = instructionBlocks(payload.instructions);
  const first = blocks.findIndex((b) => b.raw.trim());
  const title = first >= 0 && !blocks[first].code && blocks[first].raw.match(/^ {0,3}#[ \t]+(.+?)\s*$/);
  if (title && title[1] === payload.title) blocks.splice(first, 1);
  const models = blocks.flatMap((b, i) => !b.code && b.raw === "## Model" ? [i] : []);
  if (models.length === 1 && !payload.model.endsWith("[truncated]")) {
    const start = models[0];
    let end = start + 1;
    while (end < blocks.length && (blocks[end].code || !/^#{1,2}[ \t]+/.test(blocks[end].raw))) end++;
    const body = blocks.slice(start + 1, end).map((b) => b.raw).join("\n").trim();
    if (body === `- **Preferred:** ${payload.model}`) blocks.splice(start, end - start);
  }
  const identities = blocks.flatMap((b, i) => !b.code && b.raw === "## Identity" ? [i] : []);
  const start = identities[0];
  let end = start === undefined ? blocks.findIndex((b) => !b.code && /^ {0,3}#{1,6}[ \t]+/.test(b.raw)) : start + 1;
  if (start !== undefined) {
    while (end < blocks.length && (blocks[end].code || !/^ {0,3}#{1,2}[ \t]+/.test(blocks[end].raw))) end++;
  } else if (end < 0) end = blocks.length;
  const id = payload.file.path.match(/^([a-z][a-z0-9-]*)\.agent\.md$/)?.[1] ??
    payload.file.path.match(/^\.squad\/agents\/([a-z][a-z0-9-]*)\/charter\.md$/)?.[1];
  const hidden = new Set();
  for (const [field, value] of [["ID", id], ["Purpose", purposeText(payload)]]) {
    const candidates = blocks.flatMap((b, i) => !b.code && b.raw.startsWith(`- **${field}:** `) ? [i] : []);
    if (!value || value.includes("[truncated]") || candidates.length !== 1 || (field === "Purpose" && Array.from(value).length > 160)) continue;
    const index = candidates[0];
    const inIdentity = identities.length === 1 && index > start && index < end;
    const inPrelude = identities.length === 0 && field === "Purpose" && index < end;
    if ((inIdentity || inPrelude) && blocks[index].raw === `- **${field}:** ${value}`) hidden.add(index);
  }
  // An Identity heading still owns any prose, code, subheadings or other fields.
  if (identities.length === 1 && [...hidden].some((i) => i > start && i < end) && blocks.slice(start + 1, end).every((b, i) => hidden.has(start + 1 + i) || (!b.code && !b.raw.trim()))) hidden.add(start);
  const box = el(doc, "div", "instructions instruction-copy");
  let paragraph = [], list;
  const flush = () => {
    if (paragraph.length) {
      const p = el(doc, "p");
      inlineText(doc, p, paragraph.join("\n"));
      box.append(p);
      paragraph = [];
    }
    list = undefined;
  };
  for (const [index, block] of blocks.entries()) {
    if (hidden.has(index)) continue;
    if (block.code) {
      flush();
      box.append(el(doc, "pre", "code-block", block.text));
      continue;
    }
    const line = block.raw;
    const heading = line.match(/^ {0,3}(#{1,6})[ \t]+(.+?)\s*$/);
    const bullet = line.match(/^ {0,3}[-+*][ \t]+(.+)$/);
    if (!line.trim()) flush();
    else if (heading) {
      flush();
      box.append(el(doc, heading[1].length <= 2 ? "h3" : "h4", "instruction-heading", heading[2]));
    } else if (bullet) {
      if (paragraph.length) flush();
      if (!list) {
        list = el(doc, "ul");
        box.append(list);
      }
      const item = el(doc, "li");
      inlineText(doc, item, bullet[1]);
      list.append(item);
    } else {
      list = undefined;
      paragraph.push(line);
    }
  }
  flush();
  return box;
}
