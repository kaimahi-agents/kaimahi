import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { failureWording } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/failure-wording.mjs";
import { failureFacts } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/results.mjs";

test("generated wording matches the canonical JSON and keeps raw values", async () => {
  const json = JSON.parse(await readFile(new URL("../../copilot-wsl/com.github.copilot/extensions/agent-view/failure-wording.json", import.meta.url), "utf8"));
  for (const [type, template] of Object.entries(json)) assert.equal(failureWording(type, "$& x"), template.replace("{value}", () => "$& x"));
  assert.equal(failureWording("contains", 'a"b'), 'Missing "a"b"');
  assert.equal(failureWording("llmJudge", "x"), undefined);
  assert.equal(failureWording("toString", "x"), undefined);
});

test("failure facts use shared tool fallbacks", () => {
  for (const [type, text] of [["toolCalled", "Not met: Calls search"], ["toolNotCalled", "Not met: Doesn't call search"]]) {
    const a = { id: "a", type, fields: [["tool", "search"]] };
    const key = JSON.stringify([a.id, a.type]);
    const facts = failureFacts({ state: "fail", example: { assertions: [a] }, expected: new Map([[key, a]]), checks: new Map([[key, { verdict: "fail" }]]) });
    assert.equal(facts?.text, text);
  }
});
