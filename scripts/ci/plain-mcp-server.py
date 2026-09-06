#!/usr/bin/env python3
"""A plain in-cluster MCP server that Kaimahi did not write.

This exists for one reason: proving the GENERIC onboarding path
(docs/govern-your-agent.md). A proof driven against `kagent-tools`,
`slack`, `github` or the demo ERP proves nothing about onboarding an
arbitrary server, because each of those four has a hand-written
NetworkPolicy pair, a hand-written table entry and a hand-written
RemoteMCPServer committed in this repo. This one has none of that. It is
what an adopter arrives with.

It is deliberately unlike our four in the ways that matter:

  - plain http, in-cluster, in its OWN namespace (not `kaimahi`), so the
    scaffolded policy pair has to carry a namespaceSelector;
  - its Service publishes 8090 while the container listens on 9090, so a
    NetworkPolicy written against the Service port would block every call
    while reading as correct — the mistake a human makes and kmx does not;
  - its tools are its own (`stock_get`, `stock_adjust`), one of them
    consequential, so the policy_fields choice is a real choice.

  POST /mcp   initialize / notifications/initialized / tools/list /
              tools/call. Every tools/call echoes its arguments back and
              is recorded, so a call that got through can be told from
              one that did not.
  GET /calls  the calls this server actually served, newest last. The
              audit says what the PLANE decided; this says what the
              SERVER saw. A denial that both audits as denied and never
              arrives here is a denial that happened.
  anything else  404

Usage: plain-mcp-server.py [--bind 0.0.0.0] [--port 9090]
"""
import argparse
import http.server
import json
import sys
import threading

TOOLS = [
    {"name": "stock_get",
     "description": "Read the stock level of one SKU.",
     "inputSchema": {"type": "object", "properties": {"sku": {"type": "string"}},
                     "required": ["sku"]}},
    {"name": "stock_adjust",
     "description": "Change the stock level of one SKU by a signed delta.",
     "inputSchema": {"type": "object", "properties": {
         "sku": {"type": "string"}, "delta": {"type": "integer"}},
         "required": ["sku", "delta"]}},
]
TOOL_NAMES = {t["name"] for t in TOOLS}

_lock = threading.Lock()
_calls = []


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        sys.stderr.write("acme-warehouse: %s %s\n" % (self.command, fmt % args))

    def _send(self, status, body=b"", headers=()):
        self.send_response(status)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if body:
            self.wfile.write(body)

    def _json(self, status, payload):
        self._send(status, json.dumps(payload).encode(), [("Content-Type", "application/json")])

    def _rpc(self, msg_id, result=None, error=None):
        out = {"jsonrpc": "2.0", "id": msg_id}
        if error is not None:
            out["error"] = error
        else:
            out["result"] = result
        self._json(200, out)

    def do_GET(self):
        if self.path != "/calls":
            self._send(404, b"not found\n")
            return
        with _lock:
            self._json(200, {"calls": list(_calls)})

    def do_POST(self):
        if self.path != "/mcp":
            self._send(404, b"not found\n")
            return
        n = int(self.headers.get("Content-Length") or 0)
        try:
            msg = json.loads(self.rfile.read(n) or b"{}")
        except ValueError:
            self._send(400, b"not json\n")
            return
        method, msg_id = msg.get("method"), msg.get("id")
        if method == "initialize":
            self._rpc(msg_id, {"protocolVersion": "2025-03-26", "capabilities": {"tools": {}},
                               "serverInfo": {"name": "acme-warehouse", "version": "0"}})
        elif method == "notifications/initialized" or msg_id is None:
            self._send(202)
        elif method == "tools/list":
            self._rpc(msg_id, {"tools": TOOLS})
        elif method == "tools/call":
            params = msg.get("params") or {}
            name = params.get("name")
            if name not in TOOL_NAMES:
                self._rpc(msg_id, error={"code": -32602, "message": "unknown tool %r" % name})
                return
            args = params.get("arguments") or {}
            with _lock:
                _calls.append({"tool": name, "arguments": args})
            self._rpc(msg_id, {"content": [{"type": "text",
                                            "text": "acme-warehouse %s:%s" % (name, json.dumps(args, sort_keys=True))}],
                               "isError": False})
        else:
            self._rpc(msg_id, error={"code": -32601, "message": "method not found"})


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bind", default="0.0.0.0")
    ap.add_argument("--port", type=int, default=9090)
    a = ap.parse_args()
    srv = http.server.ThreadingHTTPServer((a.bind, a.port), Handler)
    sys.stderr.write("acme-warehouse: serving http on %s:%d\n" % (a.bind, a.port))
    srv.serve_forever()


if __name__ == "__main__":
    main()
