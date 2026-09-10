#!/usr/bin/env python3
"""Hold the live coordination board to itself, without retaining its archive.

Check lane uniqueness, contradictory statuses and merged-PR references against
local git history. Retired worker prompts and delta sheets are optional; when
present, their consistency checks still apply. A missing or malformed lane table
is always a failure. A shallow or absent merge history is explicitly SKIPPED.

scripts/board-open-drift.json can record known findings by claim AND lane. A
new finding fails, as does a stale ledger entry after its claim actually ran.
The checker never edits the board. Synthetic self-tests exercise both compact
and legacy formats without borrowing live prose or requiring real git history.

Run:  python3 scripts/check-board.py
      python3 scripts/check-board.py --selftest
"""
from __future__ import annotations

import json
import pathlib
import re
import runpy
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
BOARD = "docs/COORDINATION.md"
DRIFT = "scripts/board-open-drift.json"
TABLE = "State of the world"
PROMPTS = "Ready-to-paste worker prompts"
SHEETS = "Delta sheets from finished lanes"
LANE = r"[WP]\d+[a-z]?"
# Worded lanes are valid in anchored positions, not in arbitrary prose.
OPENER = r"[WP](?:\d+[a-z]?|-[A-Z][A-Z0-9-]*)"


class Anchor(Exception):
    """A claim lost its structural anchor; never count that as a pass."""


class Skipped(Exception):
    """A claim cannot be answered here, so its recorded debts remain open."""


class Finding:
    def __init__(self, lane: str, message: str):
        self.lane = lane
        self.message = message
        self.claim = ""

    def key(self) -> str:
        return f"{self.claim}:{self.lane}"

    def __str__(self) -> str:
        return f"[{self.claim}] {self.lane}: {self.message}"


def flat(text: str) -> str:
    return " ".join(text.split())


def prose(text: str) -> str:
    """Compare title words rather than Markdown or punctuation."""
    text = re.sub(r"[`*~_\"']", " ", text.lower())
    text = re.sub(r"[^a-z0-9]+", " ", text)
    return flat(text)


class Row:
    """Lane identifiers come from both the title and the worker/owner cell.

    A phase can span milestones with distinct worker numbers. Descriptive rows
    without identifiers remain outside identifier-based checks, but their status
    contradictions and explicit merged references are still checked.
    """
    def __init__(self, cells: list[str], line: int):
        self.cells = cells
        self.line = line
        self.lane_cell, self.owner = cells[0], cells[1]
        self.text = flat(" | ".join(cells))
        self.ids = set()
        opener = re.match(rf"\s*~*\s*({OPENER})\s*:", self.lane_cell)
        if opener:
            self.ids.add(opener.group(1))
        self.ids |= set(re.findall(rf"\b({LANE})\s+worker\b", self.owner))

    def title(self) -> str:
        """Remove the identifier and board-only trailing review references."""
        title = re.sub(rf"^\s*~*\s*{OPENER}\s*:\s*", "", self.lane_cell)
        title = re.sub(r"\s*\([^()]*\)\s*$", "", title.strip())
        return re.sub(r"~+$", "", title).strip()

    def says_shipped(self) -> bool:
        # Older rows sometimes put their shipping status in the owner cell.
        return bool(re.search(r"\b(MERGED|BUILT|SHIPPED)\b", self.text))

    def says_unassigned(self) -> bool:
        return bool(re.search(r"\bunassigned\b", self.text, re.I))


class Prompt:
    def __init__(self, ident: str, heading: str, line: int):
        self.id = ident
        self.heading = heading
        self.line = line
        self.pasteable = bool(re.search(r"\(UNASSIGNED\b", heading))


class Board:
    def __init__(self, text: str):
        if not text.strip():
            raise Anchor(f"{BOARD} is empty")
        self.text = text
        self.rows = self._rows()
        self._headings()

    def section(self, heading: str) -> tuple[str, int]:
        """Body and first line number of exactly one named level-two section."""
        hits = [m for m in re.finditer(rf"^## {re.escape(heading)}\s*$", self.text, re.M)]
        if len(hits) != 1:
            raise Anchor(f"{len(hits)} sections in {BOARD} are headed {heading!r}, not one — "
                         "this checker no longer knows where to read")
        start = hits[0].end()
        rest = re.search(r"^## ", self.text[start:], re.M)
        body = self.text[start:start + rest.start()] if rest else self.text[start:]
        return body, self.text[:start].count("\n") + 1

    def _rows(self) -> list[Row]:
        body, first = self.section(TABLE)
        rows = []
        header, separator = False, False
        for offset, line in enumerate(body.splitlines()):
            stripped = line.strip()
            if "|" not in stripped:
                continue
            if not (stripped.startswith("|") and stripped.endswith("|")):
                raise Anchor(f"malformed lane table row at line {first + offset}: missing edge pipe")
            cells = [c.strip() for c in re.split(r"(?<!\\)\|", stripped[1:-1])]
            if not header:
                if [c.lower() for c in cells] != ["lane", "owner", "status", "notes"]:
                    raise Anchor(f"the {TABLE} table has a missing or malformed header")
                header = True
                continue
            if not separator:
                if len(cells) != 4 or not all(re.fullmatch(r":?-{3,}:?", c) for c in cells):
                    raise Anchor(f"the {TABLE} table has a missing or malformed separator")
                separator = True
                continue
            # Some preserved rows append extra notes columns. Require the four
            # documented columns, but do not silently discard those old notes.
            if len(cells) < 4 or not all(cells[:3]) or cells[0].lower() == "lane":
                raise Anchor(f"malformed lane table row at line {first + offset}")
            if all(re.fullmatch(r":?-+:?", c) for c in cells):
                raise Anchor(f"malformed lane table row at line {first + offset}: repeated separator")
            rows.append(Row(cells, first + offset))
        if not rows:
            raise Anchor(f"the {TABLE} table parsed to no rows — refusing to report a board "
                         "with no lanes in it consistent")
        return rows

    def _headings(self) -> None:
        """Check retained prompts anywhere; absent retired sections are valid.

        Legacy prompts were interleaved with other sections, so headings still
        carry their own state. Unknown identifiers/states fail loudly. A named
        section with no readable entries remains an error, not an empty pass.
        """
        sections = set(re.findall(r"^## (.+?)\s*$", self.text, re.M))
        self.prompts, self.sheets = [], []
        for offset, line in enumerate(self.text.splitlines()):
            if not line.startswith("### "):
                continue
            opener = re.match(rf"### ({OPENER})\s+—\s", line)
            named = re.match(r"### (.+?)\s+—\s", line)
            # Require the state dash: '(RUN 3)' in a batch heading is not a prompt.
            state = re.search(r"\((UNASSIGNED|RUN)\s+—", line)
            if state and opener:
                self.prompts.append(Prompt(opener.group(1), line, offset + 1))
            elif state and named:
                raise Anchor(f"the heading at line {offset + 1} says it is a worker prompt and "
                             f"this file cannot read a lane out of it: {line.strip()!r}")
            elif re.search(r"\(PRs? #\d+[^)]*merged|\(20\d\d-\d\d-\d\d\)", line):
                self.sheets.append((set(re.findall(rf"\b({LANE})\b", line)), line, offset + 1))
            elif opener:
                raise Anchor(f"the heading at line {offset + 1} opens with a lane and says neither "
                             f"that it is pasteable nor that it is a record of a lane that "
                             f"finished: {line.strip()!r}")
        if PROMPTS in sections:
            body, first = self.section(PROMPTS)
            if not any(first <= p.line < first + len(body.splitlines()) for p in self.prompts):
                raise Anchor("no prompt headings in the retained worker prompts section")
        if SHEETS in sections:
            body, first = self.section(SHEETS)
            if not any(first <= line < first + len(body.splitlines()) for _, _, line in self.sheets):
                raise Anchor("no delta sheets in the retained delta sheets section")

    def rows_for(self, ident: str) -> list[Row]:
        return [r for r in self.rows if ident in r.ids]


def worktree(start: pathlib.Path) -> pathlib.Path:
    """Resolve git through the board symlink used by the mutation harness."""
    for where in (start, (start / BOARD).resolve().parent):
        got = subprocess.run(["git", "-C", str(where), "rev-parse", "--show-toplevel"],
                             capture_output=True)
        if got.returncode == 0 and got.stdout.strip():
            return pathlib.Path(got.stdout.decode().strip())
    return start


EMPTY_WORDS = set("the a an and or of to in on for with by as at is it that this its not no "
                  "our we you your one two".split())
SHARED_WORDS = 3
SHARE_OF_TITLE = 0.55


def content(text: str) -> set[str]:
    return {w for w in prose(text).split() if len(w) > 2 and w not in EMPTY_WORDS}


class History:
    """Squash-merge numbers and subjects from this branch, without network.

    Older rebase merges have no PR number, so claims cannot speak below the
    lowest recorded number. Board-only merges are bookkeeping, not proof that
    the work described by a lane title shipped. Shallow history is unavailable.
    """
    def __init__(self, root: pathlib.Path, log: str | None = None, shallow: bool | None = None):
        self.root = worktree(root)
        self.shallow = self._shallow() if shallow is None else shallow
        self.merges: dict[int, str] = {}
        self.bookkeeping: set[int] = set()
        for entry in (self._log() if log is None else log).split("\0"):
            subject, _, names = entry.strip("\n").partition("\n")
            m = re.search(r"\(#(\d+)\)\s*$", subject)
            if not m or int(m.group(1)) in self.merges:
                continue
            number = int(m.group(1))
            self.merges[number] = subject[:m.start()].strip()
            touched = {n for n in names.splitlines() if n.strip()}
            if touched and touched <= {BOARD}:
                self.bookkeeping.add(number)
        self.available = bool(self.merges) and not self.shallow
        self.why = ""
        if self.shallow:
            self.why = ("the checkout is shallow, so the merge ledger is not here — "
                        "`fetch-depth: 0` is what makes these claims run in CI")
        elif not self.merges:
            self.why = "`git log` reported no merge carrying a pull-request number"

    def _shallow(self) -> bool:
        got = subprocess.run(["git", "-C", str(self.root), "rev-parse", "--is-shallow-repository"],
                             capture_output=True)
        return got.stdout.decode().strip() == "true"

    def _log(self) -> str:
        got = subprocess.run(["git", "-C", str(self.root), "log", "--name-only",
                              "--pretty=format:%x00%s"], capture_output=True)
        return got.stdout.decode(errors="replace") if got.returncode == 0 else ""

    def floor(self) -> int:
        return min(self.merges)

    def named(self, title: str) -> tuple[int, str, float] | None:
        """Flag a merge sharing at least three words and 55% of a lane title.

        This is a question for the coordinator, not proof a lane is DONE: two
        pieces of work could share a title. Thresholds retain the original
        board checker's measured matching behavior.
        """
        want = content(title)
        if len(want) < SHARED_WORDS:
            return None
        best = None
        for number, subject in sorted(self.merges.items(), reverse=True):
            if number in self.bookkeeping:
                continue
            shared = len(want & content(subject))
            score = shared / len(want)
            if shared >= SHARED_WORDS and score >= SHARE_OF_TITLE \
                    and (best is None or score > best[2]):
                best = (number, subject, score)
        return best


CLAIMS: list = []


def claim(fn):
    CLAIMS.append(fn)
    return fn


@claim
def one_row_per_lane(board: Board, history: History) -> list[Finding]:
    """Workers are unique; phases may have separate milestone rows."""
    out = []
    for ident in sorted({i for r in board.rows for i in r.ids if i.startswith("W")}):
        rows = board.rows_for(ident)
        if len(rows) > 1:
            where = ", ".join(f"line {r.line}" for r in rows)
            out.append(Finding(ident, f"{len(rows)} rows in the lane table are about this lane "
                                      f"({where}); one lane, one row"))
    return out


@claim
def no_row_says_both_unassigned_and_shipped(board: Board, history: History) -> list[Finding]:
    out = []
    for row in board.rows:
        if row.says_unassigned() and row.says_shipped():
            for ident in sorted(row.ids) or [row.title()[:40]]:
                out.append(Finding(ident, f"the row at line {row.line} says the lane is unassigned "
                                          "and also that it merged"))
    return out


@claim
def every_prompt_has_a_row(board: Board, history: History) -> list[Finding]:
    known = {i for r in board.rows for i in r.ids}
    return [Finding(p.id, f"the prompt at line {p.line} is for a lane with no row in the "
                          f"{TABLE} table")
            for p in board.prompts if p.id not in known]


@claim
def one_prompt_per_lane(board: Board, history: History) -> list[Finding]:
    out = []
    seen: dict[str, list[Prompt]] = {}
    for p in board.prompts:
        seen.setdefault(p.id, []).append(p)
    for ident, prompts in sorted(seen.items()):
        if len(prompts) > 1:
            where = ", ".join(f"line {p.line}" for p in prompts)
            out.append(Finding(ident, f"{len(prompts)} prompts are headed with this lane "
                                      f"({where}); a worker handed one cannot tell which is live"))
    return out


@claim
def a_shipped_lane_has_no_pasteable_prompt(board: Board, history: History) -> list[Finding]:
    # A rowless prompt is reported by every_prompt_has_a_row, not ignored.
    out = []
    for p in board.prompts:
        if not p.pasteable:
            continue
        rows = board.rows_for(p.id)
        if rows and all(r.says_shipped() for r in rows):
            out.append(Finding(p.id, f"the row at line {rows[0].line} says this lane shipped and "
                                     f"the prompt at line {p.line} still reads UNASSIGNED — "
                                     "pasting it asks for work that exists"))
    return out


@claim
def a_retired_prompt_cites_the_pull_request_its_row_cites(board: Board,
                                                       history: History) -> list[Finding]:
    out = []
    for p in board.prompts:
        if p.pasteable:
            continue
        said = set(re.findall(r"#(\d+)", p.heading))
        if not said:
            continue
        for row in board.rows_for(p.id):
            cited = set(re.findall(r"#(\d+)", row.text))
            if cited and not (said & cited):
                out.append(Finding(p.id, f"the retired prompt at line {p.line} says this lane "
                                         f"merged as {sorted('#' + n for n in said)} and the row "
                                         f"at line {row.line} cites "
                                         f"{sorted('#' + n for n in cited)} — one of them is "
                                         "about a different lane"))
    return out


@claim
def a_lane_with_a_delta_sheet_is_not_waiting(board: Board, history: History) -> list[Finding]:
    out = []
    for ids, heading, line in board.sheets:
        for ident in sorted(ids):
            for row in board.rows_for(ident):
                if row.says_unassigned():
                    out.append(Finding(ident, f"the row at line {row.line} says unassigned, and "
                                              f"the delta sheet at line {line} is the record of "
                                              "this lane finishing"))
    return out


@claim
def every_merged_pr_the_board_cites_is_in_the_ledger(board: Board, history: History) -> list[Finding]:
    if not history.available:
        raise Skipped(history.why)
    out, floor = [], history.floor()
    cited: dict[int, str] = {}
    for m in re.finditer(r"PRs? (#\d+(?:/#\d+)*)\s+MERGED", board.text):
        for number in re.findall(r"\d+", m.group(1)):
            cited.setdefault(int(number), m.group(0))
    for m in re.finditer(r"merged as #(\d+)", board.text):
        cited.setdefault(int(m.group(1)), m.group(0))
    if not cited:
        raise Anchor("the board no longer cites a merged pull request in a form this file can "
                     "read — the claim would pass having checked none")
    for number, quoted in sorted(cited.items()):
        if number >= floor and number not in history.merges:
            out.append(Finding(f"#{number}", f"the board says {quoted!r}, and no merge in this "
                                             f"history carries #{number} (the ledger runs from "
                                             f"#{floor} to #{max(history.merges)})"))
    return out


@claim
def no_unstarted_row_for_work_already_merged(board: Board, history: History) -> list[Finding]:
    if not history.available:
        raise Skipped(history.why)
    out = []
    for row in board.rows:
        if not row.ids or row.says_shipped():
            continue
        hit = history.named(row.title())
        if hit:
            number, subject, score = hit
            out.append(Finding(sorted(row.ids)[0],
                               f"the row at line {row.line} claims nothing has shipped, and "
                               f"#{number} merged saying {round(score * 100)}% of what the row "
                               f"itself calls this lane: {subject!r}"))
    return out


class Ledger:
    """Explicit debts keyed by claim and lane, never a blanket suppression."""
    def __init__(self, spec: dict | None):
        self.entries = (spec or {}).get("open", [])
        self.accepted: dict[str, str] = {}
        for entry in self.entries:
            for field in ("claim", "lanes", "raised", "owed"):
                if not entry.get(field):
                    raise Anchor(f"an entry in {DRIFT} has no {field!r} — an accepted finding "
                                 "with no claim, no lanes, no date or no sentence saying what is "
                                 "owed is a silencer, not a ledger")
            for lane in entry["lanes"]:
                self.accepted[f"{entry['claim']}:{lane}"] = entry["owed"]

    def unpaid(self, findings: list[Finding]) -> list[Finding]:
        return [f for f in findings if f.key() not in self.accepted]

    def struck(self, findings: list[Finding], claims_ran: set[str]) -> list[str]:
        """Entries no longer earned, but only for claims actually evaluated."""
        live = {f.key() for f in findings}
        out = []
        for key in sorted(self.accepted):
            name, lane = key.split(":", 1)
            if name in claims_ran and key not in live:
                out.append(f"{DRIFT} still carries {lane} under [{name}], and the board no longer "
                           "disagrees there — strike it off")
        return out


def load_ledger(root: pathlib.Path) -> Ledger:
    path = root / DRIFT
    if not path.exists():
        return Ledger(None)
    try:
        return Ledger(json.loads(path.read_text()))
    except json.JSONDecodeError as e:
        raise Anchor(f"{DRIFT} is not readable JSON ({e}) — a ledger that cannot be read "
                     "must not be read as empty")


def check(board_text: str, history: History,
          ledger: Ledger) -> tuple[list[str], list[Finding], int, list[str]]:
    """Problems, all findings, claims attempted, explicit skip notes."""
    problems: list[str] = []
    try:
        board = Board(board_text)
    except Anchor as e:
        return [f"the board could not be read: {e}"], [], 0, []
    findings, ran, names, skipped = [], 0, set(), []
    for fn in CLAIMS:
        ran += 1
        try:
            found = fn(board, history)
        except Skipped as e:
            skipped.append(f"[{fn.__name__}] {e}")
            continue
        except Anchor as e:
            problems.append(f"[{fn.__name__}] {e}")
            continue
        names.add(fn.__name__)
        for f in found:
            f.claim = fn.__name__
        findings.extend(found)
    problems.extend(str(f) for f in ledger.unpaid(findings))
    problems.extend(ledger.struck(findings, names))
    return problems, findings, ran, skipped


def exit_code(problems: list[str]) -> int:
    """The verdict. Tested separately so findings cannot be ignored at exit."""
    return 1 if problems else 0


def main(argv) -> int:
    if argv[:1] == ["--selftest"]:
        return selftest()
    if argv:
        print(f"check-board: unexpected argument {argv[0]!r}", file=sys.stderr)
        return 2
    if not CLAIMS:
        print("check-board: no claims are registered — refusing to report the board checked.",
              file=sys.stderr)
        return 1
    root = worktree(ROOT)
    try:
        board_text = (root / BOARD).read_text(encoding="utf-8")
        history = History(root)
        ledger = load_ledger(root)
    except (Anchor, OSError) as e:
        print(f"check-board: {e}", file=sys.stderr)
        return 1
    problems, findings, ran, skipped = check(board_text, history, ledger)
    accepted = [f for f in findings if f.key() in ledger.accepted]
    for note in skipped:
        print(f"check-board: SKIPPED {note}")
    if problems:
        print(f"\ncheck-board: {BOARD} disagrees with itself", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        print(f"\n{len(problems)} disagreement(s). The board has one writer: report these to the "
              f"coordinator, or record them in {DRIFT} with what is owed.", file=sys.stderr)
        return exit_code(problems)
    print(f"check-board: {ran - len(skipped)} of {ran} claim(s) checked against {BOARD}, "
          "the board agrees with itself")
    if accepted:
        print(f"       {len(accepted)} known finding(s) still stand open in {DRIFT}:")
        for entry in ledger.entries:
            lanes = [f.lane for f in accepted if f.claim == entry["claim"]]
            print(f"       - [{entry['claim']}] {len(lanes)} lane(s) raised {entry['raised']}: "
                  + ", ".join(lanes))
    return exit_code(problems)


def selftest() -> int:
    tests = runpy.run_path(str(ROOT / "scripts/test_check_board.py"))
    return tests["selftest"](sys.modules[__name__])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
