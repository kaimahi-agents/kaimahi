#!/usr/bin/env python3
"""Fail-closed check on `make chat` output: require an A2A task with
status.state == "completed" and a non-empty reply. Reads the captured chat
output from the file named in argv[1].

Optional positional args cover the tool path:
  verify-chat.py FILE [TOOL [SUBSTRING]]
With TOOL, the task history must additionally contain a function_call for
that tool name AND a successful (isError == false) function_response for it
— a plausible-sounding reply without a real MCP invocation fails. With
SUBSTRING, that successful function_response's payload must contain it (CI
passes an unguessable probe ConfigMap name, so the payload can only have
come from a live cluster round-trip). The model's prose is printed but not
asserted on: a 3B model garbles unguessable strings when relaying them
(CI flake class 2 — PR #24 saw `probe-46649d55` in the tool payload and
`probe-466448a247` in the reply), and requiring a verbatim copy tests the
model, not the tool path.

  verify-chat.py --selftest
Runs the built-in fixtures (the PR #24 shape) and exits non-zero if the
verifier's verdicts drift; the hygiene job runs it.
"""
import copy
import json
import re
import sys


def verify(d, tool=None, needle=None):
    """Return (ok, report_lines) for one A2A task object."""
    lines = []
    state = d.get("status", {}).get("state")
    texts = [p.get("text", "") for a in d.get("artifacts", [])
             for p in a.get("parts", [])]
    reply = "\n".join(t for t in texts if t.strip())
    lines.append(f"state={state}\nreply:\n{reply}")
    ok = state == "completed" and bool(reply)

    if tool:
        calls = responses = payload_hits = 0
        for msg in d.get("history", []):
            for p in msg.get("parts", []):
                if p.get("kind") != "data":
                    continue
                data = p.get("data", {})
                kind = (p.get("metadata") or {}).get("kagent_type")
                if kind == "function_call" and data.get("name") == tool:
                    calls += 1
                if (kind == "function_response" and data.get("name") == tool
                        and not data.get("response", {}).get("isError", True)):
                    responses += 1
                    # The payload is the proof of a live round-trip; search
                    # its full JSON form so the check does not depend on
                    # the MCP content shape (text vs structured).
                    if needle and needle in json.dumps(data.get("response")):
                        payload_hits += 1
        lines.append(f"tool={tool} function_calls={calls} "
                     f"ok_responses={responses}")
        ok = ok and calls > 0 and responses > 0
        if needle:
            lines.append(f"expect={needle!r} in_function_response="
                         f"{payload_hits > 0} in_reply={needle in reply} "
                         "(reply is informational)")
            ok = ok and payload_hits > 0
    return ok, lines


# The fields the verifier reads, in the shape kagent returned for PR #24
# attempt 2 (run 33562345538): the tool payload carries the real probe
# name, the prose relays a garbled one.
_PROBE = "probe-46649d55"
_FIXTURE = {
    "status": {"state": "completed"},
    "artifacts": [{"parts": [{"kind": "text",
                              "text": "kube-root-ca.crt  \nprobe-466448a247"}]}],
    "history": [
        {"parts": [{"kind": "data", "metadata": {"kagent_type": "function_call"},
                    "data": {"id": "1", "name": "k8s_get_resources",
                             "args": {"resource_type": "configmap"}}}]},
        {"parts": [{"kind": "data",
                    "metadata": {"kagent_type": "function_response"},
                    "data": {"id": "1", "name": "k8s_get_resources",
                             "response": {"isError": False, "content": [
                                 {"type": "text", "text":
                                  "NAME               DATA   AGE\n"
                                  "kube-root-ca.crt   1      4m6s\n"
                                  f"{_PROBE}     1      35s\n"}]}}}]},
    ],
}


def selftest():
    cases = [("probe in payload, garbled in prose -> PASS", _FIXTURE, True)]
    no_resp = copy.deepcopy(_FIXTURE)
    no_resp["history"] = no_resp["history"][:1]
    cases.append(("no function_response -> FAIL", no_resp, False))
    wrong = copy.deepcopy(_FIXTURE)
    wrong["history"][1]["parts"][0]["data"]["response"]["content"][0]["text"] = \
        "NAME               DATA   AGE\nkube-root-ca.crt   1      4m6s\n"
    cases.append(("function_response lacks the probe -> FAIL", wrong, False))
    errored = copy.deepcopy(_FIXTURE)
    errored["history"][1]["parts"][0]["data"]["response"]["isError"] = True
    cases.append(("function_response isError:true -> FAIL", errored, False))
    empty = copy.deepcopy(_FIXTURE)
    empty["artifacts"] = []
    cases.append(("empty reply -> FAIL", empty, False))
    failed = False
    for name, task, want in cases:
        got, _ = verify(task, "k8s_get_resources", _PROBE)
        mark = "ok " if got == want else "BAD"
        print(f"{mark} {name}: verdict={'PASS' if got else 'FAIL'}")
        failed |= got != want
    return 1 if failed else 0


if __name__ == "__main__":
    if sys.argv[1:] == ["--selftest"]:
        sys.exit(selftest())
    raw = open(sys.argv[1]).read()
    tool = sys.argv[2] if len(sys.argv) > 2 else None
    needle = sys.argv[3] if len(sys.argv) > 3 else None
    # An empty arg (e.g. a $var that failed to expand) must not silently
    # skip the check it was meant to enable.
    if tool == "" or needle == "":
        sys.exit("empty TOOL/SUBSTRING argument — refusing to skip a check")
    m = re.search(r"^\{.*\}$", raw, re.M | re.S)
    if not m:
        sys.exit("no JSON task object found in chat output")
    ok, lines = verify(json.loads(m.group(0)), tool, needle)
    print("\n".join(lines))
    sys.exit(0 if ok else 1)
