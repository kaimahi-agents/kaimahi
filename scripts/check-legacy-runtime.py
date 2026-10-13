#!/usr/bin/env python3
"""Reject retired command instructions and exact retired assets, not kagent names.

Runtime/version validation belongs to kmx, not this scanner. Native API/image
identifiers, create/lift/status code and external chart bootstrap need no source
permissions. Historical releases, conventional tests/testdata and source comments
are inert records. Only exact path/text/reason exceptions may waive current
retirement notices, refusal strings and inert CI controls; unused entries fail.

Command matching uses bounded shell tokenization, literal source argv and at most
four physical continuation lines. It is not a shell, Go or prose parser: dynamic
argv, aliases and arbitrary source composition remain command-test territory.
Run this file normally for the tracked tree, or with --selftest for regressions.
"""
from __future__ import annotations

import contextlib
import io
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
from unittest import mock

ROOT = pathlib.Path(os.environ.get("KMX_MUTATION_ROOT", pathlib.Path(__file__).resolve().parents[1]))
ALLOWLIST = "scripts/legacy-runtime-allowlist.json"
SELF = {"scripts/check-legacy-runtime.py", ALLOWLIST}
SKIP_SUFFIX = {".png", ".jpg", ".jpeg", ".gif", ".pdf", ".ico", ".woff", ".woff2"}
TEXT_SUFFIX = {".md", ".go", ".py", ".sh", ".yaml", ".yml", ".json", ".svg", ".txt", ".mod", ".sum"}
MIN_TRACKED, MIN_READ = 200, 150
MAX_LINES = 4
VALUE_FLAGS = {"--context", "-context", "--container-engine", "-container-engine", "--namespace", "-n"}
LOCAL_STEPS = {"kagent", "agent", "tools-agent"}
CLOUD_STEPS = {"kagent", "agents", "boundary", "credential", "plane"}
RETIRED_PATHS = {
    "k8s/kagent-values.yaml", "k8s/hello-world.yaml", "k8s/tools-agent.yaml",
    "k8s/models/ollama.yaml", "k8s/models/governed-ollama.yaml", "k8s/models/governed-copilot.yaml",
    "internal/kmx/app/kagent_namespace.go", "internal/kmx/app/nokagent_test.go",
    "internal/kmx/app/quickstart_kagent_test.go", "internal/kmx/app/runtime_kagent.go",
    "internal/kmx/app/runtime_kagent_test.go", ".github/workflows/kagent-shim-spike.yml",
    "internal/kmx/app/approvals.go", "scripts/ap-await-approval.sh",
    "scripts/await-approval.sh", "scripts/test_ap_approval.py",
}
RETIRED_DIRS = ("k8s/models/", "internal/kmx/kagentcli/", "internal/kmx/kagentcompat/", "spikes/kagent-shim/")
# The basename remains a retired checkout values file, not an official chart.
ASSET = re.compile(r"(?<![\w./-])(?:\./)?(?:" + "|".join(re.escape(p) for p in sorted(RETIRED_PATHS))
                   + r"|kagent-values\.yaml)(?![\w./-])")
SOURCE_ARGV = re.compile(r"\[\]string\s*\{([^{}]*)\}")
SOURCE_CALL = re.compile(r"\b(operationCommand|exec\.Command|retiredCommand)\s*\(([^()]*?)\)")
GOVERN_ROUTE = re.compile(r'\bUse\s*:\s*"govern(?:\s|"|$)')
STRING = re.compile(r'"(?:[^"\\]|\\.)*"|\x27[^\x27]*\x27|`[^`]*`')
EXECUTABLE = re.compile(r"(?:[\w.$/{}/-]+/)?(?:kmx|kagent|make)$")


def inert(path):
    p = pathlib.PurePosixPath(path)
    return ("testdata" in p.parts or "tests" in p.parts or p.name.endswith("_test.go")
            or p.name.startswith("test_") and p.suffix == ".py"
            or p.name.endswith(("_test.py", "-test.py", "-test.sh"))
            or path.startswith("docs/reviews/"))


def load_allowlist(path):
    entries = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(entries, list):
        raise ValueError("exceptions must be a list (an empty list is valid)")
    seen = set()
    for entry in entries:
        if (not isinstance(entry, dict) or set(entry) != {"path", "text", "reason"}
                or any(not isinstance(v, str) or not v.strip() for v in entry.values())):
            raise ValueError("each exception needs nonempty string path, text and reason fields only")
        p, text = entry["path"], entry["text"]
        if (pathlib.PurePosixPath(p).is_absolute() or ".." in pathlib.PurePosixPath(p).parts
                or str(pathlib.PurePosixPath(p)) != p or "\n" in text or "\r" in text
                or text != text.strip() or p in SELF or inert(p)):
            raise ValueError(f"invalid exact exception path/text: {p!r}")
        key = p, text
        if key in seen:
            raise ValueError(f"duplicate exact exception: {p}")
        seen.add(key)
    return entries


def tracked(root):
    # Mutation mirrors read their own scripts but enumerate the real Git tree.
    candidates = [root]
    for name in ("docs", "internal", "cmd"):
        if (root / name).is_symlink():
            candidates.append((root / name).resolve().parent)
    for candidate in candidates:
        got = subprocess.run(["git", "ls-files", "-z"], cwd=candidate, capture_output=True)
        if got.returncode == 0:
            return sorted(p for p in got.stdout.decode("utf-8").split("\0") if p)
    raise ValueError("cannot enumerate tracked files")


def tokens(text):
    # Keep operators separate from argv: a quoted/escaped semicolon is a value.
    # This bounded lexer handles quotes, escapes and comments, not expansions.
    out, word, quote, started = [], "", "", False
    i = 0
    while i < len(text):
        c = text[i]
        if c == "\\" and quote != "'" and i + 1 < len(text):
            following = text[i + 1]
            if quote == '"' and following not in '$`"\\\n':
                word += c  # double quotes preserve a backslash before ordinary letters
            else:
                i += 1
                if following != "\n":
                    word += following
            started = True
        elif quote:
            if c == quote:
                quote = ""
            else:
                word += c
        elif c in "\"'":
            quote, started = c, True
        elif c == "#" and not started:
            break
        elif c.isspace() or c in ";&|`(){} ,":
            if started:
                out.append((word, False))
            word, started = "", False
            if not c.isspace():
                out.append((c, True))
        else:
            word += c
            started = True
        i += 1
    if started:
        out.append((word, False))
    return out


def source_view(text):
    # A lexical Go comment filter preserves physical newlines and code after */.
    # Strings/raw strings are opaque. Shell snippets are not given Go // syntax.
    pattern = re.compile(STRING.pattern + r"|//[^\n]*|/\*[\s\S]*?(?:\*/|$)")

    def replace(match):
        value = match[0]
        if not value.startswith(("//", "/*")):
            return value
        return " " + "\n" * value.count("\n")

    return pattern.sub(replace, text)


def command_hits(parts):
    parts = [(p, False) if isinstance(p, str) else p for p in parts]
    hits = set()
    for start, (executable, operator) in enumerate(parts):
        if operator or not EXECUTABLE.fullmatch(executable):
            continue
        argv = []
        for part, operator in parts[start + 1:]:
            if operator:
                break
            argv.append(part.rstrip(":"))
        name = executable.rsplit("/", 1)[-1]
        if name == "make":
            if argv[:1] == ["use"]:
                hits.add("retired make use")
            continue
        if name == "kagent":
            # Former executable transport, with global URL/context flags too.
            if "invoke" in argv:
                hits.add("retired kagent invoke transport")
            continue
        words, flags = [], {}
        i = 0
        while i < len(argv):
            value = argv[i]
            if value.startswith("-"):
                flag, equal, arg = value.partition("=")
                if not equal and (flag in VALUE_FLAGS or flag in {"--step", "--payload", "--session", "--runtime"}):
                    i += 1
                    arg = argv[i] if i < len(argv) else ""
                flags.setdefault(flag, []).append(arg)
            else:
                words.append(value)
            i += 1
        if words[:1] in (["govern"], ["use"]) or words[:2] == ["agent", "edit"]:
            hits.add("retired root govern/use or agent edit")
        if "--legacy-kagent" in flags:
            hits.add("retired --legacy-kagent opt-in")
        local = words[:1] == ["up"] or words[:2] == ["local", "up"]
        cloud = words[:1] == ["lift"] or words[:2] == ["aks", "up"]
        if local and LOCAL_STEPS.intersection(flags.get("--step", [])):
            hits.add("retired local installation step")
        if cloud and "kagent" in flags.get("--payload", []):
            hits.add("retired cloud kagent payload")
        if cloud and CLOUD_STEPS.intersection(flags.get("--step", [])):
            hits.add("retired cloud phase")
        if (words[:1] == ["chat"] or words[:2] == ["agent", "chat"]) and {"--session", "--json"}.intersection(flags):
            hits.add("retired chat transport flag")
    return hits


def literal_value(literal):
    if literal.startswith("`"):
        return literal[1:-1]
    if literal.startswith('"'):
        try:
            return json.loads(literal)
        except ValueError:
            pass  # Go-only escapes require command tests, not a new language parser
    return None


def content_hits(text, source=False):
    hits = command_hits(tokens(text))
    if ASSET.search(text):
        hits.add("retired asset reference")
    # Source messages and standalone CI samples may contain whole commands.
    # Never recursively expand shell argv, including command-looking contexts.
    for match in STRING.finditer(text):
        prefix = text[:match.start()].rstrip()
        message = source and re.search(
            r"(?:\breturn|\b(?:fmt\.(?:Print|Println|Printf|Sprintf|Errorf)|errors\.New|notef)\s*\("
            r"|\bfmt\.F(?:print|printf|println)\s*\(\s*[\w.]+(?:\(\))?\s*,)$", prefix)
        if not prefix or message:
            value = literal_value(match[0])
            if value is not None:
                hits.update(command_hits(tokens(value)))
    # Only literal bounded argv arrays, not arbitrary Go syntax or identifiers.
    for match in SOURCE_ARGV.finditer(text):
        args = STRING.findall(match[1])
        residue = STRING.sub("", match[1]).replace(",", "").strip()
        values = [literal_value(a) for a in args]
        if args and not residue and all(v is not None for v in values):
            hits.update(command_hits(["kmx"] + values))
    # Recognize only these existing literal command builders. A simple dynamic
    # value occupies one argv slot so context/URL values cannot shift the verb.
    for match in SOURCE_CALL.finditer(text):
        args = []
        for arg in re.split(r',(?=(?:[^\"]*\"[^\"]*\")*[^\"]*$)', match[2]):
            arg = arg.strip()
            if STRING.fullmatch(arg):
                value = literal_value(arg)
                if value is None:
                    break
                args.append(value)
            elif re.fullmatch(r"[\w.]+", arg):
                args.append("__dynamic__")
            else:
                break  # arbitrary expression composition is outside this guard
        prefix = [] if match[1] == "exec.Command" else ["kmx"]
        hits.update(command_hits(prefix + args))
    if GOVERN_ROUTE.search(text):
        hits.add("retired govern command route")
    return hits


def judge(paths, entries, root):
    exact = {(e["path"], e["text"]): i for i, e in enumerate(entries)}
    findings, read, used = [], 0, set()
    for path in paths:
        if path in RETIRED_PATHS or path.startswith(RETIRED_DIRS):
            findings.append((path, 0, "retired asset path"))
        if path in SELF or pathlib.Path(path).suffix.lower() in SKIP_SUFFIX:
            continue
        try:
            text = (root / path).read_text(encoding="utf-8")
        except (OSError, UnicodeError) as e:
            findings.append((path, 0, f"cannot read UTF-8 text: {e}"))
            continue
        read += pathlib.Path(path).suffix.lower() in TEXT_SUFFIX or path in {"Makefile", "install.sh"}
        if inert(path):
            continue
        if path == "CHANGELOG.md":
            # Current Unreleased instructions are scanned; dated releases are records.
            text = re.split(r"(?m)^## \[?v?\d", text, maxsplit=1)[0]
        original = text.splitlines()
        physical = source_view(text).splitlines() if path.endswith(".go") else original
        n = 0
        while n < len(physical):
            start, line = n, physical[n].strip()
            n += 1
            if path.endswith((".py", ".sh")) and line.startswith("#"):
                continue
            block = [line]
            # Real continuations only: a following comment/version never supplies argv.
            while n < len(physical) and len(block) < MAX_LINES and block[-1].endswith("\\"):
                block[-1] = block[-1][:-1]
                block.append(physical[n].strip())
                n += 1
            # Literal Go argv may wrap without shell continuation.
            array = re.search(r"\[\]string\s*\{", line)
            closing = "}" if array else ")"
            if array or re.search(r"\b(?:operationCommand|exec\.Command|retiredCommand)\s*\(", line):
                while n < len(physical) and len(block) < MAX_LINES and closing not in block[-1]:
                    block.append(physical[n].strip())
                    n += 1
            hits = content_hits(" ".join(block), source=path.endswith(".go"))
            if not hits:
                continue
            hit = exact.get((path, original[start].strip())) if len(block) == 1 else None
            if hit is not None:
                used.add(hit)
                continue
            findings.extend((path, start + 1, h) for h in sorted(hits))
    return findings, read, used


def main(argv, root=ROOT):
    if argv == ["--selftest"]:
        return selftest()
    if argv:
        print("usage: check-legacy-runtime.py [--selftest]", file=sys.stderr)
        return 2
    try:
        entries = load_allowlist(root / ALLOWLIST)
        paths = tracked(root)
        findings, read, used = judge(paths, entries, root)
    except (OSError, ValueError, UnicodeError) as e:
        print(f"check-legacy-runtime: {e}", file=sys.stderr)
        return 1
    bad = []
    if len(paths) < MIN_TRACKED or read < MIN_READ:
        bad.append(f"scan coverage too small: {len(paths)} enumerated, {read} known text read "
                   f"(minimum {MIN_TRACKED}/{MIN_READ})")
    if bad:
        print("check-legacy-runtime: this scan proves nothing: " + "; ".join(bad), file=sys.stderr)
        return 1
    for i, entry in enumerate(entries):
        if i not in used:
            findings.append((entry["path"], 0, "stale exact exception: " + entry["text"]))
    if findings:
        for path, n, why in findings:
            print(f"check-legacy-runtime: {path}:{n}: {why}", file=sys.stderr)
        return 1
    print(f"check-legacy-runtime: {len(paths)} tracked, {read} known text read, "
          f"{len(entries)} exact exceptions; no retired commands/assets")
    return 0


def selftest():
    failed, count = [], 0

    def check(want, got, label):
        nonlocal count
        count += 1
        if got != want:
            failed.append(f"{label}: wanted {want!r}, got {got!r}")

    retired = [
        'kmx local up --step "kagent"', "kmx aks up --payload 'kagent'",
        'kagent invoke --agent demo', 'kagent --kagent-url http://localhost:8083 invoke demo',
        'kmx govern demo', 'kmx use orka', 'make use PRESET=orka', 'kmx agent edit demo',
        'bin/kmx --context "remote team" agent --container-engine=podman edit demo',
        'kmx --context=kind-x --confirm use orka', 'kmx up --step=agent',
        'kmx local up --step tools-agent', 'kmx quickstart --legacy-kagent',
        'kmx lift --payload=kagent', 'kmx aks up --step plane', 'kmx lift --step credential',
        'kmx agent chat demo --session="abc"', 'kmx agent --context x chat --json=false',
        'kmx chat demo --session="abc"', 'kmx --context x chat --json=false',
        'args := []string{"chat", "demo", "--session", "abc"}',
        'a.operationCommand("chat", name, "--json")',
        'kmx agent create demo; kmx govern demo', 'kmx context use x && kmx use orka',
        'kmx up --step \\\n "kagent"', 'kmx \\\n --context x \\\n agent edit demo',
        'kmx agent status demo # --runtime-version v0.10.3\nkmx govern demo',
        'kmx govern demo && echo --runtime-version v0.10.3',
        'kmx --context x use orka # --runtime-version v0.10.3',
        'kmx agent create demo || kmx local up --step "tools-agent"',
        "kmx local up --step 'agent' --step 'orka'", 'kmx lift --step boundary',
        'kmx lift --step agents', 'kmx lift --step kagent',
        'args := []string{"agent", "chat", "demo", "--session", "abc"}',
        'return []string{"agent",\n"edit",\n"demo"}',
        'return []string{"--context", "remote team", "use", "orka"}',
        'fmt.Println("kmx agent edit demo")', 'apply k8s/hello-world.yaml',
        'a.operationCommand("govern", "demo")', 'a.operationCommand("use", "orka")',
        'a.operationCommand("agent", "edit", name)',
        'exec.Command("kagent", "--kagent-url", endpoint, "invoke", name)',
        'exec.Command("kmx", "--context", context, "agent", "edit", name)',
        'a.operationCommand("agent",\n "edit",\n name)',
        'cmd := &cobra.Command{Use: "govern <name>"}',
        'retiredCommand("govern", "kubectl")',
        'kmx --context ";" govern demo',
        'kmx agent create demo // note; kmx govern demo',
        'return []string{"agent", // comment\n "edit",\n "demo"}',
        'a.operationCommand("agent", // comment\n "edit",\n "demo")',
        'return []string {\n "agent",\n "edit",\n "demo"}',
        'helm install -f ./kagent-values.yaml kagent',
        'kubectl apply -f ./k8s/hello-world.yaml',
        '/*\n kmx context show\n */ kmx govern demo',
        'ordinary() /* inert */; a.operationCommand("govern", "demo")',
        'kmx --context \\; govern demo',
        'kmx --context "&&" agent edit demo',
        'return []string {\n "agent", // comment\n "edit",\n "demo"}',
        'a.operationCommand("agent", /* inert */ "edit", "demo")',
        'fmt.Fprintf(a.Out, "kmx govern demo")', 'a.notef("kmx govern demo")',
        'fmt.Fprintln(a.Err, "Run kmx agent edit demo")',
        'fmt.Fprint(out, "kmx use orka")',
        'kmx local up --step ka\\gent',
        r'fmt.Fprintf(a.Out, "kmx --context \";\" govern demo")',
        r'a.notef("kmx --context \";\" govern demo")',
        r'args := []string{"--context", "\";\"", "govern", "demo"}',
        r'a.operationCommand("--context", "\";\"", "govern", "demo")',
        'a.notef(`kmx --context ";" govern demo`)',
    ]
    current = [
        'args := []string{"context", "use", "kind-demo"}',
        'kmx context use "remote team"', 'kmx --context use context show',
        'kmx create --live demo --runtime kagent', 'kmx agent lift demo --runtime kagent',
        'kmx agent status demo --runtime kagent # --runtime-version v0.10.3',
        'kmx agent status demo --runtime kagent && echo --runtime-version v0.10.3',
        'kmx agent status demo --runtime kagent\n# --runtime-version v0.10.3',
        'kmx local up --step orka', 'kmx aks up --payload orka --step verify',
        'kmx run demo --json', 'kmx chat demo # --session abc',
        'kmx chat demo && echo --json', 'kmx chat demo --json-output',
        'kmx agent run demo --json', 'kmx agent chat demo # --session abc',
        'kmx agent chat demo && echo --json', 'echo use; kmx context show',
        'type KagentLift struct { KagentBindings ModelConfig } // kagent.dev/v1alpha2',
        'helm install kagent official --set kagent-tools.enabled=false',
        'helm pull oci://ghcr.io/kagent-dev/kagent/helm/kagent',
        'kubectl create namespace kagent', 'kmx local up --step kagent-other',
        'kmx aks up --payload kagent-other', 'kmx agent chat demo --json-output',
        'return []string{"context",\n "use",\n "kind-demo"}',
        'kmx context use "agent edit"', 'kmx --namespace use context show',
        'kmx context use x; echo --legacy-kagent', 'kmx agent chat demo --message "invoke"',
        'a.operationCommand("context", "use", context)',
        'a.operationCommand("create", "--live", name, "--runtime", "kagent")',
        'exec.Command("kubectl", "get", "agents.kagent.dev")',
        'use := &cobra.Command{Use: "use <context>"}',
        'retiredCommand("edit", "kubectl --context <ctx> edit agents.core.orka.ai")',
        'current() // a.operationCommand("govern", "demo")',
        'current() // exec.Command("kagent", "invoke", name)',
        'kmx --context "kmx use orka" context show',
        'kmx --context "|" context show',
        '/*\n kmx govern demo\n */ ordinary()',
        'ordinary() /* kmx govern demo */; current()',
        'a.operationCommand("context", // comment\n "use",\n "demo")',
        'fmt.Println("ordinary // still a string")',
        'a.operationCommand("context", "use", "kmx use orka")',
        'a.operationCommand("context", "use", ";")',
        'return []string{"context", "use", "kmx use orka"}',
        'ordinary() // kmx govern demo',
        'ordinary() /* kmx govern demo\n kagent invoke demo */; current()',
        'kmx local up --step "ka\\gent"',
        'kmx local up --step "ka\\\\gent"',
        'kmx --context "kmx govern demo" context show',
        'a.operationCommand("context", "use", "kmx govern demo")',
        'exec.Command("kmx", "context", "use", "kmx govern demo")',
        'unrelated("kmx govern demo")',
        r'fmt.Fprintf(a.Out, "kmx --context \"kmx use orka\" context show")',
        r'a.notef("kmx --context \"kmx use orka\" context show")',
        r'args := []string{"context", "use", "\"kmx govern demo\""}',
        r'a.operationCommand("context", "use", "\"kmx govern demo\"")',
        'a.notef(`kmx --context "kmx use orka" context show`)',
    ]
    with tempfile.TemporaryDirectory(prefix="retired-command-selftest-") as tmp:
        root = pathlib.Path(tmp)
        (root / "scripts").mkdir()
        policy = root / ALLOWLIST
        policy.write_text("[]", encoding="utf-8")
        for i in range(MIN_TRACKED):
            (root / f"coverage-{i}.md").write_text("ordinary text\n", encoding="utf-8")
        planted = root / "command.go"
        planted.write_text("", encoding="utf-8")
        subprocess.run(["git", "init", "-q"], cwd=root, check=True)
        subprocess.run(["git", "add", "."], cwd=root, check=True)

        def run():
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                return main([], root)

        shell = root / "command.sh"
        shell.write_text("ordinary", encoding="utf-8")
        subprocess.run(["git", "add", "command.sh"], cwd=root, check=True)
        # Literal expectations; shell and source use their real comment syntax.
        for text, want in [(t, True) for t in retired] + [(t, False) for t in current]:
            target = shell if re.match(r"(?:[\w./$-]+/)?(?:kmx|kagent|make|helm|kubectl)\s", text) else planted
            target.write_text(text + "\n", encoding="utf-8")
            check(want, bool(judge([target.name], [], root)[0]), text)
            check(int(want), run(), "main: " + text)
            target.write_text("ordinary", encoding="utf-8")
        planted.write_text("ordinary text", encoding="utf-8")
        for path in sorted(RETIRED_PATHS) + [d + "source.go" for d in RETIRED_DIRS] + ["k8s/models/new-preset.yaml"]:
            target = root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("ordinary text", encoding="utf-8")
            check(True, bool(judge([path], [], root)[0]), path)
        for path, text in (
                ("example_test.go", 'kmx govern demo'), ("scripts/test_example.py", 'kagent invoke demo'),
                ("internal/testdata/fixture.yaml", 'kmx local up --step kagent'),
                ("command.go", '// kmx govern demo'), ("command.go", 'ordinary // kmx govern demo'),
                ("CHANGELOG.md", '# Changes\n## [Unreleased]\ncurrent\n## [v0.1.0]\nkmx govern demo')):
            target = root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(text, encoding="utf-8")
            check(False, bool(judge([path], [], root)[0]), "inert " + path)
        planted.write_text("ordinary text", encoding="utf-8")
        entry = {"path": "command.go", "text": '"kmx govern demo",', "reason": "inert CI-style sample"}
        for text, want in ((entry["text"], 0), (entry["text"] + ' "kmx use orka",', 1), ("ordinary", 1)):
            policy.write_text(json.dumps([entry]), encoding="utf-8")
            planted.write_text(text, encoding="utf-8")
            check(want, run(), "exact exception/staleness: " + text)
        for doc in ({}, [dict(entry, extra="x")], [dict(entry, reason="")],
                    [dict(entry, text=12)], [dict(entry, path="../guide.md")],
                    [dict(entry, path="/guide.md")], [dict(entry, text="two\nlines")], [entry, entry]):
            policy.write_text(json.dumps(doc), encoding="utf-8")
            check(1, run(), "invalid exception " + repr(doc))
        policy.write_text("[]", encoding="utf-8")
        planted.write_text("ordinary", encoding="utf-8")
        check(0, run(), "empty exceptions valid")
        for paths in ([], ["command.go"], [f"coverage-{i}.md" for i in range(MIN_TRACKED)]):
            # Only the Git enumeration boundary is replaced; verdict logic is real.
            with mock.patch.dict(globals(), tracked=lambda r: paths):
                check(1 if len(paths) < MIN_TRACKED else 0, run(), "enumeration floor")
        unknown = [f"unknown-{i}" for i in range(MIN_TRACKED)]
        for path in unknown:
            (root / path).write_text("ordinary", encoding="utf-8")
        with mock.patch.dict(globals(), tracked=lambda r: unknown):
            check(1, run(), "no readable known text")
        with mock.patch.dict(globals(), tracked=lambda r: unknown[:51] + [f"coverage-{i}.md" for i in range(149)]):
            check(1, run(), "shrunken known-text coverage")
        for data in (b"\xff", b"ordinary"):
            planted.write_bytes(data)
            if data == b"ordinary":
                planted.unlink()
            check(1, run(), "invalid UTF-8 or missing tracked text")
        planted.write_text("ordinary", encoding="utf-8")
    check(0, main([]), "real tracked tree")
    if failed:
        print("check-legacy-runtime self-test: " + "\n".join(failed), file=sys.stderr)
        return 1
    print(f"check-legacy-runtime self-test: {count} judge/main cases passed")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
