"""The owner-managed application fixture, proven before a cluster runs it.

`scripts/ci/owner-model-client.py` is the workload `kmx migrate` is proven
against, shared by the e2e-resilience and e2e-spend shards. It stands in
for an application this project did not write: it reads the four
variables the generated patch sets and nothing else, and it speaks the
Responses API the committed `orka` upstream declares as its client path.

Everything both shards conclude from that fixture depends on three
properties which a live run cannot separate from the plane's own
behaviour, and which are therefore proven here:

  * the credential reaches the seam as a bearer token and never leaves
    this process by any other route — not through `config`, not through
    an error body the seam echoed back;
  * an upstream refusal is REPORTED, not masked. The budget assertion in
    e2e-spend reads `status` and would pass on a client that swallowed a
    429 and printed a plausible sentence;
  * TLS is verified against the authority the patch mounts. A client that
    fell back to an unverified connection would keep both shards green on
    the day the seam's certificate stopped being valid — which is the one
    failure the seam's TLS exists to produce.
"""

import importlib.util
import json
import pathlib
import shutil
import ssl
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

_spec = importlib.util.spec_from_file_location(
    "owner_model_client", pathlib.Path(__file__).parent / "ci" / "owner-model-client.py")
client = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(client)

TOKEN = "kmh_" + "b" * 64
ANSWER = "BRIDGE WORKS"


def envelope(text=ANSWER, in_tokens=39, out_tokens=5):
    return {
        "id": "resp_x", "object": "response", "status": "completed", "model": "local/qwen2.5:3b",
        "output": [{"type": "message", "role": "assistant",
                    "content": [{"type": "output_text", "text": text}]}],
        "usage": {"input_tokens": in_tokens, "output_tokens": out_tokens,
                  "total_tokens": in_tokens + out_tokens},
    }


class Seam(BaseHTTPRequestHandler):
    """A stand-in for the plane's model seam. `reply` is set per test."""

    protocol_version = "HTTP/1.1"
    reply = (200, envelope())

    def do_POST(self):
        length = int(self.headers.get("content-length", 0))
        Seam.seen = {"path": self.path, "auth": self.headers.get("Authorization"),
                     "type": self.headers.get("Content-Type"),
                     "body": json.loads(self.rfile.read(length) or b"{}")}
        status, payload = Seam.reply
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        pass


def serve(tls=None):
    """Start the stand-in seam; returns (base_url, stop)."""
    server = ThreadingHTTPServer(("127.0.0.1", 0), Seam)
    scheme = "http"
    if tls is not None:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(tls[0], tls[1])
        server.socket = context.wrap_socket(server.socket, server_side=True)
        scheme = "https"
    threading.Thread(target=server.serve_forever, daemon=True).start()

    def stop():
        server.shutdown()
        server.server_close()

    return f"{scheme}://127.0.0.1:{server.server_port}/upstream/orka/v1", stop


def environment(base_url, ca_file=None, model="local/qwen2.5:3b", token=TOKEN):
    env = {client.BASE_URL_VAR: base_url, client.KEY_VAR: token, client.MODEL_VAR: model}
    if ca_file:
        env[client.CA_VAR] = ca_file
    return env


class ConfigurationTests(unittest.TestCase):
    def test_the_four_variables_the_patch_sets_are_the_ones_read(self):
        self.assertEqual(
            [client.BASE_URL_VAR, client.KEY_VAR, client.MODEL_VAR, client.CA_VAR],
            ["OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_CHAT_MODEL", "SSL_CERT_FILE"])

    def test_a_missing_base_url_or_model_is_refused_rather_than_guessed(self):
        for env in ({}, {client.BASE_URL_VAR: "https://seam/v1"},
                    {client.MODEL_VAR: "local/qwen2.5:3b"},
                    {client.BASE_URL_VAR: "  ", client.MODEL_VAR: "m"}):
            with self.subTest(env=env), self.assertRaises(client.Misconfigured):
                client.configuration(env)

    # `config` is what the shard reads to say the owner's patch took
    # effect. It must be able to say so without ever being a way to read
    # the credential out of the pod.
    def test_config_reports_the_wiring_and_never_the_credential(self):
        report = client.config(environment("https://seam/upstream/orka/v1", "/etc/kaimahi/plane-ca/ca.crt"))
        self.assertEqual(report, {"base_url": "https://seam/upstream/orka/v1",
                                  "model": "local/qwen2.5:3b",
                                  "credential": "present", "ca_file": "/etc/kaimahi/plane-ca/ca.crt"})
        self.assertNotIn(TOKEN, json.dumps(report))

    def test_an_absent_credential_is_reported_as_absent_not_as_empty_success(self):
        report = client.config({client.BASE_URL_VAR: "https://seam/v1", client.MODEL_VAR: "m"})
        self.assertEqual(report["credential"], "absent")
        self.assertIsNone(report["ca_file"])


class RequestTests(unittest.TestCase):
    def setUp(self):
        Seam.reply = (200, envelope())
        Seam.seen = None
        self.base, stop = serve()
        self.addCleanup(stop)

    def test_the_call_is_a_responses_request_carrying_the_bearer_credential(self):
        client.ask("Reply with exactly this text: BRIDGE WORKS", environment(self.base))
        self.assertEqual(Seam.seen["path"], "/upstream/orka/v1/responses")
        self.assertEqual(Seam.seen["auth"], f"Bearer {TOKEN}")
        self.assertEqual(Seam.seen["type"], "application/json")
        self.assertEqual(Seam.seen["body"]["model"], "local/qwen2.5:3b")
        self.assertEqual(Seam.seen["body"]["input"], "Reply with exactly this text: BRIDGE WORKS")
        # The seam refuses both of these outright, so sending either would
        # turn every turn into a refusal the shard could not explain.
        self.assertNotIn("previous_response_id", Seam.seen["body"])
        self.assertFalse(Seam.seen["body"].get("store", False))
        self.assertFalse(Seam.seen["body"].get("stream", False))

    def test_the_answer_and_its_token_counts_come_out_of_the_envelope(self):
        result = client.ask("hello", environment(self.base))
        self.assertEqual(result["status"], 200)
        self.assertEqual(result["text"], ANSWER)
        self.assertEqual(result["input_tokens"], 39)
        self.assertEqual(result["output_tokens"], 5)

    # The shard's budget step asserts `status == 429`. A client that
    # turned a refusal into an empty answer would leave that step green
    # while the budget did nothing.
    def test_an_upstream_refusal_is_reported_with_its_status_and_message(self):
        Seam.reply = (429, {"error": {"message": "monthly token budget reached"}})
        result = client.ask("hello", environment(self.base))
        self.assertEqual(result["status"], 429)
        self.assertEqual(result["text"], "")
        self.assertIn("monthly token budget reached", result["error"])

    def test_a_credential_echoed_back_by_the_upstream_is_not_reprinted(self):
        Seam.reply = (401, {"error": {"message": f"unknown credential {TOKEN}"}})
        result = client.ask("hello", environment(self.base))
        self.assertEqual(result["status"], 401)
        self.assertNotIn(TOKEN, json.dumps(result))
        self.assertIn("[redacted]", result["error"])

    # A 200 with no usage is what the plane refuses rather than meters,
    # so the fixture must not invent zeros for one either.
    def test_a_reply_without_usage_reports_no_token_counts(self):
        body = envelope()
        del body["usage"]
        Seam.reply = (200, body)
        result = client.ask("hello", environment(self.base))
        self.assertIsNone(result["input_tokens"])
        self.assertIsNone(result["output_tokens"])


class TLSTests(unittest.TestCase):
    """The mounted authority is verified, and nothing else is accepted."""

    @classmethod
    def setUpClass(cls):
        openssl = shutil.which("openssl")
        if openssl is None:  # not skipped: an unverifiable claim is a failure
            raise AssertionError("openssl is required to prove the seam's authority is verified")
        cls.dir = pathlib.Path(tempfile.mkdtemp(prefix="owner-model-client-tls-"))
        cls.cert, cls.key = cls.dir / "seam.crt", cls.dir / "seam.key"
        subprocess.run([openssl, "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                        "-keyout", str(cls.key), "-out", str(cls.cert), "-days", "1",
                        "-subj", "/CN=kaimahi-seam-fixture",
                        "-addext", "subjectAltName=IP:127.0.0.1"],
                       check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.dir, ignore_errors=True)

    def setUp(self):
        Seam.reply = (200, envelope())
        self.base, stop = serve(tls=(str(self.cert), str(self.key)))
        self.addCleanup(stop)

    def test_the_mounted_authority_is_what_makes_the_call_succeed(self):
        result = client.ask("hello", environment(self.base, ca_file=str(self.cert)))
        self.assertEqual(result["status"], 200)
        self.assertEqual(result["text"], ANSWER)

    def test_without_that_authority_the_call_fails_instead_of_falling_back(self):
        result = client.ask("hello", environment(self.base))
        self.assertIsNone(result["status"])
        self.assertIn("certificate", result["error"].lower())

    def test_an_authority_file_that_is_not_there_is_refused_before_connecting(self):
        with self.assertRaises(client.Misconfigured):
            client.ask("hello", environment(self.base, ca_file=str(self.dir / "absent.crt")))


if __name__ == "__main__":
    unittest.main()
