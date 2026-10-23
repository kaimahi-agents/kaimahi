// Inert DOM editing and exact source serialization. No I/O or action authority.
export const el = (doc, tag, className, text) => {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};
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
    if (tag === "br") return "\n";
    const children = [...node.childNodes];
    if (["div", "p"].includes(tag) && node !== root && children.length === 1 && children[0].tagName?.toLowerCase() === "br") return "";
    if (inlineNodes.has(node) && children.some((child) => child.nodeType !== 3)) throw new Error("Unsupported inline formatting.");
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
  const inserted = doc.createTextNode(text);
  range.insertNode(inserted); range.setStartAfter(inserted); range.collapse(true);
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
