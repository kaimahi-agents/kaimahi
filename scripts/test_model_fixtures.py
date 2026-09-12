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
from http.server import HTTPServer

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


if __name__ == "__main__":
    unittest.main()
