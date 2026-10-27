// Inert DOM element and static icon helpers shared by the canvas views.

export const el = (doc, tag, className, text) => {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};
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
