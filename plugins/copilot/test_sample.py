"""Sample contract and real CLI parser/MCP Create; never send to a model."""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SAMPLE = HERE / "samples/release-writer.agent.md"


class SampleTests(unittest.TestCase):
    def test_instruction_repair_keeps_checks(self):
        source = SAMPLE.read_text(encoding="utf-8")
        old = 'Omit issue numbers and issue references from the notes.'
        new = 'Keep every issue reference as "Fixes #N".'
        self.assertEqual(source.count(old), 1)
        self.assertNotIn(new, source)
        self.assertIn("value: 'Fixes #218'", source)
        self.assertEqual(source.count('type: llmJudge'), 3)
        self.assertEqual(source.count('judgeModel: claude-haiku-5.5'), 3)
        self.assertEqual(source.count('judgeVersion: default'), 3)
        self.assertEqual(source.count('minScore:'), 3)
        self.assertEqual(sum(line.startswith('  - id: ') for line in source.splitlines()), 3)
        self.assertIn('- **Preferred:** claude-haiku-5.5', source)
        self.assertIn('ask for approval', source.lower())
        repaired = source.replace(old, new)
        self.assertEqual(repaired.count(new), 1)
        self.assertEqual(source.split('## Examples\n', 1)[1], repaired.split('## Examples\n', 1)[1])

    @unittest.skipIf(os.name == "nt", "native Windows CLI is guarded")
    def test_real_mcp_create_parses_original_and_one_line_repair(self):
        with tempfile.TemporaryDirectory(prefix="sample parser ") as temp:
            base = Path(temp)
            binary = base / "kmx"
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/kmx"],
                           cwd=HERE.parents[1], check=True, capture_output=True, timeout=120,
                           env={**os.environ, "CGO_ENABLED": "0", "GOWORK": "off"})
            root = base / "selected"
            root.mkdir()
            source = SAMPLE.read_text(encoding="utf-8")
            repaired = source.replace('Omit issue numbers and issue references from the notes.',
                                      'Keep every issue reference as "Fixes #N".')
            with subprocess.Popen([str(binary), "mcp", "--root", str(root)],
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, text=True,
                                  env={**os.environ, "KMX_HOME": str(base / "home")}) as process:
                assert process.stdin is not None and process.stdout is not None and process.stderr is not None
                def call(identifier, method, params):
                    process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": identifier,
                                                    "method": method, "params": params}) + "\n")
                    process.stdin.flush()
                    return json.loads(process.stdout.readline())

                self.assertIn("result", call(1, "initialize", {
                    "protocolVersion": "2025-06-18", "capabilities": {},
                    "clientInfo": {"name": "sample-parser", "version": "1.0.0"}}))
                process.stdin.write(json.dumps({"jsonrpc": "2.0",
                                                "method": "notifications/initialized"}) + "\n")
                process.stdin.flush()
                for number, content in enumerate((source, repaired), 2):
                    path = f"release-{number}.agent.md"
                    result = call(number, "tools/call", {"name": "create", "arguments": {
                        "schemaVersion": 1, "path": path, "content": content}})
                    self.assertFalse(result["result"].get("isError"), result)
                    self.assertEqual((root / path).read_text(encoding="utf-8"), content)
                    resource = call(number + 10, "resources/read", {"uri": "kmx://agent/" + path})
                    parsed = json.loads(resource["result"]["contents"][0]["text"])
                    self.assertEqual(parsed["examples"][0]["assertions"][0]["value"], "Fixes #218")
                process.stdin.close()
                self.assertEqual(process.wait(timeout=5), 0)
                self.assertEqual(process.stderr.read(), "")
