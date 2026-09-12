#!/usr/bin/env python3
"""A plain OpenAI **Responses API** endpoint, for proving the model seam.

Nothing about this server is Kaimahi's and nothing about it is a model:
it answers `POST /v1/responses` with a fixed completion and a usage
envelope, which is the only part of an upstream the meter reads. It also
answers `POST /v1/silent` with the same completion and NO usage at all,
which is the case the plane refuses rather than metering as zero. That is
deliberate — the proof this fixture exists for is that a Responses-API
call is *metered*, and a real model would make the token counts
unpredictable and the run slow, without making the proof stronger.

The counts are derived from the request so an assertion can name a
number the upstream chose rather than one the test wrote: input_tokens is
the number of whitespace-separated words in the request's `input`, and
output_tokens is the number of words in the answer. A ledger row that
matches those matches what the upstream reported.

Keyless by construction. It reads no credential, sends none, and has no
network egress of its own — see `plain-model.sh`, which gives it a
NetworkPolicy pair through `kmx models add`.

The bundled `ollama/ollama` image this repo pins has no `/v1/responses`
at all (measured 2026-09-08), which is exactly why this fixture is here:
a proof driven against the committed model upstreams would prove only
that chat-completions works.
"""

import json
import os
import ssl
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ANSWER = "a governed answer from a fixture"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _send(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/healthz":
            self._send(200, {"ok": True})
            return
        self._send(404, {"error": "not found"})

    def do_POST(self):
        length = int(self.headers.get("content-length", 0))
        raw = self.rfile.read(length) if length else b"{}"
        try:
            request = json.loads(raw or b"{}")
        except ValueError:
            self._send(400, {"error": {"message": "not JSON"}})
            return
        # Hosted-egress tests must observe a redirect rather than follow it.
        if self.path.rstrip("/") == "/redirect":
            self.send_response(307)
            self.send_header("Location", "https://example.invalid/refused")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        # `/v1/silent` is the deliberate bad case: a 200 with a real
        # answer in it and NO usage envelope at all. It is here because
        # the plane's decision about that case — refuse the call rather
        # than ledger it as zero — cannot be proven against an upstream
        # that always reports usage, and inventing the case by declaring
        # the wrong protocol would prove the config refusal instead.
        if self.path.rstrip("/") == "/v1/silent":
            self._send(200, {"id": "resp_silent", "status": "completed",
                             "output": [{"type": "message", "role": "assistant",
                                         "content": [{"type": "output_text", "text": ANSWER}]}]})
            return
        if self.path.rstrip("/") != "/v1/responses":
            self._send(404, {"error": {"message": "this fixture serves /v1/responses and /v1/silent"}})
            return
        # `input` is the Responses API's own field name for the prompt;
        # it is a string or a list of content parts, and only its size
        # matters here.
        prompt = request.get("input", "")
        if not isinstance(prompt, str):
            prompt = json.dumps(prompt)
        self._send(200, {
            "id": "resp_fixture",
            "object": "response",
            "status": "completed",
            "model": request.get("model", "fixture"),
            "output": [{
                "type": "message",
                "role": "assistant",
                "content": [{"type": "output_text", "text": ANSWER}],
            }],
            # The shape the whole lane is about: input_tokens and
            # output_tokens, NOT prompt_tokens and completion_tokens.
            "usage": {
                "input_tokens": len(prompt.split()),
                "output_tokens": len(ANSWER.split()),
                "total_tokens": len(prompt.split()) + len(ANSWER.split()),
            },
        })

    def log_message(self, fmt, *args):  # one line per call, on stderr
        print("plain-model: " + (fmt % args), flush=True)


if __name__ == "__main__":
    # A held keep-alive connection from one replica must not block another.
    server = ThreadingHTTPServer(("0.0.0.0", int(os.environ.get("PORT", "9000"))), Handler)
    cert, key = os.environ.get("TLS_CERT"), os.environ.get("TLS_KEY")
    if bool(cert) != bool(key):
        raise SystemExit("TLS_CERT and TLS_KEY must be supplied together")
    if cert:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
    print("serving " + ("https" if cert else "http"), flush=True)
    server.serve_forever()
