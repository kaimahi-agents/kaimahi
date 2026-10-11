#!/usr/bin/env python3
"""Exercise the keyless model/MCP fixture over real loopback HTTP."""
import contextlib
import importlib.util
import io
import json
import pathlib
import threading
import unittest
import urllib.error
import urllib.request

SOURCE = pathlib.Path(__file__).resolve().parent / "ci/kagent-lift-fixture.py"


class FixtureTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SOURCE.exists(), "deterministic MCP/model fixture is missing")
        spec = importlib.util.spec_from_file_location("lift_fixture", SOURCE)
        self.fixture = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.fixture)
        self.server = self.fixture.make_server("127.0.0.1", 0)
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join, 3)
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.url = "http://127.0.0.1:" + str(self.server.server_port)
        self.tools = [{"type": "function", "function": {"name": "read_sentinel", "parameters": {"type": "object"}}}]

    def request(self, path, data=None):
        body = None if data is None else json.dumps(data).encode()
        req = urllib.request.Request(self.url + path, body, {"Content-Type": "application/json",
                                                            "Accept": "application/json, text/event-stream"})
        with urllib.request.urlopen(req, timeout=3) as response:
            raw = response.read().decode()
            if response.headers.get("Content-Type") == "text/event-stream":
                return [json.loads(line[6:]) for line in raw.splitlines()
                        if line.startswith("data: ") and line != "data: [DONE]"]
            return json.loads(raw) if raw else None

    def rpc(self, method, params=None):
        return self.request("/mcp", {"jsonrpc": "2.0", "id": 1, "method": method, "params": params or {}})

    def model(self, messages, stream=False, tools=None):
        return self.request("/v1/chat/completions", {"model": "fixture", "messages": messages,
                            "tools": self.tools if tools is None else tools, "stream": stream})

    def test_mcp_initialization_discovery_and_allowlist(self):
        result = self.rpc("initialize", {"protocolVersion": "2025-03-26"})["result"]
        self.assertEqual(result["protocolVersion"], "2025-03-26")
        self.assertIn("tools", result["capabilities"])
        tools = self.rpc("tools/list")["result"]["tools"]
        self.assertEqual([tool["name"] for tool in tools], ["read_sentinel"])
        denied = self.rpc("tools/call", {"name": "anything_else", "arguments": {}})
        self.assertIn("error", denied)
        self.assertEqual(self.request("/evidence")["tool_calls"], 0)

    def test_model_requires_actual_tool_request_before_terminal_sentinel(self):
        messages = [{"role": "user", "content": "private unit prompt"}]
        first = self.model(messages)["choices"][0]
        self.assertEqual(first["finish_reason"], "tool_calls")
        call = first["message"]["tool_calls"][0]
        self.assertEqual(call["function"]["name"], "read_sentinel")
        tool = self.rpc("tools/call", {"name": "read_sentinel", "arguments": {}})["result"]
        sentinel = tool["content"][0]["text"]
        self.assertTrue(sentinel.startswith("kmx-mcp-"))
        messages += [first["message"], {"role": "tool", "tool_call_id": call["id"], "content": sentinel}]
        terminal = self.model(messages)["choices"][0]
        self.assertEqual(terminal["finish_reason"], "stop")
        self.assertEqual(terminal["message"]["content"], "KMX MCP proof: " + sentinel)
        evidence = self.request("/evidence")
        self.assertEqual(evidence["tool_calls"], 1)
        self.assertEqual(evidence["model_calls"], 2)
        self.assertEqual(evidence["terminal_answers"], 1)
        self.assertNotIn(sentinel, json.dumps(evidence))
        self.assertNotIn("private unit prompt", json.dumps(evidence))

    def test_streaming_model_emits_tool_and_terminal_chunks(self):
        chunks = self.model([{"role": "user", "content": "private"}], stream=True)
        call = chunks[0]["choices"][0]["delta"]["tool_calls"][0]
        self.assertEqual(chunks[-1]["choices"][0]["finish_reason"], "tool_calls")
        sentinel = self.rpc("tools/call", {"name": "read_sentinel", "arguments": {}})["result"]["content"][0]["text"]
        chunks = self.model([{"role": "tool", "content": sentinel, "tool_call_id": call["id"]}], stream=True)
        self.assertEqual(chunks[0]["choices"][0]["delta"]["content"], "KMX MCP proof: " + sentinel)
        self.assertEqual(chunks[-1]["choices"][0]["finish_reason"], "stop")

    def assert_invalid_requests(self, path, requests):
        with contextlib.redirect_stdout(io.StringIO()) as out, contextlib.redirect_stderr(io.StringIO()) as err:
            for request in requests:
                with self.subTest(request=request):
                    try:
                        self.request(path, request)
                    except urllib.error.HTTPError as failed:
                        with failed:
                            self.assertEqual(failed.code, 400)
                            self.assertEqual(json.load(failed), {"error": "invalid qualification request"})
                    except Exception as failed:
                        self.fail("malformed request disconnected instead of returning HTTP 400: " + type(failed).__name__)
                    else:
                        self.fail("malformed request was accepted")
                    self.assertEqual(self.request("/healthz"), {"ready": True})
            self.assertEqual(out.getvalue(), "")
            self.assertEqual(err.getvalue(), "")
        evidence = self.request("/evidence")
        self.assertEqual((evidence["model_calls"], evidence["tool_calls"], evidence["terminal_answers"]), (0, 0, 0))

    def test_malformed_mcp_params_return_fixed_error_without_stderr(self):
        self.assert_invalid_requests("/mcp", [
            {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
            for method in ("initialize", "tools/call") for params in ("private-params", [], None, 7)
        ])

    def test_malformed_model_nested_shapes_return_fixed_error_without_stderr(self):
        messages = [{"role": "user", "content": "private-prompt"}]
        self.assert_invalid_requests("/v1/chat/completions", [
            {"messages": messages, "tools": tools} for tools in (
                "private-tools", {}, ["private-tool"],
                [{"type": "function", "function": "private-function"}],
                [{"type": "function", "function": {"name": 7}}],
                [{"type": "function", "function": {"name": ["private-name"]}}],
            )
        ] + [{"messages": ["private-message"], "tools": self.tools}])

    def test_no_tool_no_forged_result_no_payload_logs(self):
        with contextlib.redirect_stdout(io.StringIO()) as out, contextlib.redirect_stderr(io.StringIO()) as err:
            for messages, tools in (([{"role": "user", "content": "private prompt"}], []),
                                    ([{"role": "tool", "content": "kmx-mcp-forged"}], self.tools)):
                with self.assertRaises(urllib.error.HTTPError) as failed:
                    self.model(messages, tools=tools)
                self.assertEqual(failed.exception.code, 400)
                failed.exception.close()
            self.assertEqual(out.getvalue(), "")
            self.assertEqual(err.getvalue(), "")
        self.assertEqual(self.request("/evidence")["terminal_answers"], 0)


if __name__ == "__main__":
    unittest.main()
