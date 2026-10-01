"""Deterministic Responses API fixture for a read-only Orka health Tool call."""

import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def send_json(self, status, payload):
        body = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.send_json(200, {"ok": True})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length))
        inputs = request.get("input", [])
        if not isinstance(inputs, list):
            inputs = []
        results = [item for item in inputs
                   if isinstance(item, dict) and item.get("type") == "function_call_output"]
        if not results:
            output = [{
                "type": "function_call",
                "id": "fc_k8s",
                "call_id": "call_k8s",
                "name": "k8s-get-resources",
                "arguments": '{"resource":"deployments","namespace":"orka-system"}',
                "status": "completed",
            }]
        else:
            try:
                raw_result = results[-1]["output"]
                # Orka prefixes the Tool result with a label.
                result = json.loads(raw_result[raw_result.index("{"):])
                marker = os.environ.get("ORKA_HEALTH_DEPLOYMENT_MARKER")
                if (not marker or result["resource"] != "deployments" or
                        not isinstance(result["items"], list) or
                        not any(isinstance(item, dict) and item.get("name") == marker and
                                item.get("namespace") == "orka-system" for item in result["items"])):
                    raise ValueError("live health deployment missing")
            except (KeyError, ValueError, TypeError):
                self.send_json(500, {"error": {"message": "health inventory unavailable"}})
                return
            output = [{
                "type": "message",
                "id": "msg_k8s",
                "status": "completed",
                "role": "assistant",
                "content": [{"type": "output_text", "text": "Health inventory checked.", "annotations": []}],
            }]
        self.send_json(200, {
            "id": "resp_orka_tool_proof",
            "object": "response",
            "created_at": 1,
            "status": "completed",
            "model": request.get("model", "tool-fixture"),
            "output": output,
            "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
        })

    def log_message(self, fmt, *args):
        pass  # Never log HTTP requests, prompts, model output or Tool results.


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
