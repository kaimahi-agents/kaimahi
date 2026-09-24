#!/usr/bin/env python3
"""The owner-managed application `kmx migrate` is proven against.

This is not a Kaimahi component and nothing here is imported by kmx. It
stands in for a workload this project did not write: a Deployment its own
owner runs, configured entirely through environment variables, which a
migration repoints at the governed model seam by patching four of them
and mounting one file.

It uses the standard library alone, on purpose. The claim the migration
makes is that an application needs no source edit, no rebuilt image and no
SDK of ours; a fixture that reached for `openai` would be proving that
SDK's behaviour instead, and the recorded kind and AKS migrations
(docs/migrate.md) used a standard-library client for the same reason.

What it reads is exactly what the generated patch writes:

  OPENAI_BASE_URL     the seam, ending at /v1 — this client appends
                      `responses`, which is the client path the committed
                      `orka` upstream declares
  OPENAI_API_KEY      the plane credential, sent as a bearer token
  OPENAI_CHAT_MODEL   the model name the endpoint resolves
  SSL_CERT_FILE       the authority that signs the seam

Three behaviours are load-bearing for the evidence and are pinned by
scripts/test_owner_model_client.py:

  * the credential is sent as a bearer token and is printed by nothing —
    `config` reports whether one is present, never what it is, and an
    error body that echoed it back is redacted before it is reported;
  * an upstream refusal keeps its status. The shard's budget step reads
    429 off this output, so masking one would leave that step green while
    the budget did nothing;
  * TLS is verified against SSL_CERT_FILE, and a missing or unusable
    authority fails the call. There is no unverified fallback: one would
    keep the shard passing on the day the seam's certificate stopped
    being valid.

Usage (all three are what the shard runs, through `kubectl exec`):

    owner-model-client.py serve         health endpoint; the pod's command
    owner-model-client.py config        the wiring it would use, as JSON
    owner-model-client.py ask <prompt>  one real turn, as JSON

`ask` exits 0 whenever the seam produced a STATUS — including a refusal,
which is an answer about governance — and nonzero when no status could be
obtained at all. Callers assert on the `status` field rather than on the
exit code.
"""

import json
import os
import ssl
import sys
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BASE_URL_VAR = "OPENAI_BASE_URL"
KEY_VAR = "OPENAI_API_KEY"
MODEL_VAR = "OPENAI_CHAT_MODEL"
CA_VAR = "SSL_CERT_FILE"

# A local 3B model on a two-CPU runner is slow, and the seam's own
# upstream bound is five minutes; timing out earlier than the seam would
# report a transport failure for a call that was still being answered.
TIMEOUT = float(os.environ.get("OWNER_REQUEST_TIMEOUT", "300"))
MAX_OUTPUT_TOKENS = int(os.environ.get("OWNER_MAX_OUTPUT_TOKENS", "64"))


class Misconfigured(Exception):
    """The environment does not describe a call that could be made."""


def configuration(env=None):
    """The wiring, read from the environment and refused where absent."""
    env = os.environ if env is None else env
    base = (env.get(BASE_URL_VAR) or "").strip().rstrip("/")
    if not base:
        raise Misconfigured(f"{BASE_URL_VAR} is not set: this application is configured by its "
                            f"environment and has no endpoint to call")
    model = (env.get(MODEL_VAR) or "").strip()
    if not model:
        raise Misconfigured(f"{MODEL_VAR} is not set: the endpoint resolves a model by name and "
                            f"this client never invents one")
    return {"base_url": base, "model": model, "key": env.get(KEY_VAR) or "",
            "ca_file": (env.get(CA_VAR) or "").strip() or None}


def config(env=None):
    """What `configuration` found, with the credential reduced to a fact."""
    wiring = configuration(env)
    return {"base_url": wiring["base_url"], "model": wiring["model"],
            "credential": "present" if wiring["key"] else "absent",
            "ca_file": wiring["ca_file"]}


def tls_context(base_url, ca_file):
    """The authority to verify the seam against, or None for plain HTTP.

    `create_default_context` verifies and checks the hostname, and neither
    is relaxed anywhere in this file. A named authority that cannot be
    read is a refusal rather than a fall back to the system trust store,
    which has never heard of the plane and would fail later and less
    legibly.
    """
    if not base_url.lower().startswith("https://"):
        return None
    if ca_file is None:
        return ssl.create_default_context()
    try:
        return ssl.create_default_context(cafile=ca_file)
    except OSError as err:
        raise Misconfigured(f"{CA_VAR}={ca_file} cannot be read as a certificate authority "
                            f"({err}); refusing to connect without verifying the seam") from None


def output_text(envelope):
    """The assistant's words out of a Responses envelope."""
    parts = []
    for item in envelope.get("output") or []:
        if item.get("type") != "message":
            continue
        for part in item.get("content") or []:
            if part.get("type") == "output_text":
                parts.append(part.get("text") or "")
    return "".join(parts).strip()


def redact(text, secret):
    return text.replace(secret, "[redacted]") if secret else text


def ask(prompt, env=None):
    """One real turn through whatever the environment points at.

    The request body is the smallest one the seam's translation accepts:
    no `previous_response_id` and no `store`, both of which it refuses
    because it holds no conversation state, and no `stream`, which it
    refuses on a translating upstream.
    """
    wiring = configuration(env)
    context = tls_context(wiring["base_url"], wiring["ca_file"])
    body = json.dumps({"model": wiring["model"], "input": prompt,
                       "max_output_tokens": MAX_OUTPUT_TOKENS}).encode()
    headers = {"Content-Type": "application/json"}
    if wiring["key"]:
        headers["Authorization"] = "Bearer " + wiring["key"]
    request = urllib.request.Request(wiring["base_url"] + "/responses", data=body,
                                     headers=headers, method="POST")
    result = {"status": None, "model": wiring["model"], "text": "",
              "input_tokens": None, "output_tokens": None, "response_id": None, "error": None}
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT, context=context) as response:
            payload = json.loads(response.read().decode() or "{}")
            result["status"] = response.status
    except urllib.error.HTTPError as err:
        # A refusal is an answer about governance, so its status and its
        # message are carried out rather than raised away.
        result["status"] = err.code
        result["error"] = redact(err.read().decode(errors="replace").strip(), wiring["key"])
        err.close()
        return result
    except (urllib.error.URLError, OSError, ValueError) as err:
        result["error"] = redact(str(err), wiring["key"])
        return result
    result["text"] = output_text(payload)
    result["response_id"] = payload.get("id")
    # Usage is reported only where the upstream reported it. Zeros here
    # would put a number on a call nobody counted, which is the case the
    # plane refuses rather than meters.
    usage = payload.get("usage")
    if isinstance(usage, dict):
        result["input_tokens"] = usage.get("input_tokens")
        result["output_tokens"] = usage.get("output_tokens")
    return result


class Health(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        body = b'{"ok": true}' if self.path == "/healthz" else b'{"error": "not found"}'
        self.send_response(200 if self.path == "/healthz" else 404)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print("owner-ci: " + (fmt % args), flush=True)


def main(argv):
    command = argv[1] if len(argv) > 1 else "serve"
    if command == "serve":
        print("owner-ci: serving /healthz", flush=True)
        ThreadingHTTPServer(("0.0.0.0", int(os.environ.get("PORT", "9000"))), Health).serve_forever()
        return 0
    if command == "config":
        print(json.dumps(config()))
        return 0
    if command == "ask":
        if len(argv) < 3:
            print("usage: owner-model-client.py ask <prompt>", file=sys.stderr)
            return 2
        result = ask(" ".join(argv[2:]))
        print(json.dumps(result))
        return 0 if result["status"] is not None else 1
    print("usage: owner-model-client.py serve|config|ask <prompt>", file=sys.stderr)
    return 2


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv))
    except Misconfigured as problem:
        print(json.dumps({"status": None, "error": str(problem)}))
        sys.exit(1)
