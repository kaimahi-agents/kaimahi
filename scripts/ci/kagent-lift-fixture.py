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

    def evidence(self):
        with self.lock:
            return {"model_calls": self.model_calls, "tool_calls": self.tool_calls,
                    "terminal_answers": self.terminal_answers,
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
        returned = [message.get("content", "") for message in messages if message.get("role") == "tool"]
        with self.server.state.lock:
            if returned:
                # The result can be the MCP text or its JSON envelope depending
                # on the ADK serializer. Only the live marker qualifies it.
                if not self.server.state.tool_calls or not any(
                        self.server.state.sentinel in value for value in returned if isinstance(value, str)):
                    raise ValueError("actual tool result required")
                message = {"role": "assistant", "content": "KMX MCP proof: " + self.server.state.sentinel}
                finish = "stop"
                self.server.state.terminal_answers += 1
            else:
                if len(names) != 1:
                    raise ValueError("one allowed MCP tool required")
                message = {"role": "assistant", "content": None, "tool_calls": [{
                    "id": "call-kmx-sentinel", "type": "function", "function": {"name": names[0], "arguments": "{}"}}]}
                finish = "tool_calls"
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
