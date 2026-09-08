#!/usr/bin/env python3
"""Hold docs/COORDINATION.md to itself.

The board's lane table has said a lane was unassigned when it had already
shipped five times in six days, and twice it carried two rows for one lane
that contradicted each other. Each time it was hand-patched. On the day
this was written it held 61 rows — 43 of them a numbered lane — and 38
ready-to-paste worker prompts, all of it moving daily; those three counts
come from this file reading the board, which is the only way a number
about the board stays true. A document changing that fast, whose claims
are checked only by whoever last remembered to look, drifts by
construction.

A stale row is not cosmetic. It invites a worker to rebuild something that
exists — a row said unassigned for the lane that had shipped `kmx tools
add`, and a fresh session pasting the prompt under it would have rebuilt a
merged command.

WHAT THIS CHECKS, AND WHAT IT DELIBERATELY DOES NOT.

  Whether a lane is DONE is a judgement, and nothing here asks it. Whether
  the board CONTRADICTS ITSELF is not: two rows for one lane, a row that
  says both unassigned and merged, a pasteable prompt for a lane the table
  says shipped, a finished lane's delta sheet above a row that says nobody
  has started — the document answers all of these about itself, and a
  disagreement is a fact about the document rather than an opinion about
  the work.

  One claim reaches outside the document, to the only ledger that can say
  a lane shipped without being told: the subjects of the merges on this
  branch's history. Pull-request titles do not carry lane identifiers —
  the row that was stale the day this was written belongs to a lane that
  merged as "Govern a runtime this repository did not write: what it needs,
  and the two things it cannot have" — but they do carry the lane's own
  words, because the board writes
  a lane's title in prose and the person who ships it writes the same
  sentence at the top of the merge. So the match is on the title the row
  itself states, and it fires only for a row that claims nothing shipped.

  There is no network here and no token. `git log` is the ledger, which
  means a shallow checkout has no ledger at all. Those claims then name
  themselves as SKIPPED rather than passing, and nothing they would have
  found is treated as settled, because a check that quietly did not run is
  the failure this repository keeps finding.

DERIVED, NOT COPIED. There is no list of lanes in this file. Every lane it
knows about it read out of the table, every prompt out of the prompts
section, every merge out of git. A guard that restates what it guards
agrees with it forever.

THE EMPTY CASE IS A FAILURE. A table that parses to no rows, a prompts
section with no prompts, a history with no merges: each is a failure here
rather than a clean run. A check that examined nothing has not checked
anything.

THE BOARD IS NOT THIS LANE'S TO EDIT. It has one writer. So the drift that
exists today is recorded in scripts/board-open-drift.json — by claim and
by lane, dated, with a sentence saying what is owed — and this file prints
how much of it is outstanding on every run. An entry that no longer
matches anything is a failure too: the ledger is a list of debts, and a
paid one has to be struck off rather than left to grow stale in its turn.

Run:  python3 scripts/check-board.py
      python3 scripts/check-board.py --selftest
"""
from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
BOARD = "docs/COORDINATION.md"
DRIFT = "scripts/board-open-drift.json"

TABLE = "State of the world"
PROMPTS = "Ready-to-paste worker prompts"
SHEETS = "Delta sheets from finished lanes"

# A lane's identifier as the board writes it: a phase or worker number,
# with the letter suffix that distinguishes a lane split in two.
LANE = r"[WP]\d+[a-z]?"


class Anchor(Exception):
    """The board no longer says the thing a claim was anchored to.

    Raised rather than returned because it is not the board contradicting
    itself — it is this checker having lost its grip, which must be as
    loud as a real finding and must never be a pass.
    """


class Skipped(Exception):
    """A claim that cannot be answered here.

    A shallow checkout has no merge ledger, and the two claims that read
    one can then say nothing. Saying nothing must not read as saying
    everything is fine: a skipped claim is named in the output, and the
    ledger of open findings is not allowed to strike off a debt under it,
    because nothing looked.
    """


class Finding:
    """One disagreement, tied to the lane it is about.

    The lane is what makes an entry in the open-drift ledger answerable to
    something: a debt is recorded against a claim and a lane, so a new
    lane failing the same claim is a new failure rather than one already
    forgiven.
    """

    def __init__(self, lane: str, message: str):
        self.lane = lane
        self.message = message
        self.claim = ""

    def key(self) -> str:
        return f"{self.claim}:{self.lane}"

    def __str__(self) -> str:
        return f"[{self.claim}] {self.lane}: {self.message}"


def flat(text: str) -> str:
    """One line, single-spaced."""
    return " ".join(text.split())


def prose(text: str) -> str:
    """A title reduced to the words in it, so two spellings of the same
    sentence compare equal.

    The board writes a lane title with backticks, em dashes and its own
    punctuation; a merge subject writes the same sentence with whatever
    punctuation the sentence needed. Neither is the fact. The words are.
    """
    text = re.sub(r"[`*~_\"']", " ", text.lower())
    text = re.sub(r"[^a-z0-9]+", " ", text)
    return flat(text)


class Row:
    """One line of the lane table.

    `ids` is the lane or lanes the row is about, and it comes from two
    places because the board uses both: the identifier the lane cell opens
    with, and the worker named in the owner cell. A row whose lane cell
    opens with a phase and its milestone, owned by a numbered worker, is
    one lane under two names, and a checker reading only one of them would
    think half the prompts below had no row at all.
    """

    def __init__(self, cells: list[str], line: int):
        self.cells = cells
        self.line = line
        self.lane_cell, self.owner = cells[0], cells[1]
        self.text = flat(" | ".join(cells))
        self.ids = set()
        opener = re.match(rf"\s*~*\s*({LANE})\s*:", self.lane_cell)
        if opener:
            self.ids.add(opener.group(1))
        self.ids |= set(re.findall(rf"\b({LANE})\s+worker\b", self.owner))

    def title(self) -> str:
        """What the row calls the work: the identifier off the front, and
        the board's cross-references off the back.

        A lane cell ends in a bracketed reference to the rulings that
        authorised it, or to the review that raised it. Those point into
        this document and appear in nothing else, so left in they are
        words the row has that no merge subject can ever share — and on
        some rows they are half the title.
        """
        title = re.sub(rf"^\s*~*\s*{LANE}\s*:\s*", "", self.lane_cell)
        title = re.sub(r"\s*\([^()]*\)\s*$", "", title.strip())
        return re.sub(r"~+$", "", title).strip()

    def says_shipped(self) -> bool:
        """Whether the row says this lane landed.

        Read across the whole row rather than one column, because the
        board does not keep the status in one column: most rows put it
        third, and the row that most needed saying — the half-shipped lane
        whose prompt would rebuild a merged command — says it in the owner
        column instead, in bold, because that is where the writer was
        looking.
        """
        return bool(re.search(r"\b(MERGED|BUILT|SHIPPED)\b", self.text))

    def says_unassigned(self) -> bool:
        return bool(re.search(r"\bunassigned\b", self.text, re.I))


class Prompt:
    """One ready-to-paste worker prompt, by its heading.

    `pasteable` is the heading's own word. The board marks a prompt it has
    retired by rewriting the heading — `(RUN — merged as #95; kept as the
    record of what the lane was asked for)` — so a heading still saying
    UNASSIGNED is the board inviting a fresh session to paste it.
    """

    def __init__(self, ident: str, heading: str, line: int):
        self.id = ident
        self.heading = heading
        self.line = line
        self.pasteable = bool(re.search(r"\(UNASSIGNED\b", heading))


class Board:
    """docs/COORDINATION.md: its lane table, its prompts, its delta sheets."""

    def __init__(self, text: str):
        if not text.strip():
            raise Anchor(f"{BOARD} is empty")
        self.text = text
        self.rows = self._rows()
        self._headings()

    def section(self, heading: str) -> tuple[str, int]:
        """(body, first line number) of the one `##` section with this
        heading. Zero means the board has been reorganised under this
        file's feet; two means the anchor cannot say which one it read."""
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
        for offset, line in enumerate(body.splitlines()):
            stripped = line.strip()
            if not stripped.startswith("|"):
                continue
            cells = [c.strip() for c in stripped.strip("|").split("|")]
            if len(cells) < 3 or not set("".join(cells)) - set("- :"):
                continue
            if cells[0].lower() == "lane":
                continue
            rows.append(Row(cells, first + offset))
        if not rows:
            raise Anchor(f"the {TABLE} table parsed to no rows — refusing to report a board "
                         "with no lanes in it consistent")
        return rows

    def _headings(self) -> None:
        """Every `###` heading, split into prompts and delta sheets by the
        word the board itself puts in the parenthesis.

        Section boundaries cannot do this. The prompts are not one block:
        five other `##` sections are interleaved among them, so anything
        reading only the section named after them would find the first
        nine prompts, report on those, and call the rest checked. The
        discriminator that does hold is the heading's own state word —
        UNASSIGNED or RUN for a prompt, a merged pull request or a date
        for a sheet. A heading that opens with a lane and says neither is
        a state this file has not been taught, and it stops the run rather
        than being filed under nothing.
        """
        for heading in ("## " + PROMPTS, "## " + SHEETS):
            if heading not in self.text:
                raise Anchor(f"{BOARD} no longer has a section headed {heading!r} — the board has "
                             "been reorganised under this checker")
        self.prompts, self.sheets = [], []
        for offset, line in enumerate(self.text.splitlines()):
            if not line.startswith("### "):
                continue
            opener = re.match(rf"### ({LANE})\s+—\s", line)
            if re.search(r"\((UNASSIGNED|RUN)\b", line) and opener:
                self.prompts.append(Prompt(opener.group(1), line, offset + 1))
            elif re.search(r"\(PRs? #\d+[^)]*merged|\(20\d\d-\d\d-\d\d\)", line):
                self.sheets.append((set(re.findall(rf"\b({LANE})\b", line)), line, offset + 1))
            elif opener:
                raise Anchor(f"the heading at line {offset + 1} opens with a lane and says neither "
                             f"that it is pasteable nor that it is a record of a lane that "
                             f"finished: {line.strip()!r}")
        if not self.prompts:
            raise Anchor("no prompt headings this file can read — every claim about a pasteable "
                         "prompt would pass having read none")
        if not self.sheets:
            raise Anchor("no delta sheets — the claims that read them would pass having read "
                         "nothing")

    def rows_for(self, ident: str) -> list[Row]:
        return [r for r in self.rows if ident in r.ids]


def worktree(start: pathlib.Path) -> pathlib.Path:
    """The repository this file belongs to, following symlinks to reach it.

    scripts/check-mutations.py runs a checker from a throwaway directory
    whose every entry except scripts/ is a symlink to the real tree — so
    the checker sees a docs/ but stands in something git knows nothing
    about. Asking git where the BOARD actually lives resolves that.
    """
    for where in (start, (start / BOARD).resolve().parent):
        got = subprocess.run(["git", "-C", str(where), "rev-parse", "--show-toplevel"],
                             capture_output=True)
        if got.returncode == 0 and got.stdout.strip():
            return pathlib.Path(got.stdout.decode().strip())
    return start


# Words that carry no subject. A title and a merge subject are compared
# on what is left after these, because "the" and "and" agreeing proves
# nothing and a short title is mostly them.
EMPTY_WORDS = set("the a an and or of to in on for with by as at is it that this its not no "
                  "our we you your one two".split())


SHARED_WORDS = 3
SHARE_OF_TITLE = 0.55


def content(text: str) -> set[str]:
    """The words in a title that are about the work."""
    return {w for w in prose(text).split() if len(w) > 2 and w not in EMPTY_WORDS}


class History:
    """What this branch's history says has merged, by pull-request number.

    Squash merges here end their subject with `(#N)`, which makes the log
    a ledger of numbers and titles with no network and no token. Three
    things it is honest about:

    ITS FLOOR. The oldest lanes were rebase-merged and their subjects
    carry no number at all, so the ledger cannot speak below the lowest
    number in it. A claim asking "does #4 exist?" would answer no and be
    wrong. The floor is read off the ledger rather than written down.

    THE BOARD'S OWN BOOKKEEPING. A merge that changed nothing but the
    board is the coordinator writing a lane down, and it quotes the lane's
    title while doing it. Left in, those merges answer "has this shipped?"
    with the sentence that asked the question. They are identified by what
    they touched, not by how their subject is worded.

    ITS ABSENCE. A shallow checkout — which is what `actions/checkout`
    does by default — has almost no history, and every claim resting on it
    would then pass by knowing nothing. So `available` is false and the
    claims say so out loud instead.
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
        """Every commit's subject and the files it touched, in one call."""
        got = subprocess.run(["git", "-C", str(self.root), "log", "--name-only",
                              "--pretty=format:%x00%s"], capture_output=True)
        return got.stdout.decode(errors="replace") if got.returncode == 0 else ""

    def floor(self) -> int:
        return min(self.merges)

    def named(self, title: str) -> tuple[int, str, float] | None:
        """The merge that says what this title says, if one does.

        Pull-request titles carry no lane identifier — the comment rule
        keeps planning numbers out of everything but the board — so the
        only thing a merge and a row can share is the words. They are
        never the same words exactly: a row says "the lift — local agent
        to AKS with managed observability" and the merge says "The lift:
        your local agent, running on AKS, with dashboards you did not ask
        for". So the measure is how much of the row's own vocabulary the
        merge repeats — at least three of its words, and at least 55% of
        them — and both numbers were set by measurement rather than
        taste: across the 43 lane rows on the board the day this was
        written, they matched 17 rows to the exact pull request the row
        itself cites and no row to a different one. The 25 they matched to
        nothing are mostly the lanes that merged before this repository
        started writing a merge subject as a sentence.

        Three words is the floor because two are not evidence: "enforcing
        MCP gateway" shares two with a merge about hosted upstreams, and a
        threshold that counted that would stop being evidence at all.

        What comes back is a question, not a verdict: either the lane
        shipped and nobody updated its row, or two pieces of work were
        given the same name. The board's writer answers it; this file only
        refuses to let it go unasked.
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
    """Two rows for one lane is the failure that has happened twice, and
    both times the two rows said different things.

    A worker number, not a phase number. The board splits a phase across
    milestones and gives each milestone its own row and its own worker, so
    two rows opening with the same phase are two lanes — and a check
    calling that a duplicate would be wrong about the one document it
    reads.
    """
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
    """A row cannot be waiting for somebody and finished at once."""
    out = []
    for row in board.rows:
        if row.says_unassigned() and row.says_shipped():
            for ident in sorted(row.ids) or [row.title()[:40]]:
                out.append(Finding(ident, f"the row at line {row.line} says the lane is unassigned "
                                          "and also that it merged"))
    return out


@claim
def every_prompt_has_a_row(board: Board, history: History) -> list[Finding]:
    """A prompt whose lane the table has forgotten.

    This is the paste-and-rebuild hazard in its purest form: text ready to
    hand to a fresh session, about a lane the board can say nothing about.
    """
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
    """The row says it merged and the prompt still says UNASSIGNED.

    The board already handles one of these by hand — a row carrying
    **HALF SHIPPED — do NOT paste the prompt below** because the prompt
    under it would rebuild a merged command. That warning is the practice;
    this is the same warning derived rather than remembered.
    """
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
def a_lane_with_a_delta_sheet_is_not_waiting(board: Board, history: History) -> list[Finding]:
    """A delta sheet is written when a lane finishes. A row for that lane
    saying nobody has started it contradicts the sheet three sections
    down."""
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
    """A row saying `PR #N MERGED` names a number that merged.

    Below the ledger's floor the oldest lanes were rebase-merged and their
    subjects carry no number, so nothing can be concluded about them and
    nothing is.
    """
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
    """The stale row, caught the only way a document cannot catch it.

    Pull-request titles carry no lane identifier — the comment rule keeps
    planning numbers out of everything but this board — but they carry the
    lane's own sentence, because whoever ships a lane writes it up in the
    words the row already uses. So for a row claiming nothing has shipped,
    this asks whether a merge on this branch's history says most of what
    that row says. A hit is either a lane that shipped and a row nobody
    updated, or two pieces of work given the same name; both are the
    board's to answer, and the second is answered by recording it.
    """
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
    """The drift the board carries today, by claim and by lane.

    This lane may not edit the board — it has one writer, and a checker
    that silently corrected the document would remove the evidence that it
    works. So what is wrong on the day this ships is written down here
    instead: every entry names the claim, the lanes, the date it was
    raised and the sentence describing what is owed.

    It fails in both directions on purpose. A finding not listed here is a
    failure, which is what makes tomorrow's drift visible. An entry that
    matches nothing is also a failure, which is what stops the ledger
    outliving the debt and quietly forgiving a lane that comes back.
    """

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
        """Entries the board no longer earns. Only for claims that ran: a
        claim skipped for want of a history would otherwise read as every
        debt under it paid."""
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
    """(problems, every finding, claims evaluated, claims skipped).

    A claim that could not find what it was anchored to is a problem, not
    a claim that quietly did not run.
    """
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
    """The verdict. Its own function because a verdict that is computed
    correctly and then not acted on is indistinguishable from a clean
    run."""
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


# --------------------------------------------------------------------------
# The self-test breaks the FIXTURE — the board, the history, the ledger —
# because scripts/check-mutations.py breaks this file and cannot see an
# assertion whose fixture already satisfies it by another route. Each edit
# below makes the real board say something contradictory in one specific
# way, and names the claim that has to notice.

BOARD_EDITS = [
    ("| W40: three places we say we protect something and do not (drift review A3, A9, A15) "
     "| W40 worker |",
     "| W40: three places we say we protect something and do not (drift review A3, A9, A15) "
     "| unassigned |",
     "says a lane is unassigned in the row that says it merged",
     "no_row_says_both_unassigned_and_shipped"),

    ("| W34b: the version handshake, the context guard, the credential lag | W34b worker "
     "| PR #118 MERGED | coordinator verification owed |\n",
     "| W34b: the version handshake, the context guard, the credential lag | W34b worker "
     "| PR #118 MERGED | coordinator verification owed |\n"
     "| W34b: the version handshake, the context guard, the credential lag | W34b worker "
     "| SHAPED, prompt below | second thoughts |\n",
     "carries two rows for one lane, saying different things",
     "one_row_per_lane"),

    ("| W36: `kmx workflow run` has no first command | W36 worker |",
     "| W44: `kmx workflow run` has no first command | W44 worker |",
     "renames a lane in the table and leaves its prompt behind",
     "every_prompt_has_a_row"),

    ("### W41 — govern a runtime this repository did not write (UNASSIGNED — paste into a "
     "fresh CLI session)\n",
     "### W41 — govern a runtime this repository did not write (UNASSIGNED — paste into a "
     "fresh CLI session)\n\n### W41 — govern a runtime this repository did not write, re-cut "
     "(UNASSIGNED — paste into a fresh CLI session)\n",
     "offers two prompts under one lane number",
     "one_prompt_per_lane"),

    ("### W32 — the release agent: Kaimahi's first real user (RUN — merged as #95;",
     "### W32 — the release agent: Kaimahi's first real user (UNASSIGNED — paste into a fresh "
     "CLI session; was merged as #95;",
     "invites a fresh session to paste the prompt for a lane its own row says merged",
     "a_shipped_lane_has_no_pasteable_prompt"),

    ("| W30: identity on the call, and credentials that expire (D35) | W30 worker | PR #86 MERGED",
     "| W30: identity on the call, and credentials that expire (D35) | unassigned | PR #86 MERGED",
     "says nobody has picked up a lane whose delta sheet is written",
     "a_lane_with_a_delta_sheet_is_not_waiting"),

    ("PR #134 MERGED — coordinator VERIFIED by execution",
     "PR #1340 MERGED — coordinator VERIFIED by execution",
     "cites a pull-request number that never merged",
     "every_merged_pr_the_board_cites_is_in_the_ledger"),

    ("| W40: three places we say we protect something and do not (drift review A3, A9, A15) "
     "| W40 worker | PR #134 MERGED",
     "| W40: three places we say we protect something and do not (drift review A3, A9, A15) "
     "| unassigned | SHAPED, prompt below, from #134",
     "says a lane is waiting to start, under a title whose own words are half references to "
     "this document",
     "no_unstarted_row_for_work_already_merged"),

    ("| W37: say the thing, not its planning number | W37 worker | PR #114 MERGED "
     "(+#115, #116 follow-ups) —",
     "| W37: say the thing, not its planning number | unassigned | SHAPED 2026-09-06 — "
     "prompt below;",
     "says a lane is waiting to start after it has already merged under its own title",
     "no_unstarted_row_for_work_already_merged"),
]


# Cases that must stay QUIET. A checker is also wrong when it answers a
# question nobody asked, and both of these are places where it nearly did:
# the ledger cannot speak about lanes older than its own first number, and
# a merge that only wrote the board down repeats the lane's title while
# proving nothing about whether the lane ran.
QUIET = [
    ("the board cites lanes that merged before the ledger's first number",
     None, None, "every_merged_pr_the_board_cites_is_in_the_ledger"),

    ("a lane whose words appear only in the coordinator writing the lane down",
     "| P8b: approval routing via Slack + per-approver identity (D21) | W18 worker "
     "| PR #41 MERGED (109e08d) ahead of",
     "| P8b: approval routing via Slack + per-approver identity (D21) | unassigned "
     "| SHAPED, prompt below; was going to be #41, ahead of",
     "no_unstarted_row_for_work_already_merged"),
]


def edit(text: str, find: str, replace: str) -> str:
    """The edit, refusing anything ambiguous — a `find` that matches twice
    is not the edit that was described, and one that matches nothing is a
    case that has stopped testing anything."""
    if text.count(find) != 1:
        raise Anchor(f"the board has {text.count(find)} places matching a self-test edit, "
                     f"not one: {find[:60]!r}")
    return text.replace(find, replace)


def selftest() -> int:
    root = worktree(ROOT)
    real = (root / BOARD).read_text(encoding="utf-8")
    history = History(root)
    nothing = Ledger(None)
    failed = 0
    proven: set[str] = set()

    if not history.available:
        print(f"FAIL the merge ledger is not available here ({history.why}), so the claims that "
              "read it cannot be proven by this run")
        return 1

    base, base_findings, ran, _ = check(real, history, nothing)
    known = {f.key() for f in base_findings}
    print(f"     the board as it stands: {ran} claims, {len(base_findings)} finding(s), "
          f"{len(base)} unaccepted")

    for find, replace, says, expect in BOARD_EDITS:
        try:
            broken = edit(real, find, replace)
        except Anchor as e:
            print(f"FAIL [{expect}] {e}")
            failed += 1
            continue
        _, found, _, _ = check(broken, history, nothing)
        fresh = [f for f in found if f.key() not in known]
        if any(f.claim == expect for f in fresh):
            proven.add(expect)
            print(f"ok   a board that {says} — [{expect}]")
        else:
            print(f"FAIL a board that {says} produced {len(fresh)} new finding(s), none from "
                  f"[{expect}]: {[str(f) for f in fresh][:3]}")
            failed += 1

    for says, find, replace, quiet in QUIET:
        try:
            text = real if find is None else edit(real, find, replace)
        except Anchor as e:
            print(f"FAIL [{quiet}] {e}")
            failed += 1
            continue
        _, found, _, _ = check(text, history, nothing)
        # A case with no edit is about the board as it stands, so every
        # finding under the claim is noise. Subtracting the baseline there
        # would compare the board to itself and see nothing.
        already = known if find else set()
        noisy = [f for f in found if f.claim == quiet and f.key() not in already]
        if noisy:
            print(f"FAIL {says} was reported as a finding: {[str(f) for f in noisy]}")
            failed += 1
        else:
            print(f"ok   {says} says nothing — [{quiet}]")

    # The empty cases. Each of these is a board this file can read no
    # claims out of, and each has to be a failure rather than a clean run.
    for name, text, expect in (
            ("an empty board", "", "is empty"),
            ("a board whose lane table has no rows",
             re.sub(r"(## State of the world\n\n)(?:\|.*\n)+", r"\1", real),
             "parsed to no rows"),
            ("a board whose prompts are in a state this file was never taught",
             real.replace("(UNASSIGNED — ", "(WITHDRAWN — "), "says neither"),
            ("a board with no worker prompts left",
             real.replace("\n### W", "\n#### W").replace("\n### P", "\n#### P"),
             "no prompt headings"),
            ("a board whose lane table has been renamed",
             real.replace("## State of the world", "## Where things stand"),
             "not one"),
            ("a board that no longer says a pull request merged",
             real.replace(" MERGED", " landed").replace("merged as #", "landed as #"),
             "[every_merged_pr_the_board_cites_is_in_the_ledger]")):
        if text == real:
            print(f"FAIL {name} could not be constructed — the case tests nothing")
            failed += 1
            continue
        problems, found, _, _ = check(text, history, nothing)
        if any(expect in p for p in problems):
            print(f"ok   {name} is refused, saying {expect!r}")
        else:
            print(f"FAIL {name} produced {len(problems)} problem(s), none saying {expect!r}")
            failed += 1

    # A shallow checkout has no merge ledger. The claims that read one
    # must say so and produce nothing — and, the part that is easy to get
    # wrong, the ledger must not then read their open entries as paid.
    blind = History(root, log=history._log(), shallow=True)
    if blind.available or not blind.why:
        print("FAIL a shallow checkout was not recognised as having no merge ledger")
        failed += 1
    else:
        problems, found, _, notes = check(real, blind, load_ledger(root))
        struck = [p for p in problems if "strike it off" in p]
        if any(f.claim == "no_unstarted_row_for_work_already_merged" for f in found):
            print("FAIL a claim that reads the merge ledger produced findings without one")
            failed += 1
        elif struck:
            print(f"FAIL a skipped claim made the ledger look paid: {struck}")
            failed += 1
        elif len(notes) != 2:
            print(f"FAIL a shallow checkout skipped {len(notes)} claim(s) out loud, not the two "
                  "that read the merge ledger")
            failed += 1
        else:
            print("ok   with no merge ledger the claims that need one are skipped, and their "
                  "open findings are not struck off")

    # The ledger, in both directions.
    live = next((f for f in base_findings if f.key() in load_ledger(root).accepted), None)
    if live is None:
        print("FAIL the ledger accepts nothing on today's board, so neither direction is tested")
        failed += 1
    else:
        one = Ledger({"open": [{"claim": live.claim, "lanes": [live.lane], "raised": "2026-09-08",
                                "owed": "a sentence"}]})
        problems, _, _, _ = check(real, history, one)
        if any(str(live) == p for p in problems):
            print("FAIL a finding recorded in the ledger was reported as a failure anyway")
            failed += 1
        elif not problems:
            print("FAIL a ledger holding one finding forgave every other one too")
            failed += 1
        else:
            print("ok   a recorded finding is accepted and the rest still fail")

        stale = Ledger({"open": [{"claim": live.claim, "lanes": ["W99"], "raised": "2026-09-08",
                                  "owed": "a sentence"}]})
        problems, _, _, _ = check(real, history, stale)
        if any("strike it off" in p for p in problems):
            print("ok   a ledger entry the board no longer earns is a failure")
        else:
            print("FAIL a ledger entry matching nothing was left to grow stale")
            failed += 1

    try:
        Ledger({"open": [{"claim": "one_row_per_lane", "lanes": ["W1"], "raised": "2026-09-08"}]})
        print("FAIL a ledger entry with no sentence saying what is owed was accepted")
        failed += 1
    except Anchor:
        print("ok   a ledger entry that does not say what is owed is refused")

    if exit_code(["a disagreement"]) == 1 and exit_code([]) == 0:
        print("ok   a run with disagreements exits non-zero and a clean one exits zero")
    else:
        print("FAIL the verdict does not follow from the findings")
        failed += 1

    # The question scripts/check-mutations.py cannot ask, because it never
    # touches the fixture: for each claim, what would have to change for it
    # to fail? A claim no edit above breaks is a claim whose fixture
    # satisfies it by some other route, and it is not proven.
    for fn in CLAIMS:
        if fn.__name__ not in proven:
            print(f"FAIL nothing in this self-test breaks [{fn.__name__}], so nothing shows it "
                  "would notice")
            failed += 1

    if failed:
        print(f"\ncheck-board self-test: {failed} case(s) failed", file=sys.stderr)
        return 1
    print(f"\ncheck-board self-test: {len(CLAIMS)} claims, each broken by one of "
          f"{len(BOARD_EDITS)} deliberate contradictions, every one caught")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
