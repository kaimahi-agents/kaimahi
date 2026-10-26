// Tools section (T1) and one server's tick page (T2). Inert DOM only: catalog
// labels, names and descriptions are untrusted text. Drafts, routes, saving and
// page keys stay with the caller; these views only report choices.
import { el, icon, plural, saveButton } from "./editor.mjs";

const EMPTY_SENTENCE = "A tool lets the agent read or change things through a server this app has. Tests use the same tools.";
const SETTINGS_HELP = "Set up more servers in the app’s MCP settings.";
const VISIBLE_ROWS = 9;

// A declared name belongs to a server only when exactly one catalog server lists
// it; anything else is reported generically, never assigned to a guessed server.
function ownership(servers, names, settings) {
  const seen = new Map();
  for (const server of servers) for (const name of new Set(server.tools.map(t => t.name))) seen.set(name, [...seen.get(name) ?? [], server]);
  const counts = new Map(), disabled = new Map(), off = new Set((settings ?? []).filter(s => s.state === "Disabled").map(s => s.name));
  let unmatched = 0;
  for (const name of new Set(names)) {
    const owners = seen.get(name) ?? [];
    if (owners.length === 1) {
      const owner = owners[0].name;
      counts.set(owner, (counts.get(owner) ?? 0) + 1);
      if (off.has(name)) disabled.set(owner, (disabled.get(owner) ?? 0) + 1);
    } else unmatched++;
  }
  return { count: server => counts.get(server.name) ?? 0, disabled: server => disabled.get(server.name) ?? 0, unmatched };
}
const countText = (n, server, disabled = 0) => `${n} of ${plural(server.tools.length, "tool")}${disabled ? ` · ${disabled} disabled for tests` : ""}`;

export function toolsSection(doc, button, { servers, names, settings, editable, loading, loaded = true, error, menuOpen, onToggleMenu, onOpen }) {
  const section = el(doc, "section", "tools-section"), head = el(doc, "div", "tools-head");
  head.append(el(doc, "h2", "lbl", "Tools")); section.append(head);
  const ready = loaded && !loading && !error, owned = ownership(ready ? servers : [], names, settings);
  const addControl = () => {
    const wrap = el(doc, "div", "tools-add"), add = button("+ Add tools", onToggleMenu, "tb", "nav");
    add.setAttribute("aria-haspopup", "menu"); add.setAttribute("aria-expanded", String(!!menuOpen));
    wrap.append(add);
    if (menuOpen) wrap.append(addMenu());
    return wrap;
  };
  const addMenu = () => {
    const menu = el(doc, "div", "menu tools-menu"); menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", "Add tools");
    if (ready) for (const server of servers) {
      const n = owned.count(server), item = button(server.label, () => onOpen(server), "mi tool-server-option", "nav");
      item.setAttribute("role", "menuitem");
      item.append(el(doc, "span", "meta", n ? countText(n, server) : plural(server.tools.length, "tool")));
      menu.append(item);
    }
    const status = loading ? "Loading this app’s tools…" : error ? "Couldn’t read this app’s tools." : servers.length ? "" : "This app has no MCP servers yet.";
    if (status) menu.append(el(doc, "p", "meta tools-menu-note", status));
    menu.append(el(doc, "p", "meta tools-menu-note", SETTINGS_HELP));
    return menu;
  };
  if (names.length && editable) head.append(addControl());
  if (loading || error) section.append(el(doc, "p", "meta tools-status", loading ? "Loading this app’s tools…" : "Couldn’t read this app’s tools."));
  if (!names.length) {
    const empty = el(doc, "div", "tools-empty"), text = el(doc, "div", "empty-text");
    text.append(el(doc, "span", "", "No tools yet"), el(doc, "span", "quiet", EMPTY_SENTENCE));
    empty.append(text);
    if (editable) empty.append(addControl());
    section.append(empty);
    return section;
  }
  const list = el(doc, "ul", "test-list tool-sources");
  for (const server of ready ? servers : []) {
    const n = owned.count(server);
    if (!n) continue;
    const item = el(doc, "li", "test-item"), row = button("", () => onOpen(server), "test-row tool-source", "nav");
    row.setAttribute("data-server", server.name);
    row.append(el(doc, "span", "test-name", server.label), el(doc, "span", "test-input-summary", `MCP server · ${countText(n, server, owned.disabled(server))}`));
    item.append(row); list.append(item);
  }
  if (list.children.length) section.append(list);
  if (!ready) section.append(el(doc, "p", "quiet tools-unmatched", `${plural(names.length, "tool")} in the agent file`));
  else if (owned.unmatched) section.append(el(doc, "p", "quiet tools-unmatched", `${plural(owned.unmatched, "tool")} in the agent file can’t be matched to one of this app’s servers`));
  return section;
}

// Rows arrive in the caller's fixed opening order; filtering only hides rows.
// `servers` is the whole catalog, so the count matches the T1 row's ownership rule.
export function serverToolsView(doc, button, { agentName, server, servers = [server], rows, names, settings = [], filter, onFilter, onToggle, onStateToggle, onBack, onDiscard, onSubmit, dirty, footer, busy, editable = true, settingsEditable = editable }) {
  const ticked = new Set(names), owned = ownership(servers, names, settings);
  const head = el(doc, "div", "detail-head"), bar = el(doc, "div", "detail-bar");
  const back = button("", onBack, "back", "nav");
  back.setAttribute("aria-label", `Back to ${agentName}`); back.append(icon(doc, "back", 14, 2), agentName);
  bar.append(back);
  head.append(bar, el(doc, "h1", "test-title", server.label),
    el(doc, "p", "detail-status", `MCP server · ${countText(owned.count(server), server, owned.disabled(server))}`));

  const body = el(doc, "section", "detail-section tools-picker");
  const search = el(doc, "input", "tool-filter");
  search.type = "search"; search.value = filter ?? ""; search.disabled = !!busy;
  const placeholder = `Filter ${plural(server.tools.length, "tool")}`;
  search.setAttribute("placeholder", placeholder); search.setAttribute("aria-label", placeholder); search.setAttribute("spellcheck", "false");
  const list = el(doc, "ul", "tool-list"), more = el(doc, "p", "filter-note");
  const items = rows.map(tool => {
    const item = el(doc, "li", "tool-row"), label = el(doc, "label", "tool-label"), box = el(doc, "input", "tool-choice");
    box.type = "checkbox"; box.checked = ticked.has(tool.name);
    box.disabled = !!busy || (!tool.selectable && !box.checked);
    box.setAttribute("data-tool-name", tool.name);
    box.addEventListener("change", event => onToggle(tool.name, !!(event?.target ?? box).checked));
    const text = el(doc, "span", "tool-text");
    text.append(el(doc, "code", "tool-name", tool.name), el(doc, "span", "tool-description", tool.description ?? ""));
    label.append(box, text); item.append(label);
    if (ticked.has(tool.name) && settings !== null) {
      const value = settings.find(s => s.name === tool.name)?.state ?? "Enabled", cls = `tool-state${value === "Disabled" ? " is-disabled" : ""}`;
      const word = settingsEditable && editable ? button(value, () => onStateToggle?.(tool.name), `tb ${cls}`, "settings") : el(doc, "span", cls, value);
      word.setAttribute("data-tool-state", tool.name);
      if (settingsEditable && editable) {
        word.disabled = !!busy; word.setAttribute("role", "switch"); word.setAttribute("aria-checked", String(value === "Enabled"));
        word.setAttribute("aria-label", `${tool.name}: ${value} for tests`);
      }
      item.append(word);
    }
    list.append(item);
    return { item, tool };
  });
  const draw = () => {
    const query = search.value.trim().toLowerCase();
    let shown = 0;
    for (const { item, tool } of items) {
      const match = !query || tool.name.toLowerCase().includes(query) || (tool.description ?? "").toLowerCase().includes(query);
      item.hidden = !match || (!query && shown >= VISIBLE_ROWS);
      if (match) shown++;
    }
    more.textContent = !query && rows.length > VISIBLE_ROWS ? `${rows.length - VISIBLE_ROWS} more · filter to find a tool`
      : query && !shown ? "No tools match this filter." : "";
  };
  search.addEventListener("input", () => { onFilter(search.value); draw(); });
  // The page owns bare Enter; the search handles it here and stops it so one key press saves once.
  search.addEventListener("keydown", event => {
    if (event.isComposing || event.key !== "Enter" || event.shiftKey || event.ctrlKey || event.altKey || event.metaKey) return;
    event.preventDefault(); event.stopPropagation?.(); onSubmit();
  });
  body.append(search);
  if (editable) body.append(el(doc, "p", "quiet tools-hint", "Tick a tool to give it to the agent. Disabled tools stay in the agent but don't run in tests."));
  body.append(list, more);
  draw();

  const foot = el(doc, "div", "detail-foot"), actions = el(doc, "div", "foot-actions");
  foot.append(typeof footer === "string" ? el(doc, "span", "meta prompt-status", footer) : footer);
  if (dirty && editable) {
    const discard = button("", onDiscard, "ib tools-discard", "idle");
    discard.setAttribute("aria-label", "Discard these edits (Esc)"); discard.setAttribute("title", "Discard these edits (Esc)");
    discard.append(icon(doc, "close", 16, 2.2)); actions.append(discard);
  }
  if (editable) actions.append(saveButton(doc, button, "Save and run all tests", onSubmit, "ib save-run tools-save"));
  foot.append(actions);
  return [head, body, foot];
}
