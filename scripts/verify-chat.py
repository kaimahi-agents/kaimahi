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

An unanswered question is the other way a chat ends without an answer: the
agent calls the runtime's built-in `ask_user` tool and the task ends
state == "input-required" with no artifacts. It fails here, and must keep
failing — nobody is present in CI to answer it. The identical state carries
a human APPROVAL decision on a real tool call, which also fails here and
must: a pending decision is not a completed answer either way. The report
names which of the two it was, because a red build should not need the raw
JSON to say what the agent was waiting for.

  verify-chat.py --selftest
Runs the built-in fixtures (the PR #24 shape, plus synthetic variations
of it) and exits non-zero if the verifier's verdicts drift; the hygiene
job runs it. Every fixture differs from the passing one in a single way,
so each half of the verdict is the only thing deciding some case — a
check that no fixture can fail is a check that can be deleted unnoticed.
The last cases run this file as a program, because a verdict that never
reaches an exit code is invisible to a fixture pair.
"""
import copy
import json
import os
import re
import subprocess
import sys
import tempfile


def pending_requests(d):
    """Names of the calls an input-required task is waiting on: `ask_user`
    (the agent asked the human a question) or a real tool (a human approval
    decision). Reads the confirmation wrapper in both of its shapes."""
    names = []
    for part in d.get("status", {}).get("message", {}).get("parts", []):
        data = part.get("data") or {}
        if data.get("name") != "adk_request_confirmation":
            continue
        args = data.get("args") or {}
        nested = (((args.get("toolConfirmation") or {}).get("payload") or {})
                  .get("hitl_parts") or [])
        originals = ([n.get("originalFunctionCall") or {} for n in nested]
                     or [args.get("originalFunctionCall") or {}])
        names += [o.get("name", "") for o in originals]
    return names


def verify(d, tool=None, needle=None):
    """Return (ok, report_lines) for one A2A task object."""
    lines = []
    state = d.get("status", {}).get("state")
    texts = [p.get("text", "") for a in d.get("artifacts", [])
             for p in a.get("parts", [])]
    reply = "\n".join(t for t in texts if t.strip())
    lines.append(f"state={state}\nreply:\n{reply}")
    if state == "input-required":
        waiting = [n for n in pending_requests(d) if n]
        lines.append("the agent stopped and is waiting for: "
                     + (", ".join(waiting) or "an undecodable request"))
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
# A second tool name, used only to record an exchange under the wrong name.
_OTHER_TOOL = "k8s_get_pods"
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


# The shape the e2e-tools shard went red on (2026-09-07): asked who it was
# and where it was running, the agent answered half, called the runtime's
# built-in `ask_user` with "Where are you?" and stopped. No artifacts, so the
# reply is empty; the state means it is waiting for a human.
_ASK_USER_FIXTURE = {
    "status": {"state": "input-required", "message": {"role": "agent", "parts": [
        {"kind": "data", "metadata": {"kagent_type": "function_call",
                                      "kagent_is_long_running": True},
         "data": {"id": "adk-1", "name": "adk_request_confirmation",
                  "args": {"originalFunctionCall": {
                      "id": "call_6bgth59d", "name": "ask_user",
                      "args": {"questions": [{"question": "Where are you?"}]}},
                      "toolConfirmation": {"confirmed": False,
                                           "hint": "Where are you?"}}}}]}},
    "history": [
        {"role": "user", "parts": [{"kind": "text",
                                    "text": "Hello! Who are you and where are you running?"}]},
        {"role": "agent", "parts": [
            {"kind": "text", "text": "I am the \"hello_world\" agent. Here's who I am:\n"},
            {"kind": "data", "metadata": {"kagent_type": "function_call"},
             "data": {"id": "call_6bgth59d", "name": "ask_user",
                      "args": {"questions": [{"question": "Where are you?"}]}}}]},
    ],
}

# The SAME state carrying a human approval decision on a real tool call —
# what the interactive chat exists to answer. It must fail here too: a
# pending decision is not an answer, and nothing may let one pass as one.
_APPROVAL_FIXTURE = {
    "status": {"state": "input-required", "message": {"role": "agent", "parts": [
        {"kind": "data", "metadata": {"kagent_type": "function_call",
                                      "kagent_is_long_running": True},
         "data": {"id": "adk-1", "name": "adk_request_confirmation",
                  "args": {"originalFunctionCall": {
                      "id": "call-1", "name": "delete_pod",
                      "args": {"name": "pod-a"}}}}}]}},
    "history": [],
}

# The same two tasks in the other wrapper shape the runtime uses: the
# original call is buried under toolConfirmation.payload.hitl_parts instead
# of sitting at the top of args. Both fixtures are synthetic — written to
# match the nested shape the Go chat client parses, since the captured runs
# to hand all used the flat one. A verifier that only understood the flat
# shape would still fail these tasks, but for the wrong reason: it would
# report an undecodable request instead of naming what the agent waited on,
# and a red build would need the raw JSON to find out which it was.
_NESTED_ASK_USER_FIXTURE = {
    "status": {"state": "input-required", "message": {"role": "agent", "parts": [
        {"kind": "data", "metadata": {"kagent_type": "function_call",
                                      "kagent_is_long_running": True},
         "data": {"id": "adk-2", "name": "adk_request_confirmation",
                  "args": {"toolConfirmation": {"payload": {"hitl_parts": [
                      {"originalFunctionCall": {
                          "id": "call-nested-1", "name": "ask_user",
                          "args": {"questions": [
                              {"question": "Where are you?"}]}}}]}}}}}]}},
    "history": [],
}

_NESTED_APPROVAL_FIXTURE = {
    "status": {"state": "input-required", "message": {"role": "agent", "parts": [
        {"kind": "data", "metadata": {"kagent_type": "function_call",
                                      "kagent_is_long_running": True},
         "data": {"id": "adk-2", "name": "adk_request_confirmation",
                  "args": {"toolConfirmation": {"payload": {"hitl_parts": [
                      {"originalFunctionCall": {
                          "id": "call-nested-2", "name": "delete_pod",
                          "args": {"name": "pod-a"}}}]}}}}}]}},
    "history": [],
}


def _script(args):
    """Run this file as a program, the way CI runs it, and return the
    completed process. Calling verify() directly would leave the arguments,
    the JSON extraction and the exit code untested — a removed entry point
    would then be invisible to the self-test."""
    return subprocess.run([sys.executable, os.path.abspath(__file__)] + args,
                          capture_output=True, text=True)


def _end_to_end():
    """The cases that only exist once the script is given a file: the
    argument guards, finding the task in a log, and the verdict actually
    reaching an exit code. Returns the number that came out wrong."""
    failed = 0
    with tempfile.TemporaryDirectory() as d:
        good = os.path.join(d, "chat.out")
        with open(good, "w") as f:
            # A real capture has the command's own chatter around the task.
            f.write("connecting to the agent...\n"
                    + json.dumps(_FIXTURE) + "\nbye\n")
        failing = os.path.join(d, "errored.out")
        errored = copy.deepcopy(_FIXTURE)
        errored["history"][1]["parts"][0]["data"]["response"]["isError"] = True
        with open(failing, "w") as f:
            f.write(json.dumps(errored) + "\n")
        bad = os.path.join(d, "prose.out")
        with open(bad, "w") as f:
            f.write("the agent said hello and nothing else\n")
        checks = [
            ("a task in a captured log -> exit 0",
             [good, "k8s_get_resources", _PROBE], 0),
            # A rejected task has to reach the exit code, not only the
            # report: a verdict printed and then discarded is a green build.
            ("a rejected task -> exit 1",
             [failing, "k8s_get_resources", _PROBE], 1),
            ("an empty TOOL argument is refused",
             [good, "", _PROBE], 1),
            ("an empty SUBSTRING argument is refused",
             [good, "k8s_get_resources", ""], 1),
            ("output with no task object in it is refused", [bad], 1),
            # Called with nothing at all. 2 means "called wrong", which is
            # a different fact from "the chat did not pass" and the release
            # job's own convention — a traceback would report neither.
            ("no arguments at all is a usage error, not a verdict", [], 2),
        ]
        for name, args, want in checks:
            got = _script(args).returncode
            mark = "ok " if got == want else "BAD"
            print(f"{mark} {name}: exit={got}")
            failed += got != want
    return failed


def selftest():
    cases = [("probe in payload, garbled in prose -> PASS", _FIXTURE, True)]
    no_resp = copy.deepcopy(_FIXTURE)
    no_resp["history"] = no_resp["history"][:1]
    cases.append(("no function_response -> FAIL", no_resp, False))
    # The other half of the tool path: a successful response with no call
    # recorded before it. Without this case the call count could be dropped
    # from the verdict and every fixture would still come out the same way.
    no_call = copy.deepcopy(_FIXTURE)
    no_call["history"] = no_call["history"][1:]
    cases.append(("no function_call -> FAIL", no_call, False))
    # A real exchange, recorded under a different tool's name than the one
    # the run was asked about — the whole point of the tool path is that the
    # named tool was invoked, so a call to some other tool is not an answer.
    # The name is matched twice, on the call and on the response, and each
    # match can be lost on its own, so each gets a case.
    other_call = copy.deepcopy(_FIXTURE)
    other_call["history"][0]["parts"][0]["data"]["name"] = _OTHER_TOOL
    cases.append(("the call is under another tool's name -> FAIL",
                  other_call, False))
    other_resp = copy.deepcopy(_FIXTURE)
    other_resp["history"][1]["parts"][0]["data"]["name"] = _OTHER_TOOL
    cases.append(("the response is under another tool's name -> FAIL",
                  other_resp, False))
    # Half finished: prose was produced and the task never reached
    # completed. Every other unfinished shape here also has an empty reply,
    # so without this one the state and the reply cannot be told apart and
    # the state could stop being checked without a fixture noticing.
    half_done = copy.deepcopy(_FIXTURE)
    half_done["status"]["state"] = "working"
    cases.append(("a non-completed state with a real reply -> FAIL",
                  half_done, False))
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
    tool_cases = [(name, task, want, "k8s_get_resources", _PROBE)
                  for name, task, want in cases]
    # Checked with a TOOL but no SUBSTRING — the form with no payload probe
    # to fall back on, where the count of successful responses is the only
    # thing left asserting that the tool answered at all.
    tool_cases.append(("no substring given, real exchange -> PASS",
                       _FIXTURE, True, "k8s_get_resources", None))
    tool_cases.append(("no substring given, no function_response -> FAIL",
                       no_resp, False, "k8s_get_resources", None))
    # Checked with no TOOL argument, so the verdict can only come from the
    # state and the reply — the plain `make chat` steps' form.
    tool_cases.append(("agent asked the user a question -> FAIL",
                       _ASK_USER_FIXTURE, False, None, None))
    tool_cases.append(("human approval pending on a real tool -> FAIL",
                       _APPROVAL_FIXTURE, False, None, None))
    tool_cases.append(("agent asked the user a question, nested -> FAIL",
                       _NESTED_ASK_USER_FIXTURE, False, None, None))
    tool_cases.append(("human approval pending, nested -> FAIL",
                       _NESTED_APPROVAL_FIXTURE, False, None, None))
    failed = False
    for name, task, want, tool, needle in tool_cases:
        got, _ = verify(task, tool, needle)
        mark = "ok " if got == want else "BAD"
        print(f"{mark} {name}: verdict={'PASS' if got else 'FAIL'}")
        failed |= got != want
    # The report has to distinguish the two input-required cases, or a red
    # build cannot tell "the model asked a question" from "a human owes a
    # decision" without reading the raw task.
    for shape, task, want in (("flat", _ASK_USER_FIXTURE, "ask_user"),
                              ("flat", _APPROVAL_FIXTURE, "delete_pod"),
                              ("nested", _NESTED_ASK_USER_FIXTURE, "ask_user"),
                              ("nested", _NESTED_APPROVAL_FIXTURE, "delete_pod")):
        _, lines = verify(task)
        said = any(f"waiting for: {want}" in line for line in lines)
        print(f"{'ok ' if said else 'BAD'} input-required names what it waits "
              f"for ({shape} wrapper): {want}")
        failed |= not said
    failed = bool(failed) or _end_to_end() > 0
    if failed:
        print("verify-chat self-test: a verdict drifted", file=sys.stderr)
        return 1
    print(f"verify-chat self-test: {len(tool_cases)} task verdicts, "
          "both confirmation shapes named, and the script run end to end")
    return 0


def check_file(argv):
    """The FILE [TOOL [SUBSTRING]] form, as an exit code."""
    # Called with nothing at all, say so rather than raising IndexError at
    # the reader: a traceback is not a verdict, and a CI step that reads
    # this script's exit code deserves the usage line instead.
    if not argv:
        print(__doc__.strip().splitlines()[0], file=sys.stderr)
        print("usage: verify-chat.py FILE [TOOL [SUBSTRING]] | --selftest", file=sys.stderr)
        return 2
    raw = open(argv[0]).read()
    tool = argv[1] if len(argv) > 1 else None
    needle = argv[2] if len(argv) > 2 else None
    # An empty arg (e.g. a $var that failed to expand) must not silently
    # skip the check it was meant to enable.
    if tool == "" or needle == "":
        print("empty TOOL/SUBSTRING argument — refusing to skip a check",
              file=sys.stderr)
        return 1
    m = re.search(r"^\{.*\}$", raw, re.M | re.S)
    if not m:
        print("no JSON task object found in chat output", file=sys.stderr)
        return 1
    ok, lines = verify(json.loads(m.group(0)), tool, needle)
    print("\n".join(lines))
    return 0 if ok else 1


if __name__ == "__main__":
    if sys.argv[1:] == ["--selftest"]:
        sys.exit(selftest())
    sys.exit(check_file(sys.argv[1:]))
