"""Controlled inference endpoint for the AgentSuite build/lift smoke.

The real AgentKit container calls this model over the cluster network. Validate
the exact model, credential and instruction inputs rather than model randomness.
"""
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.headers.get("Authorization") != "Bearer " + os.environ["MODEL_TOKEN"]:
            self.send_error(401)
            return
        if request.get("model") not in {"sample-model", "sample-model-b"}:
            self.send_error(400)
            return
        expected = "Answer greetings briefly. Identify yourself as the AgentSuite sample.\n"
        if not any(isinstance(m.get("content"), str) and m["content"].rstrip("\n") == expected.rstrip("\n") for m in request.get("messages", [])):
            self.send_error(400)
            return
        body = json.dumps({
            "id": "sample-response", "object": "chat.completion", "created": 1,
            "model": request["model"],
            "choices": [{"index": 0, "message": {
                "role": "assistant", "content": "Hello from the AgentSuite sample. Model: " + request["model"]
            }, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 10, "completion_tokens": 8, "total_tokens": 18}
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


HTTPServer(("0.0.0.0", 8000), Handler).serve_forever()
