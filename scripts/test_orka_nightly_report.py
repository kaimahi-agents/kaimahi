#!/usr/bin/env python3
"""Hermetic policy tests for the scheduled Orka health issue reporter."""
import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parent / "ci/orka-nightly-report.py"
REPO = "/repos/example/project"
RUN = REPO + "/actions/runs/123"
LATEST = REPO + "/actions/workflows/orka-nightly.yml/runs?event=schedule&branch=main&per_page=1"
ISSUES = REPO + "/issues?state=all&per_page=100&page="
TITLE = "Orka nightly qualification failing"
MARKER = "<!-- kmx-orka-nightly -->"
STATE = "<!-- kmx-orka-nightly-state: "
CANARY = "PRIVATE_PAYLOAD_CANARY"
ENV = {"GITHUB_REPOSITORY": "example/project", "GITHUB_RUN_ID": "123",
       "GITHUB_SHA": "a" * 40, "QUALIFICATION_RESULT": "failure", "GH_TOKEN": "test-token"}


def issue(number=7, state="open", body=None, **extra):
    return {"number": number, "state": state, "title": TITLE,
            "body": MARKER if body is None else body,
            "user": {"login": "github-actions[bot]", "type": "Bot"}, **extra}


def stored_body(first="2026-10-20T01:00:00Z", latest="2026-10-20T01:00:00Z", run_id="100"):
    return MARKER + "\n" + STATE + json.dumps({"first_failed_at": first,
            "latest_failed_at": latest, "run_id": run_id}) + " -->\n" + CANARY


class FakeAPI:
    """In-memory GitHub boundary; all policy and body building stay real."""
    def __init__(self, pages=None):
        self.run = {"id": 123, "event": "schedule", "head_branch": "main", "head_sha": "a" * 40,
                    "created_at": "2026-10-21T02:03:04Z", "display_title": CANARY,
                    "html_url": "https://evil.invalid/" + CANARY}
        self.latest_id = 123
        self.pages = pages or [[]]
        self.calls = []
        self.error = None

    @property
    def writes(self):
        return [call for call in self.calls if call[0] != "GET"]

    def request(self, method, path, data=None):
        self.calls.append((method, path, copy.deepcopy(data)))
        if self.error:
            raise self.error
        if method == "GET" and path == RUN:
            return self.run
        if method == "GET" and path == LATEST:
            return {"workflow_runs": [] if self.latest_id is None else [{"id": self.latest_id}]}
        if method == "GET" and path.startswith(ISSUES):
            page = int(path.removeprefix(ISSUES))
            return self.pages[page - 1] if page <= len(self.pages) else []
        if method == "POST" and path == REPO + "/issues":
            return issue(body=data["body"])
        if method == "PATCH" and path.startswith(REPO + "/issues/"):
            return {}
        raise AssertionError((method, path, data))


class ReporterTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.reporter = None
        if SCRIPT.exists():
            spec = importlib.util.spec_from_file_location("orka_nightly_report", SCRIPT)
            cls.reporter = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(cls.reporter)

    def setUp(self):
        self.assertIsNotNone(self.reporter, "nightly reporter must exist")
        self.network = patch("urllib.request.urlopen", side_effect=AssertionError("network forbidden"))
        self.network.start()
        self.addCleanup(self.network.stop)

    def execute(self, api=None, **env):
        api = api or FakeAPI()
        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            code = self.reporter.main({**ENV, **env}, api)
        return code, stdout.getvalue(), stderr.getvalue()

    def state(self, body):
        return json.loads(body.split(STATE, 1)[1].split(" -->", 1)[0])

    def test_new_failure_creates_assigned_owned_payload_free_issue(self):
        api = FakeAPI()
        self.assertEqual(self.execute(api), (0, "Orka nightly reporter: created.\n", ""))
        self.assertEqual(api.calls[:2], [("GET", RUN, None), ("GET", LATEST, None)])
        self.assertEqual(len(api.writes), 1)
        method, path, data = api.writes[0]
        self.assertEqual((method, path), ("POST", REPO + "/issues"))
        self.assertEqual(data["title"], TITLE)
        self.assertEqual(data["assignees"], ["gambtho"])
        self.assertIn(MARKER, data["body"])
        self.assertIn("https://github.com/example/project/actions/runs/123", data["body"])
        self.assertIn("a" * 40, data["body"])
        self.assertIn("runtime-qualification", data["body"])
        self.assertIn("Result: failure", data["body"])
        self.assertEqual(self.state(data["body"]), {"run_id": "123",
                         "first_failed_at": "2026-10-21T02:03:04Z",
                         "latest_failed_at": "2026-10-21T02:03:04Z"})
        self.assertNotIn(CANARY, json.dumps(data))
        self.assertLess(len(data["body"]), 4096)

    def test_failure_updates_owned_issue_and_preserves_first_failure(self):
        api = FakeAPI([[issue(body=stored_body())]])
        self.assertEqual(self.execute(api)[0], 0)
        self.assertEqual(len(api.writes), 1)
        method, path, data = api.writes[0]
        self.assertEqual((method, path), ("PATCH", REPO + "/issues/7"))
        self.assertEqual(data["state"], "open")
        self.assertNotIn("assignees", data, "updates must preserve maintainer-added assignees")
        self.assertEqual(self.state(data["body"])["first_failed_at"], "2026-10-20T01:00:00Z")
        self.assertEqual(self.state(data["body"])["latest_failed_at"], "2026-10-21T02:03:04Z")
        self.assertNotIn(CANARY, json.dumps(data))

    def test_closed_issue_is_reopened_instead_of_creating_another(self):
        api = FakeAPI([[issue(state="closed", body=stored_body())]])
        self.assertEqual(self.execute(api)[1], "Orka nightly reporter: reopened.\n")
        self.assertEqual(api.writes[0][0:2], ("PATCH", REPO + "/issues/7"))
        self.assertEqual(api.writes[0][2]["state"], "open")
        self.assertEqual(self.state(api.writes[0][2]["body"])["first_failed_at"], "2026-10-21T02:03:04Z")
        self.assertNotIn("assignees", api.writes[0][2])

    def test_every_non_success_result_is_reported_as_failure_health(self):
        for result in ("failure", "cancelled", "skipped", "unknown"):
            with self.subTest(result=result):
                api = FakeAPI()
                self.assertEqual(self.execute(api, QUALIFICATION_RESULT=result)[0], 0)
                self.assertEqual(len(api.writes), 1)
                self.assertIn("Result: " + result, api.writes[0][2]["body"])

    def test_success_closes_owned_issue_with_canonical_recovery_link(self):
        api = FakeAPI([[issue(body=stored_body())]])
        self.assertEqual(self.execute(api, QUALIFICATION_RESULT="success")[1],
                         "Orka nightly reporter: recovered.\n")
        self.assertEqual(len(api.writes), 1)
        method, path, data = api.writes[0]
        self.assertEqual((method, path, data["state"]), ("PATCH", REPO + "/issues/7", "closed"))
        self.assertIn("Recovery: https://github.com/example/project/actions/runs/123", data["body"])
        self.assertEqual(self.state(data["body"])["latest_failed_at"], "2026-10-20T01:00:00Z")
        self.assertNotIn(CANARY, json.dumps(data))

    def test_success_without_open_owned_issue_does_not_mutate(self):
        for pages in ([[]], [[issue(state="closed")]]):
            with self.subTest(pages=pages):
                api = FakeAPI(pages)
                self.assertEqual(self.execute(api, QUALIFICATION_RESULT="success")[0], 0)
                self.assertEqual(api.writes, [])

    def test_stale_success_cannot_close_latest_failure(self):
        for result in ("success", "failure"):
            with self.subTest(result=result):
                api = FakeAPI([[issue()]])
                api.latest_id = 124
                self.assertEqual(self.execute(api, QUALIFICATION_RESULT=result)[1],
                                 "Orka nightly reporter: ignored.\n")
                self.assertEqual(api.writes, [])
                self.assertFalse(any(call[1].startswith(ISSUES) for call in api.calls))

    def test_missing_latest_scheduled_run_does_not_mutate(self):
        api = FakeAPI()
        api.latest_id = None
        self.assertEqual(self.execute(api)[0], 0)
        self.assertEqual(api.writes, [])

    def test_branch_or_manual_run_does_not_modify_health(self):
        for field, value in (("head_branch", "feature"), ("event", "workflow_dispatch"),
                             ("event", "pull_request")):
            with self.subTest(field=field, value=value):
                api = FakeAPI([[issue()]])
                api.run[field] = value
                self.assertEqual(self.execute(api)[1], "Orka nightly reporter: ignored.\n")
                self.assertEqual(api.writes, [])
                self.assertEqual(len(api.calls), 1)

    def test_metadata_sha_mismatch_or_invalid_timestamp_refuses_writes(self):
        for field, value in (("head_sha", "b" * 40), ("created_at", CANARY),
                             ("created_at", "2026-02-30T02:03:04Z")):
            with self.subTest(field=field):
                api = FakeAPI()
                api.run[field] = value
                self.assertEqual(self.execute(api), (1, "", "Orka nightly reporter: failed.\n"))
                self.assertEqual(api.writes, [])

    def test_invalid_environment_refuses_before_requests_and_never_echoes(self):
        cases = {"GITHUB_REPOSITORY": ("", "../repo", "owner/repo/extra", "owner/repo?" + CANARY),
                 "GITHUB_RUN_ID": ("", "0", "-1", "123/" + CANARY, "１２３"),
                 "GITHUB_SHA": ("", "a" * 39, "g" * 40, CANARY),
                 "QUALIFICATION_RESULT": ("", "passed", "failure\n" + CANARY),
                 "GH_TOKEN": ("", " ")}
        for key, values in cases.items():
            for value in values:
                with self.subTest(key=key, value=value):
                    api = FakeAPI()
                    self.assertEqual(self.execute(api, **{key: value}),
                                     (1, "", "Orka nightly reporter: failed.\n"))
                    self.assertEqual(api.calls, [])

    def test_missing_environment_is_generic_failure(self):
        for key in ENV:
            with self.subTest(key=key):
                env = {**ENV}
                del env[key]
                api, output = FakeAPI(), io.StringIO()
                with contextlib.redirect_stderr(output):
                    self.assertEqual(self.reporter.main(env, api), 1)
                self.assertEqual(output.getvalue(), "Orka nightly reporter: failed.\n")
                self.assertEqual(api.calls, [])

    def test_api_errors_are_visible_without_response_payload(self):
        api = FakeAPI()
        api.error = RuntimeError(CANARY)
        self.assertEqual(self.execute(api), (1, "", "Orka nightly reporter: failed.\n"))
        self.assertEqual(api.writes, [])

    def test_only_marker_title_and_actions_bot_owned_non_pr_issue_matches(self):
        impostors = [issue(number=1, body="not owned"), issue(number=2, title="Other"),
                     issue(number=3, user={"login": "someone", "type": "User"}),
                     issue(number=4, pull_request={"url": "unused"}),
                     issue(number=5, user={"login": "github-actions[bot]", "type": "User"})]
        api = FakeAPI([impostors + [issue(number=8)]])
        self.assertEqual(self.execute(api)[0], 0)
        self.assertEqual(api.writes[0][1], REPO + "/issues/8")
        api = FakeAPI([impostors])
        self.assertEqual(self.execute(api)[0], 0)
        self.assertEqual(api.writes[0][0], "POST")

    def test_pagination_finds_closed_owned_issue_after_first_full_page(self):
        api = FakeAPI([[issue(number=i + 10, body="unowned") for i in range(100)],
                       [issue(number=222, state="closed")]])
        self.assertEqual(self.execute(api)[0], 0)
        self.assertIn(("GET", ISSUES + "2", None), api.calls)
        self.assertEqual(api.writes[0][1], REPO + "/issues/222")

    def test_repeated_run_failure_does_not_mutate(self):
        api = FakeAPI([[issue(body=stored_body(latest="2026-10-21T01:00:00Z", run_id="123"))]])
        self.assertEqual(self.execute(api)[1], "Orka nightly reporter: unchanged.\n")
        self.assertEqual(api.writes, [])

    def test_recorded_newer_failure_survives_stale_latest_run_response(self):
        for result in ("success", "failure"):
            for latest in ("2026-10-22T02:03:04Z", "2026-10-21T02:03:04Z"):
                with self.subTest(result=result, latest=latest):
                    # The run-list API can lag already-published issue metadata.
                    api = FakeAPI([[issue(body=stored_body(latest=latest, run_id="124"))]])
                    self.assertEqual(self.execute(api, QUALIFICATION_RESULT=result)[1], "Orka nightly reporter: ignored.\n")
                    self.assertEqual(api.writes, [])

    def test_newest_failure_same_day_updates_existing_issue_without_new_issue(self):
        api = FakeAPI([[issue(body=stored_body(latest="2026-10-21T01:00:00Z", run_id="122"))]])
        self.assertEqual(self.execute(api)[1], "Orka nightly reporter: updated.\n")
        self.assertEqual(api.writes[0][:2], ("PATCH", REPO + "/issues/7"))
        state = self.state(api.writes[0][2]["body"])
        self.assertEqual(state["run_id"], "123")
        self.assertEqual(state["latest_failed_at"], "2026-10-21T02:03:04Z")

    def test_closed_issue_reopens_even_when_same_day_was_already_reported(self):
        api = FakeAPI([[issue(state="closed", body=stored_body(latest="2026-10-21T01:00:00Z"))]])
        self.assertEqual(self.execute(api)[1], "Orka nightly reporter: reopened.\n")
        self.assertEqual(api.writes[0][2]["state"], "open")
        self.assertEqual(self.state(api.writes[0][2]["body"])["first_failed_at"], "2026-10-21T02:03:04Z")

    def test_arbitrary_or_oversized_existing_metadata_is_not_republished(self):
        bodies = [stored_body(first=CANARY), stored_body(first="2026-99-99T00:00:00Z"),
                  MARKER + "\n" + STATE + "not-json -->", stored_body() + CANARY * 1000,
                  MARKER + "\n" + STATE + "[] -->"]
        for body in bodies:
            with self.subTest(body=body[:100]):
                api = FakeAPI([[issue(body=body)]])
                self.assertEqual(self.execute(api)[0], 0)
                data = api.writes[0][2]
                self.assertNotIn(CANARY, json.dumps(data))
                self.assertLess(len(data["body"]), 4096)
                self.assertEqual(self.state(data["body"])["first_failed_at"], "2026-10-21T02:03:04Z")

    def test_real_api_transport_is_json_authenticated_and_has_twenty_second_timeout(self):
        response = io.BytesIO(b'{"number":7}')
        with patch.object(self.reporter.urllib.request, "urlopen", return_value=response) as opening:
            result = self.reporter.API("test-token").request("POST", REPO + "/issues", {"title": TITLE})
        self.assertEqual(result, {"number": 7})
        request = opening.call_args.args[0]
        self.assertEqual(request.full_url, "https://api.github.com" + REPO + "/issues")
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(json.loads(request.data), {"title": TITLE})
        self.assertEqual(request.get_header("Authorization"), "Bearer test-token")
        self.assertEqual(opening.call_args.kwargs, {"timeout": 20})

    def test_main_reads_documented_environment_and_uses_real_api_policy(self):
        api = FakeAPI()
        with patch.dict("os.environ", ENV, clear=True), patch.object(self.reporter, "API", return_value=api), \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(self.reporter.main(), 0)
        self.assertEqual(len(api.writes), 1)


if __name__ == "__main__":
    unittest.main()
