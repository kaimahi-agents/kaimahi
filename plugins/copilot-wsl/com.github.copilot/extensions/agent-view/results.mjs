// Result reading: cases, check facts, plain reasons, rows and per-model history.
import { failureWording } from "./failure-wording.mjs";
import { sourceIdentity, preRouteRefusal, same, uniqueIndex, operandKey, quoted } from "./authoring.mjs";

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
    : a.type === "toolCalled" ? `Calls ${value}` : a.type === "toolNotCalled" ? `Doesn't call ${value}` : a.type === "regex" ? `Matches regex ${quoted(value)}` : a.type === "llmJudge" ? `${value} · judge` : `Condition: ${a.type}`;
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
    const text = failureWording(a.type, value) ?? `Not met: ${checkLabel(a)}`;
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
  invalid_tool_settings: "The tool settings in kmx.yaml are invalid. Fix them before running this agent.",
  unsafe_tool_content: "Tool content was withheld before it could be saved.", credential_content: "Content containing credentials was withheld.",
  runner_unavailable: "The agent could not start. No model was called.", execution_failed: "The agent could not finish this run.",
  invalid_completion: "The answer could not be confirmed for this run.", diagnostics_failed: "Run details could not be saved.",
  journal_failed: "The run could not be saved, so its result is unconfirmed.",
  source_changed: "The agent or its tests changed during the run. Reload before testing again.",
  recording_mismatch: "Recording mismatch: tool arguments differ from the saved call. Update the saved call before retrying.",
  recording_source_changed: "The agent file changed. The recording was not saved.",
  recording_unrepresentable: "The recording does not fit this agent file. Nothing was saved.",
  recording_save_failed: "The recording could not be saved.",
  recording_missing: "A Recorded tool has no saved call. Change the test or tool mode before comparing.",
  tool_settings_changed: "Tool settings changed. Reload before comparing.",
  copilot_auth_required: "Sign in to GitHub Copilot before running this agent.", copilot_auth_revoked: "Sign in to GitHub Copilot again before running this agent.",
  copilot_no_subscription: "An active GitHub Copilot subscription is needed for this run.", copilot_policy_denied: "Your Copilot policy does not allow this run.",
  copilot_quota: "Copilot's usage limit was reached.", copilot_model_unavailable: "Choose an available Copilot model.",
  copilot_malformed: "Copilot returned an answer that could not be used.", copilot_keychain_unavailable: "Copilot sign-in could not be read. Use an existing GitHub sign-in.",
  copilot_unavailable: "Copilot could not complete this run.",
  model_reply_unusable: "This model's reply couldn't be used, so the test didn't finish. Try another model.",
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
  let last, preferred, focused, capturedModel;
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
    else if (stopped?.run?.revision === sourceIdentity(view)) return;
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
      if (attempt?.run?.revision === sourceIdentity(view)) return;
      if (sourceIdentity(attempt?.view) === sourceIdentity(view)) return { ...view, latest: attempt.view.latest };
      const matches = [...models.values()].filter(v => modelId(v) === model && sourceIdentity(v) === sourceIdentity(view));
      return matches.length === 1 ? { ...view, latest: matches[0].latest } : undefined;
    },
    pair: () => [...models.values()],
    noteCapture: (receipt, saved) => {
      capturedModel = receipt.model;
      const defs = uniqueIndex(saved?.authoring?.examples ?? [], e => e.id), route = receipt.modelRoute;
      if (saved) for (const c of inspectCases({ ...saved, latest: { status: "current", result: receipt } })) {
        const id = c.example.id, record = { definition: defs.get(id), c: { ...c, capturedRevision: sourceIdentity(saved) }, model: route };
        if (record.definition && c.row) { records.set(id, record); modelCases.set(`${route}\0${id}`, record); }
      }
    },
    comparisonModels(selected) { const kept = [...models.values()], candidates = kept.filter(v => v.latest.result.state === "complete").map(modelId).filter(id => id !== selected); const other = candidates.includes(preferred) ? preferred : candidates.at(-1) ?? (capturedModel !== selected && !attempts.has(capturedModel) && !kept.some(v => modelId(v) === capturedModel) ? capturedModel : undefined); return other ? [selected, other] : [selected]; },
    prefer: model => { preferred = model; },
    focus: model => { focused = model; },
    started: model => { attempts.delete(model); },
    elapsed: view => timings.get(view.latest.result?.modelRoute),
    attempt: model => attempts.get(model),
    stop(run, view) {
      attempts.set(run.model, { run, view });
      pruneAttempts();
    },
    reset() { capturedModel = undefined; attempts.clear(); models.clear(); records.clear(); modelCases.clear(); timings.clear(); last = undefined; },
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
