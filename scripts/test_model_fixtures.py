#!/usr/bin/env python3
"""Exercise the actual model fixture: usage, silent responses, and redirects."""
import importlib.util
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

SOURCE = pathlib.Path(__file__).parent / "ci" / "plain-model-server.py"
spec = importlib.util.spec_from_file_location("model_fixture", SOURCE)
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class ModelFixtureTests(unittest.TestCase):
    def setUp(self):
        self.server = HTTPServer(("127.0.0.1", 0), fixture.Handler)
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()
        self.addCleanup(self.stop)

    def stop(self):
        self.server.shutdown()
        self.server.server_close()
        self.worker.join()

    def request(self, path):
        return urllib.request.Request(
            f"http://127.0.0.1:{self.server.server_port}{path}",
            data=json.dumps({"model": "fixture", "input": "one two three four"}).encode(),
            headers={"Content-Type": "application/json"},
        )

    def test_reported_usage_matches_response_and_request(self):
        with urllib.request.urlopen(self.request("/v1/responses"), timeout=3) as response:
            body = json.load(response)
        self.assertEqual(body["usage"], {"input_tokens": 4, "output_tokens": 6, "total_tokens": 10})
        self.assertEqual(body["output"][0]["content"][0]["text"], "a governed answer from a fixture")

    def test_silent_response_does_not_invent_usage(self):
        with urllib.request.urlopen(self.request("/v1/silent"), timeout=3) as response:
            body = json.load(response)
        self.assertNotIn("usage", body)
        self.assertEqual(body["status"], "completed")

    def test_hosted_redirect_is_a_real_307_not_a_success(self):
        with self.assertRaises(urllib.error.HTTPError) as caught:
            urllib.request.build_opener(NoRedirect).open(self.request("/redirect"), timeout=3)
        self.assertEqual(caught.exception.code, 307)
        self.assertEqual(caught.exception.headers["Location"], "https://example.invalid/refused")
        caught.exception.close()


class UpgradeProbeCustodyTests(unittest.TestCase):
    def test_upgrade_probe_keeps_bearers_out_of_curl_argv(self):
        source = SOURCE.parent.parent / "plane-upgrade-probe.sh"
        text = source.read_text()
        helpers = text[text.index("admin() {"):text.index("\nseed() {")]
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            curl = root / "curl"
            curl.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, stat, sys
args = sys.argv[1:]
for token in ("probe-admin-fixture-token", "probe-governed-fixture-token"):
    if any(token in arg for arg in args):
        sys.exit("bearer reached curl argv")
headers = [args[i + 1] for i, arg in enumerate(args[:-1]) if arg == "-H" and args[i + 1].startswith("@")]
assert len(headers) == 1, "no private authorization header file"
header = pathlib.Path(headers[0][1:])
assert stat.S_IMODE(header.stat().st_mode) == 0o600, "header file is not private"
with open(os.environ["CURL_LOG"], "a") as log:
    log.write(json.dumps({"args": args, "header": header.read_text()}) + "\\n")
if "-o" in args:
    pathlib.Path(args[args.index("-o") + 1]).write_text("fixture response")
if "-w" in args:
    print("200", end="")
''')
            curl.chmod(0o700)
            env = {**os.environ, "PATH": tmp + os.pathsep + os.environ["PATH"],
                   "CURL_LOG": str(root / "calls.jsonl")}
            script = '''set -euo pipefail
umask 077
work="$1"
admin_token=probe-admin-fixture-token
admin_port=19191
data_port=18180
fail() { printf '%s\\n' "$*" >&2; exit 1; }
'''+ helpers + '''
admin GET /admin/version
admin POST /admin/requests '{"kind":"budget"}'
seam_scheme=http
governed_call probe-governed-fixture-token
seam_scheme=https
governed_call probe-governed-fixture-token
'''
            result = subprocess.run(["bash", "-c", script, "probe", tmp],
                                    env=env, text=True, capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
            self.assertEqual([c["header"] for c in calls], [
                "Authorization: Bearer probe-admin-fixture-token\n",
                "Authorization: Bearer probe-admin-fixture-token\n",
                "Authorization: Bearer probe-governed-fixture-token\n",
                "Authorization: Bearer probe-governed-fixture-token\n",
            ])
            self.assertNotIn("--cacert", calls[2]["args"])
            self.assertIn("--cacert", calls[3]["args"])

    def test_upgrade_probe_rejects_unexpected_status_and_accepts_expected_denial(self):
        # Exercise real curl: leaving -f in place rejects the expected 429;
        # dropping it without checking the status accepts an unexpected 200/503.
        class StatusHandler(BaseHTTPRequestHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers.get("content-length", 0)))
                body = b"fixture status response"
                self.send_response(self.server.response_status)
                self.send_header("content-length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_):
                pass

        text = (SOURCE.parent.parent / "plane-upgrade-probe.sh").read_text()
        helpers = text[text.index("admin() {"):text.index("\nseed() {")]
        server = HTTPServer(("127.0.0.1", 0), StatusHandler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                script = '''set -euo pipefail
umask 077
work="$1"
data_port="$2"
seam_scheme=http
fail() { printf '%s\\n' "$*" >&2; exit 1; }
''' + helpers + '\ngoverned_call fixture-token ${3:+"$3"}\n'
                for status, expected, passes in ((200, "", True), (429, "", False),
                                                 (429, "429", True), (200, "429", False),
                                                 (503, "429", False)):
                    with self.subTest(status=status, expected=expected or "200"):
                        server.response_status = status
                        result = subprocess.run(
                            ["bash", "-c", script, "probe", tmp, str(server.server_port), expected],
                            text=True, capture_output=True, timeout=5,
                        )
                        self.assertEqual(result.returncode == 0, passes, result.stderr)
                        if passes:
                            self.assertIn("fixture status response", result.stdout)
        finally:
            server.shutdown()
            server.server_close()
            worker.join()

    def test_upgrade_probe_refuses_nonisolated_database_names_before_psql(self):
        source = SOURCE.parent.parent / "plane-upgrade-probe.sh"
        cases = (
            ("kaimahi", "kaimahi_upgrade_broken", "kaimahi", False),
            ("kaimahi_upgrade_probe", "production", "kaimahi", False),
            ("kaimahi_upgrade_probe", "kaimahi_upgrade_probe", "kaimahi", False),
            ("kaimahi_upgrade_probe", "kaimahi_upgrade_broken", "kaimahi_upgrade_probe", False),
            ("kaimahi_upgrade_probe;drop database kaimahi", "kaimahi_upgrade_broken", "kaimahi", False),
            ("kaimahi_upgrade_probe", "kaimahi_upgrade_broken", "kaimahi", True),
        )
        for upgrade, broken, maintenance, allowed in cases:
            with self.subTest(upgrade=upgrade, broken=broken, maintenance=maintenance):
                with tempfile.TemporaryDirectory() as tmp:
                    root = pathlib.Path(tmp)
                    psql = root / "psql"
                    psql.write_text(f"#!{sys.executable}\n" +
                                    'import os, pathlib, sys\n'
                                    'pathlib.Path(os.environ["PSQL_CALLED"]).touch()\n'
                                    'sys.exit(99)\n')
                    psql.chmod(0o700)
                    env = {**os.environ, "PATH": tmp + os.pathsep + os.environ["PATH"],
                           "UPGRADE_DB": upgrade, "BROKEN_DB": broken, "PGDATABASE": maintenance,
                           "PSQL_CALLED": str(root / "called"), "KEEP_WORKDIR": ""}
                    result = subprocess.run(["bash", str(source)], env=env,
                                            text=True, capture_output=True, timeout=5)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual((root / "called").exists(), allowed, result.stderr)
                    self.assertNotIn("unbound variable", result.stderr)
                    self.assertNotIn("command not found", result.stderr)


if __name__ == "__main__":
    unittest.main()
