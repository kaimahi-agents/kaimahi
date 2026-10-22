#!/usr/bin/env python3
"""Maintain one payload-free health issue for the latest scheduled main run."""
from datetime import datetime
import json
import os
import re
import sys
import urllib.request

TITLE = "Orka nightly qualification failing"
MARKER = "<!-- kmx-orka-nightly -->"
STATE = "<!-- kmx-orka-nightly-state: "
BODY_LIMIT = 4096


class API:
    def __init__(self, token):
        self.token = token

    def request(self, method, path, data=None):
        request = urllib.request.Request(
            "https://api.github.com" + path,
            data=None if data is None else json.dumps(data).encode("utf-8"),
            method=method,
            headers={"Authorization": "Bearer " + self.token,
                     "Accept": "application/vnd.github+json",
                     "Content-Type": "application/json",
                     "X-GitHub-Api-Version": "2022-11-28"})
        with urllib.request.urlopen(request, timeout=20) as response:
            return json.load(response)


def timestamp(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{4}(-[0-9]{2}){2}T[0-9]{2}(:[0-9]{2}){2}Z", value):
        raise ValueError("invalid timestamp")
    datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ")
    return value


def stored_state(body):
    # Retain only typed metadata, never copy existing issue prose into a write.
    if not body or len(body) > BODY_LIMIT:
        return {}
    try:
        state = json.loads(body.split(STATE, 1)[1].split(" -->", 1)[0])
        first = timestamp(state["first_failed_at"])
        latest = timestamp(state["latest_failed_at"])
        run_id = state["run_id"]
        if first > latest or not re.fullmatch(r"[1-9][0-9]*", run_id):
            return {}
        return {"first_failed_at": first, "latest_failed_at": latest, "run_id": run_id}
    except (ValueError, KeyError, IndexError, TypeError):
        return {}


def owned_issue(api, base):
    page = 1
    while True:
        issues = api.request("GET", f"{base}/issues?state=all&per_page=100&page={page}")
        for issue in issues:
            if ("pull_request" not in issue and issue["title"] == TITLE
                    and MARKER in (issue["body"] or "")
                    and issue["user"]["login"] == "github-actions[bot]"
                    and issue["user"]["type"] == "Bot"):
                return issue
        if len(issues) < 100:
            return None
        page += 1


def report(api, repository, run_id, sha, result):
    base = f"/repos/{repository}"
    run = api.request("GET", f"{base}/actions/runs/{run_id}")
    if run["event"] != "schedule" or run["head_branch"] != "main":
        return "ignored"
    if run["head_sha"] != sha:
        raise ValueError("run SHA mismatch")
    when = timestamp(run["created_at"])
    latest = api.request("GET", f"{base}/actions/workflows/orka-nightly.yml/runs"
                         "?event=schedule&branch=main&per_page=1")["workflow_runs"]
    if not latest or str(latest[0]["id"]) != run_id:
        return "ignored"
    issue = owned_issue(api, base)
    if result == "success" and (issue is None or issue["state"] == "closed"):
        return "unchanged"
    state = stored_state(issue["body"]) if issue else {}
    # Preserve an already-published newer failure even if the run list lags.
    if state and state["run_id"] != run_id and state["latest_failed_at"] >= when:
        return "ignored"
    if result != "success":
        # Repeated attempts deduplicate; a newer failure still updates this issue.
        if issue and issue["state"] == "open" and state and state["run_id"] == run_id:
            return "unchanged"
        first = state.get("first_failed_at", when) if issue and issue["state"] == "open" else when
        state = {"run_id": run_id, "first_failed_at": first, "latest_failed_at": when}
    url = f"https://github.com/{repository}/actions/runs/{run_id}"
    lines = [MARKER, "Phase: runtime-qualification", f"Result: {result}", f"Run: {url}", f"SHA: {sha}"]
    if state:
        lines += [STATE + json.dumps(state, sort_keys=True) + " -->",
                  "First failure: " + state["first_failed_at"],
                  "Latest failure: " + state["latest_failed_at"]]
    if result == "success":
        lines.append("Recovery: " + url)
    body = "\n\n".join(lines) + "\n"
    if len(body) > BODY_LIMIT:
        raise ValueError("body exceeds limit")
    data = {"title": TITLE, "body": body}
    if issue is None:
        data["assignees"] = ["gambtho"]
        api.request("POST", f"{base}/issues", data)
        return "created"
    data["state"] = "closed" if result == "success" else "open"
    api.request("PATCH", f"{base}/issues/{issue['number']}", data)
    return "recovered" if result == "success" else "reopened" if issue["state"] == "closed" else "updated"


def main(env=None, api=None):
    try:
        env = os.environ if env is None else env
        repository, run_id, sha, result, token = (env[name] for name in (
            "GITHUB_REPOSITORY", "GITHUB_RUN_ID", "GITHUB_SHA", "QUALIFICATION_RESULT", "GH_TOKEN"))
        if (not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}", repository)
                or repository.split("/")[1] in (".", "..")
                or not re.fullmatch(r"[1-9][0-9]*", run_id)
                or not re.fullmatch(r"[0-9a-fA-F]{40}", sha)
                or result not in ("success", "failure", "cancelled", "skipped", "unknown")
                or not token.strip()):
            raise ValueError("invalid environment")
        outcome = report(API(token) if api is None else api, repository, run_id, sha.lower(), result)
        print(f"Orka nightly reporter: {outcome}.")
        return 0
    except Exception:
        # Exceptions may contain tokens, HTTP response bodies, or private paths.
        print("Orka nightly reporter: failed.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
