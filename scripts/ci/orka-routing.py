#!/usr/bin/env python3
"""Independent, default-RUN Orka path decision. No paths or Git errors are logged."""
import argparse
import fnmatch
import json
import pathlib
import re
import subprocess

POLICY = pathlib.Path(__file__).with_suffix(".json")


def trigger_path(path, triggers):
    # Bad records always run, even when an exception could otherwise match.
    if (any(ord(c) < 32 or ord(c) == 127 or c == "\\" for c in path)
            or any(part in ("", ".", "..") for part in path.split("/"))):
        return True
    if path in triggers["exclude_files"]:
        return False
    parts = path.split("/")
    return (path in triggers["files"]
            or any(path.startswith(prefix) for prefix in triggers["prefixes"])
            or any(len(parts) == len(pattern.split("/"))
                   and all(fnmatch.fnmatchcase(part, glob) for part, glob in zip(parts, pattern.split("/")))
                   for pattern in triggers["globs"]))


def orka_required(base, head):
    try:
        # Full object IDs only: never let a ref/option/pathspec masquerade as a commit.
        if not all(re.fullmatch(r"(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})", sha)
                   for sha in (base, head)):
            return True
        for sha in (base, head):
            subprocess.run(["git", "cat-file", "-e", sha + "^{commit}"],
                           capture_output=True, check=True, timeout=30)
        diff = subprocess.run(["git", "diff", "--name-only", "--no-renames", "-z", base, head, "--"],
                              capture_output=True, check=True, timeout=30).stdout
        if not diff or not diff.endswith(b"\0"):
            return True
        paths = diff[:-1].decode("utf-8").split("\0")
        triggers = json.loads(POLICY.read_text())["triggers"]
        for name in ("prefixes", "files", "globs", "exclude_files"):
            values = triggers[name]
            if not isinstance(values, list) or not all(isinstance(value, str) and value for value in values):
                return True
        # Only the generated manifest is exempt; policy edits cannot exempt themselves.
        if any(path != "scripts/ci/orka-callers.json" for path in triggers["exclude_files"]):
            return True
        if not any(triggers[name] for name in ("prefixes", "files", "globs")):
            return True
        return any(trigger_path(path, triggers) for path in paths)
    except Exception:
        # Unknown history, tool/policy failure, or malformed bytes never grant a skip.
        return True


class QuietParser(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError("invalid arguments")


def main():
    required = True
    try:
        parser = QuietParser(add_help=False)
        parser.add_argument("--base", required=True)
        parser.add_argument("--head", required=True)
        args = parser.parse_args()
        required = orka_required(args.base, args.head)
    except Exception:
        pass
    print("true" if required else "false")


if __name__ == "__main__":
    main()
