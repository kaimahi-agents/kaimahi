// Checkout-only UI rehearsal over the real stdio MCP engine. No model or file
// operations are implemented here; the adapter only forwards JSON-RPC.
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { EventEmitter } from "node:events";
import { startSquadBridge } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-view.mjs";

const [binary, root, squad, provider] = process.argv.slice(2);
if (!binary || !root || !squad) throw new Error("Usage: node plugins/tests/copilot/squad-demo.mjs /absolute/kmx /absolute/demo-root /absolute/squad");
if (provider !== undefined && provider !== "copilot") throw new Error("The optional provider must be copilot.");
const child = spawn(binary, ["mcp", "--root", root, "--squad-program", squad, "--squad-release", "1.1.0", ...(provider ? ["--provider", provider] : [])], { stdio: ["pipe", "pipe", "inherit"] });
let next = 0;
const pending = new Map();
const events = new EventEmitter();
const lines = createInterface({ input: child.stdout });
lines.on("line", (line) => {
  let message; try { message = JSON.parse(line); } catch { return; }
  if (message.method === "notifications/progress") {
    const p = message.params;
    events.emit("tool.execution_progress", { data: { toolCallId: p.progressToken, progressMessage: `${p.progress}: ${p.message}` } });
    return;
  }
  const call = pending.get(message.id);
  if (!call) return;
  clearTimeout(call.timer); pending.delete(message.id);
  if (message.error) call.reject(new Error("MCP request failed")); else call.resolve(message.result);
});
child.on("exit", () => { for (const call of pending.values()) { clearTimeout(call.timer); call.reject(new Error("MCP exited")); } pending.clear(); });
function rpc(method, params) {
  const id = ++next;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { pending.delete(id); reject(new Error("MCP outcome unknown")); }, 310000);
    pending.set(id, { resolve, reject, timer }); child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
  });
}
await rpc("initialize", { protocolVersion: "2025-03-26", clientInfo: { name: "kmx-squad-browser-rehearsal", version: "0.1.0" }, capabilities: {} });
child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");
const session = {
  on: (name, listener) => { events.on(name, listener); return () => events.off(name, listener); },
  rpc: {
    mcp: { resources: { read: ({ uri }) => rpc("resources/read", { uri }) } },
    tools: {
      getCurrentMetadata: async () => ({ tools: (await rpc("tools/list", {})).tools.map((t) => ({ name: t.name, mcpToolName: t.name, mcpServerName: "kmx-agent-builder" })) }),
      execute: async ({ name, arguments: args, toolCallId }) => {
        const result = await rpc("tools/call", { name, arguments: args, ...(toolCallId ? { _meta: { progressToken: toolCallId } } : {}) });
        return { resultType: result.isError ? "failure" : "success", structuredContent: result.structuredContent };
      },
    },
  },
  send: async () => { throw new Error("Suggestions require the Copilot app session."); },
};
const bridge = await startSquadBridge({ session });
console.log(`Squad canvas rehearsal (real native MCP, ${provider || "deterministic fake"} previews):\n${bridge.url}`);
const close = async () => { await bridge.close(); lines.close(); child.kill(); process.exit(); };
process.on("SIGINT", close); process.on("SIGTERM", close);
