#!/usr/bin/env python3
"""Keyless OpenAI and stateless Streamable HTTP MCP qualification fixture.

The terminal text is unavailable until a real allowed MCP call occurred and
its unique result returned through the model's tool message. Evidence is only
counts and hashes; neither this server nor its HTTP logger prints payloads.
"""
import hashlib
import json
import secrets
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class State:
    def __init__(self):
        self.lock = threading.Lock()
        self.sentinel = "kmx-mcp-" + secrets.token_hex(24)
        self.model_calls = 0
        self.tool_calls = 0
        self.terminal_answers = 0
        self.judge_calls = 0
        self.cases = []

    def evidence(self):
        with self.lock:
            return {"model_calls": self.model_calls, "tool_calls": self.tool_calls,
                    "terminal_answers": self.terminal_answers, "judge_calls": self.judge_calls,
                    "cases": [dict(row) for row in self.cases],
                    "sentinel_sha256": hashlib.sha256(self.sentinel.encode()).hexdigest()}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def respond(self, status, data=None, content_type="application/json"):
        body = b"" if data is None else json.dumps(data, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/evidence":
            self.respond(200, self.server.state.evidence())
        elif self.path == "/healthz":
            self.respond(200, {"ready": True})
        elif self.path == "/mcp":
            self.respond(405)  # Stateless transport has no unsolicited SSE stream.
        else:
            self.respond(404)

    def do_DELETE(self):
        self.respond(200 if self.path == "/mcp" else 404)

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 1048576:
                raise ValueError("bounded request required")
            request = json.loads(self.rfile.read(length))
            if not isinstance(request, dict):
                raise ValueError("object required")
            if self.path == "/mcp":
                self.mcp(request)
            elif self.path == "/v1/chat/completions":
                self.model(request)
            else:
                self.respond(404)
        except (ValueError, TypeError, KeyError):
            self.respond(400, {"error": "invalid qualification request"})

    def mcp(self, request):
        if request.get("jsonrpc") != "2.0":
            raise ValueError("JSON-RPC required")
        method = request.get("method")
        if method == "notifications/initialized":
            self.respond(202)
            return
        response = {"jsonrpc": "2.0", "id": request["id"]}
        if method == "initialize":
            params = request["params"]
            if not isinstance(params, dict) or not isinstance(params.get("protocolVersion"), str):
                raise ValueError("initialization params required")
            response["result"] = {"protocolVersion": params["protocolVersion"],
                                  "capabilities": {"tools": {}},
                                  "serverInfo": {"name": "kmx-lift-fixture", "version": "1"}}
        elif method == "tools/list":
            response["result"] = {"tools": [{"name": "read_sentinel", "description": "Return the live qualification marker",
                                    "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False}}]}
        elif method == "tools/call":
            params = request.get("params", {})
            if not isinstance(params, dict):
                raise ValueError("tool params required")
            if params.get("name") != "read_sentinel" or params.get("arguments", {}) != {}:
                response["error"] = {"code": -32602, "message": "tool is not allowed"}
            else:
                with self.server.state.lock:
                    pending = [row for row in self.server.state.cases if row["tool_calls"] == 0 and row["terminal_answers"] == 0]
                    if len(pending) != 1:
                        raise ValueError("one pending model tool call required")
                    pending[0]["tool_calls"] = 1
                    self.server.state.tool_calls += 1
                    sentinel = self.server.state.sentinel
                response["result"] = {"content": [{"type": "text", "text": sentinel}], "isError": False}
        elif method == "ping":
            response["result"] = {}
        else:
            response["error"] = {"code": -32601, "message": "method not supported"}
        self.respond(200, response)

    def model(self, request):
        messages = request["messages"]
        if not isinstance(messages, list) or not messages or not all(isinstance(message, dict) for message in messages):
            raise ValueError("messages required")
        if request.get("model") == "fixture-judge":
            if ("temperature" in request or "max_tokens" in request or request.get("max_completion_tokens") != 1024 or
                    request.get("stream", False) is not False or request.get("tools")):
                raise ValueError("modern independent judge request required")
            if [message.get("role") for message in messages] != ["system", "user"]:
                raise ValueError("judge messages required")
            payload = json.loads(messages[1]["content"])
            if not isinstance(payload, dict) or set(payload) != {"input", "answer", "rubric"} or not all(isinstance(v, str) for v in payload.values()):
                raise ValueError("judge payload required")
            with self.server.state.lock:
                self.server.state.judge_calls += 1
            self.respond(200, {"model": "fixture-judge", "choices": [{"finish_reason": "stop", "message": {
                "role": "assistant", "content": json.dumps({"score": 100, "reasoning": "Synthetic fixture rubric satisfied."})}}]})
            return
        advertised = request.get("tools", [])
        if not isinstance(advertised, list) or not all(isinstance(tool, dict) for tool in advertised):
            raise ValueError("tools required")
        tools = []
        for tool in advertised:
            if tool.get("type") == "function":
                function = tool.get("function")
                if not isinstance(function, dict) or not isinstance(function.get("name"), str):
                    raise ValueError("function name required")
                tools.append(function["name"])
        # Kagent may namespace the advertised function. Emit the actual
        # advertised spelling, but require the sole allowlisted tool suffix.
        names = [name for name in tools if name == "read_sentinel" or name.endswith("__read_sentinel")]
        users = [message for message in messages if message.get("role") == "user"]
        if len(users) != 1:
            raise ValueError("one fresh user turn required")
        content = users[0].get("content")
        if isinstance(content, list) and all(isinstance(part, dict) and part.get("type") == "text" and isinstance(part.get("text"), str) for part in content):
            content = "\n".join(part["text"] for part in content)
        if not isinstance(content, str) or not content:
            raise ValueError("user text required")
        input_hash = hashlib.sha256(content.encode()).hexdigest()
        returned = [message for message in messages if message.get("role") == "tool"]
        with self.server.state.lock:
            if request.get("model") == "fixture-suite":
                # The Go ADK advertises its built-in ask_user even without any
                # authored tool binding. Never request it or an external tool.
                if tools not in ([], ["ask_user"]) or returned:
                    raise ValueError("suite fixture requires a fresh turn with no tool execution")
                answer = "KMX suite proof: " + self.server.state.sentinel
                row = {"call_id": "call-" + secrets.token_hex(16), "input_sha256": input_hash,
                       "model_calls": 0, "tool_calls": 0, "terminal_answers": 1, "user_turns": 1,
                       "answer_sha256": hashlib.sha256(answer.encode()).hexdigest()}
                self.server.state.cases.append(row)
                self.server.state.terminal_answers += 1
                message, finish = {"role": "assistant", "content": answer}, "stop"
            elif returned:
                rows = [row for row in self.server.state.cases if row["call_id"] == returned[-1].get("tool_call_id")]
                # MCP may be serialized as text or a JSON envelope by the ADK.
                if len(returned) != 1 or len(rows) != 1 or rows[0]["input_sha256"] != input_hash or rows[0]["tool_calls"] != 1 or rows[0]["terminal_answers"] != 0 or not isinstance(returned[0].get("content"), str) or self.server.state.sentinel not in returned[0]["content"]:
                    raise ValueError("actual fresh tool result required")
                row = rows[0]
                answer = "KMX MCP proof: " + self.server.state.sentinel
                message = {"role": "assistant", "content": answer}
                row["terminal_answers"] = 1
                row["answer_sha256"] = hashlib.sha256(answer.encode()).hexdigest()
                finish = "stop"
                self.server.state.terminal_answers += 1
            else:
                if len(names) != 1:
                    raise ValueError("one allowed MCP tool required")
                row = {"call_id": "call-" + secrets.token_hex(16), "input_sha256": input_hash,
                       "model_calls": 0, "tool_calls": 0, "terminal_answers": 0, "user_turns": 1}
                self.server.state.cases.append(row)
                message = {"role": "assistant", "content": None, "tool_calls": [{
                    "id": row["call_id"], "type": "function", "function": {"name": names[0], "arguments": "{}"}}]}
                finish = "tool_calls"
            row["model_calls"] += 1
            self.server.state.model_calls += 1
        base = {"id": "chatcmpl-kmx-lift", "created": 1, "model": request.get("model", "fixture")}
        if request.get("stream"):
            delta = dict(message)
            if "tool_calls" in delta:
                delta["tool_calls"][0]["index"] = 0
            chunks = [dict(base, object="chat.completion.chunk", choices=[{"index": 0, "delta": delta, "finish_reason": None}]),
                      dict(base, object="chat.completion.chunk", choices=[{"index": 0, "delta": {}, "finish_reason": finish}])]
            body = ("".join("data: " + json.dumps(chunk) + "\n\n" for chunk in chunks) + "data: [DONE]\n\n").encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            self.respond(200, dict(base, object="chat.completion", choices=[{"index": 0, "message": message,
                                                                         "finish_reason": finish}],
                                  usage={"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}))


def make_server(host="0.0.0.0", port=9000):
    server = ThreadingHTTPServer((host, port), Handler)
    server.state = State()
    return server


if __name__ == "__main__":
    make_server().serve_forever()
