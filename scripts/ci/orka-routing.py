#!/usr/bin/env python3
"""Independent, default-RUN Orka path decision. No paths or Git errors are logged."""
import argparse
import json
import pathlib
import re
import subprocess

POLICY = pathlib.Path(__file__).with_suffix(".json")


def safe_path(path, policy):
    # Refuse malformed/ambiguous records rather than normalizing away a boundary.
    if (any(ord(c) < 32 or ord(c) == 127 or c == "\\" for c in path)
            or any(part in ("", ".", "..") for part in path.split("/"))):
        return False
    return (path in policy["safe_files"]
            or any(path.startswith(prefix) for prefix in policy["safe_prefixes"])
            or any(path.startswith(prefix) and "/" not in path[len(prefix):]
                   and path[len(prefix):].endswith(".md")
                   for prefix in policy["safe_direct_markdown"]))


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
        policy = json.loads(POLICY.read_text())
        return not all(safe_path(path, policy) for path in paths)
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
