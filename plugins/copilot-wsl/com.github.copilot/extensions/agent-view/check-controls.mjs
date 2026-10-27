// Model names and picker, and the check editing controls that use them.
import { effectiveModel, DEFAULT_MODEL, operandKey, EDITABLE_TYPES } from "./authoring.mjs";
import { el, icon } from "./dom.mjs";
import { friendlyName, plainReason } from "./results.mjs";

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
export function growTextarea(input) {
  input.style.height = "auto";
  input.style.height = `${input.scrollHeight + 2}px`;
}
// A check's fields own only their input values; source mutation and key actions stay caller-owned.
export function checkFields(doc, initial, index, change, fieldKeys, modelControl, toolNames = []) {
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
      const input = el(doc, key === "tool" ? "select" : judge && key === "rubric" ? "textarea" : "input", `field check-value${key === "rubric" ? " judge-rubric" : ""}`);
      if (key === "tool") for (const name of new Set([assertion.tool, ...toolNames].filter(Boolean))) { const option = el(doc, "option", "", toolNames.includes(name) ? name : `${name} is not on this agent`); option.value = name; input.append(option); }
      input.value = assertion[key] ?? "";
      input.setAttribute("aria-label", `Check ${index + 1} ${key === "minScore" ? "minimum score" : label.toLowerCase()}`); input.setAttribute("spellcheck", "false");
      if (key === "minScore") { input.type = "number"; input.min = "0"; input.max = "100"; input.step = "1"; }
      else if (input.tagName?.toLowerCase() !== "textarea" && key !== "rubric" && key !== "tool") input.type = "text";
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
export function checkTypeControl(doc, initial, change, cancel, toolNames = []) {
  const node = el(doc, "div", "check-type"), trigger = el(doc, "button", "check-type-button"), menu = el(doc, "div", "check-type-menu");
  trigger.type = "button"; trigger.setAttribute("aria-haspopup", "menu"); menu.setAttribute("role", "menu");
  const open = value => { menu.hidden = !value; trigger.setAttribute("aria-expanded", String(value)); };
  const show = type => {
    trigger.replaceChildren(EDITABLE_TYPES.find(([id]) => id === type)[1], icon(doc, "down", 12, 2));
    trigger.setAttribute("aria-label", `Check type: ${trigger.textContent}`);
  };
  const choices = EDITABLE_TYPES.filter(([type]) => toolNames.length || !["toolCalled", "toolNotCalled"].includes(type)).map(([type, label], index) => {
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
