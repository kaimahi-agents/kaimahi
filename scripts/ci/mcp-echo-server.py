#!/usr/bin/env python3
"""A tiny MCP (streamable-HTTP) echo server over https — CI's SYNTHETIC
hosted upstream (docs/hosted-upstreams.md).

It stands in for a real internet MCP server so the hardened dialer, the
opt-in egress allowance and the gateway's audit rows can be proven on a
kind cluster with no GitHub token anywhere. It is deliberately
minimal: no sessions, no SSE, no resources — just the four methods the
gateway relays, answered as plain JSON.

  POST /mcp        initialize / notifications/initialized / tools/list /
                   tools/call. Every tool echoes its arguments back; what
                   differs is what the committed table SAYS about each:
                     echo            allowlisted, read-only stand-in
                     echo_write      exists only so a NOT-allowlisted
                                     tool has a name
                     pay_invoice     declared policy fields and a
                                     money-shaped argument, for the
                                     standing-constraint checks
                     list_pull_requests  a read bound to one repository
                                     by a standing constraint
                     create_branch   a consequential call whose approval
                                     must bind the artifact
                     actions_run_trigger a CONSOLIDATED DISPATCHER:
                                     one tool whose `method` argument
                                     selects what it really does, which is
                                     how both real servers the release
                                     agent talks to are shaped

  It also honours X-MCP-Tools, the way GitHub's and Azure DevOps' hosted
  servers do: when the header is present, tools/list offers only the
  names it lists and tools/call refuses anything else with the upstream's
  own error. That is the OUTER of the two rings this project relies on —
  narrowing at the server, before discovery — and CI can only prove it
  against a stand-in that behaves the same way.
  POST /redirect   302 → /mcp, so the gateway's refusal of a redirecting
                   upstream can be exercised
  anything else    404

Usage: mcp-echo-server.py --cert server.crt --key server.key [--bind 0.0.0.0] [--port 443]

The certificate and key are generated at run time by
scripts/ci/synthetic-upstream.sh (a throwaway CA that lives for one job);
nothing here is committed key material.
"""
import argparse
import http.server
import json
import ssl
import sys


def _obj(**props):
    return {"type": "object", "properties": {k: {"type": v} for k, v in props.items()}}


# One list, so tools/list and the tools/call name check cannot drift apart.
TOOLS = [
    {"name": "echo", "description": "Echo the arguments back (read-only stand-in).",
     "inputSchema": _obj(text="string")},
    {"name": "echo_write", "description": "Echo the arguments back (a stand-in for a write tool).",
     "inputSchema": _obj(text="string")},
    {"name": "pay_invoice", "description": "Echo the arguments back (a stand-in for a consequential tool).",
     "inputSchema": _obj(invoice_id="string", amount_cents="integer", payee_id="string")},
    # Shaped exactly like the tools the release agent really calls.
    {"name": "list_pull_requests", "description": "Echo the arguments back (a read bound to one repository).",
     "inputSchema": _obj(owner="string", repo="string", state="string", base="string")},
    {"name": "create_branch", "description": "Echo the arguments back (a consequential call).",
     "inputSchema": _obj(owner="string", repo="string", branch="string", from_branch="string")},
    {"name": "actions_run_trigger", "description": "Echo the arguments back (a consolidated dispatcher).",
     "inputSchema": _obj(method="string", owner="string", repo="string", workflow_id="string", ref="string")},
]
TOOL_NAMES = {t["name"] for t in TOOLS}


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _offered(self, name):
        """Whether this server offers `name`, after X-MCP-Tools."""
        header = self.headers.get("X-MCP-Tools")
        if not header:
            return True
        return name in {p.strip() for p in header.split(",") if p.strip()}

    def log_message(self, fmt, *args):  # one line per request on stderr
        sys.stderr.write("mcp-echo: %s %s\n" % (self.command, fmt % args))

    def _send(self, status, body=b"", headers=()):
        self.send_response(status)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if body:
            self.wfile.write(body)

    def _rpc(self, msg_id, result=None, error=None):
        out = {"jsonrpc": "2.0", "id": msg_id}
        if error is not None:
            out["error"] = error
        else:
            out["result"] = result
        self._send(200, json.dumps(out).encode(), [("Content-Type", "application/json")])

    def do_POST(self):
        if self.path == "/redirect":
            self._send(302, b"", [("Location", "/mcp")])
            return
        if self.path != "/mcp":
            self._send(404, b"not found\n")
            return
        n = int(self.headers.get("Content-Length") or 0)
        try:
            msg = json.loads(self.rfile.read(n) or b"{}")
        except ValueError:
            self._send(400, b"not json\n")
            return
        method = msg.get("method")
        msg_id = msg.get("id")
        if method == "initialize":
            self._rpc(msg_id, {"protocolVersion": "2025-03-26", "capabilities": {"tools": {}},
                               "serverInfo": {"name": "kaimahi-ci-mcp-echo", "version": "0"}})
        elif method == "notifications/initialized" or msg_id is None:
            self._send(202)
        elif method == "tools/list":
            self._rpc(msg_id, {"tools": [t for t in TOOLS if self._offered(t["name"])]})
        elif method == "tools/call":
            params = msg.get("params") or {}
            name = params.get("name")
            if name not in TOOL_NAMES:
                self._rpc(msg_id, error={"code": -32602, "message": "unknown tool %r" % name})
                return
            # The server's own narrowing, which is what X-MCP-Tools buys
            # on the real servers: a tool the header excludes is not
            # merely un-allowlisted, it does not exist here.
            if not self._offered(name):
                self._rpc(msg_id, error={"code": -32602,
                                         "message": "tool %r is not enabled on this server" % name})
                return
            args = params.get("arguments") or {}
            self._rpc(msg_id, {"content": [{"type": "text", "text": "echo:" + json.dumps(args, sort_keys=True)}],
                               "isError": False})
        else:
            self._rpc(msg_id, error={"code": -32601, "message": "method not found"})


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cert", required=True)
    ap.add_argument("--key", required=True)
    ap.add_argument("--bind", default="0.0.0.0")
    ap.add_argument("--port", type=int, default=443)
    a = ap.parse_args()
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.minimum_version = ssl.TLSVersion.TLSv1_2
    ctx.load_cert_chain(a.cert, a.key)
    srv = http.server.ThreadingHTTPServer((a.bind, a.port), Handler)
    srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
    sys.stderr.write("mcp-echo: serving https on %s:%d\n" % (a.bind, a.port))
    srv.serve_forever()


if __name__ == "__main__":
    main()
