// Display-only host inventory. A listed tool is selectable only when the
// existing execution resolver accepts its current metadata, not just tools/list.
import { freezeTools } from "./host-relay.mjs";

const unavailable = () => new Error("Tools are unavailable in this session.");
const text = (value, limit) => typeof value === "string" && value.length > 0 && value.length <= limit && !/[\p{Cc}\u202a-\u202e\u2066-\u2069]/u.test(value);

export async function readToolCatalog(session, excluded = []) {
  try {
    const metadata = (await session.rpc.tools.getCurrentMetadata())?.tools;
    if (!Array.isArray(metadata) || metadata.length > 4096) throw unavailable();
    const hidden = name => excluded.includes(name) || name === "kmx-agent-builder" || name.startsWith("kmx-agent-preview-");
    const exclusions = [...new Set([...excluded, ...metadata.filter(t => hidden(t?.mcpServerName ?? "")).map(t => t.mcpServerName)])];
    const names = [...new Set(metadata.filter(t => text(t?.mcpServerName, 512) && !hidden(t.mcpServerName)).map(t => t.mcpServerName))];
    if (names.length > 128) throw unavailable();
    if (!names.length) return { servers: [] };
    const configured = typeof session.rpc.mcp.listConfigured === "function" ? (await session.rpc.mcp.listConfigured())?.servers : [];
    if (!Array.isArray(configured)) throw unavailable();
    let total = 0;
    const servers = [];
    for (const name of names) {
      const inventory = (await session.rpc.mcp.listTools({ serverName: name }))?.tools;
      if (!Array.isArray(inventory)) throw unavailable();
      total += inventory.length;
      if (total > 4096 || new Set(inventory.map(t => t?.name)).size !== inventory.length) throw unavailable();
      const match = configured.filter(s => s.name === name);
      const label = match.length === 1 && text(match[0].displayName, 512) ? match[0].displayName : name;
      const tools = inventory.map(t => {
        if (!text(t?.name, 512) || t.description !== undefined && (typeof t.description !== "string" || t.description.length > 4096)) throw unavailable();
        let selectable = false;
        try { selectable = freezeTools(metadata, [t.name], exclusions).get(t.name)?.mcpServerName === name; } catch { /* Inventory alone grants no binding. */ }
        return { name: t.name, description: t.description ?? "", selectable };
      });
      servers.push({ name, label, tools });
    }
    if (Buffer.byteLength(JSON.stringify(servers)) > 512 * 1024) throw unavailable();
    return { servers };
  } catch { throw unavailable(); } // Never project SDK error bodies or configuration.
}
