#!/usr/bin/env python3
"""Synthetic board regressions; no dependence on the live board or git history.

Run directly, or through check-board.py --selftest. The latter passes the
checker module explicitly so mutation runs exercise the mutated checker,
not an import of the original file through a symlink.
"""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

CHECKER = None

COMPACT = """# Coordination

## State of the world

| Lane | Owner | Status | Notes |
|------|-------|--------|-------|
| P1: initial agent setup | W1 worker | PR #2 MERGED | predates numbered merge history |
| W2: bounded model request budgets (review alpha beta gamma delta epsilon zeta eta theta) | W2 worker | PR #12 MERGED | complete |
| W3: credentials expire promptly | W3 worker | PR #13 MERGED | complete |
| W-RENAME: package identity cleanup | rename worker | PR #14 MERGED | complete |
| W4: write example recipes | unassigned | SHAPED | implementation pending |
"""

LEGACY = COMPACT + """
## Ready-to-paste worker prompts

### W1 — initial agent setup (RUN — merged as #2; record)
### W2 — bounded model request budgets (RUN — merged as #12; record)
### W3 — credentials expire promptly (RUN — merged as #13; record)
### W-RENAME — package identity cleanup (RUN — merged as #14; record)
### W4 — write example recipes (UNASSIGNED — ready)

## Delta sheets from finished lanes

### W2 — budget implementation (PR #12 merged)
"""

LOG = "\0".join((
    "bounded model request budgets (#12)\nsrc/budgets.py\n",
    "credentials expire promptly (#13)\nsrc/credentials.py\n",
    "package identity cleanup (#14)\nsrc/package.py\n",
    "write example recipes (#15)\ndocs/COORDINATION.md\n",
))


class BoardTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.c = CHECKER
        cls.history = cls.c.History(cls.c.ROOT, log=LOG, shallow=False)

    def check(self, text=COMPACT, history=None, ledger=None):
        return self.c.check(text, history or self.history, ledger or self.c.Ledger(None))

    def assert_clean(self, text=COMPACT):
        problems, findings, ran, skipped = self.check(text)
        self.assertEqual(problems, [])
        self.assertEqual(findings, [])
        self.assertGreater(ran, 0, "an empty claim registry checks nothing")
        self.assertEqual(skipped, [])

    def assert_claim(self, text, claim, lane):
        problems, findings, _, _ = self.check(text)
        self.assertTrue(any(f.claim == claim and f.lane == lane for f in findings),
                        (claim, lane, problems))
        self.assertEqual(self.c.exit_code(problems), 1)

    def assert_refused(self, text, message):
        problems, _, _, _ = self.check(text)
        self.assertTrue(any(message in p for p in problems), problems)
        self.assertEqual(self.c.exit_code(problems), 1)

    def test_compact_prompt_free_board_passes(self):
        self.assert_clean()

    def test_legacy_sections_still_pass(self):
        self.assert_clean(LEGACY)

    def test_each_legacy_section_can_be_retired_independently(self):
        self.assert_clean(LEGACY.split("## Delta sheets", 1)[0])
        self.assert_clean(COMPACT + "\n## Delta sheets from finished lanes\n"
                          "\n### W2 — budget implementation (PR #12 merged)\n")

    def test_empty_board_fails(self):
        self.assert_refused("", "is empty")

    def test_empty_table_fails(self):
        self.assert_refused("## State of the world\n\n| Lane | Owner | Status | Notes |\n"
                            "|---|---|---|---|\n", "parsed to no rows")

    def test_separator_cannot_count_as_a_lane(self):
        self.assert_refused("## State of the world\n\n| Lane | Owner | Status | Notes |\n"
                            "|---|---|---|---|\n|---|---|---|---|\n"
                            "\n## References\nPR #12 MERGED\n", "malformed")

    def test_missing_table_fails(self):
        self.assert_refused(COMPACT.replace("State of the world", "Other heading"), "not one")

    def test_duplicate_table_section_fails(self):
        self.assert_refused(COMPACT + "\n## State of the world\n", "not one")

    def test_malformed_rows_cannot_hide_among_valid_rows(self):
        for row in ("| W9: broken | unassigned |\n",
                    "| W9: broken | unassigned | SHAPED |\n",
                    "| W9: broken | unassigned | SHAPED | note\n",
                    "W9: broken | unassigned | SHAPED | note |\n",
                    "| W9: broken | | SHAPED | note |\n",
                    "| W9: broken | unassigned | | note |\n",
                    "| | unassigned | SHAPED | note |\n"):
            with self.subTest(row=row):
                self.assert_refused(LEGACY.replace("\n## Ready-to-paste", row + "\n## Ready-to-paste"),
                                    "malformed")

    def test_header_is_required_and_not_silently_read_as_a_row(self):
        self.assert_refused(COMPACT.replace("| Lane | Owner | Status | Notes |\n", ""),
                            "header")
        self.assert_refused(COMPACT.replace("| Lane | Owner | Status | Notes |",
                                           "| Lane | State | Notes |"), "header")

    def test_separator_is_required(self):
        self.assert_refused(COMPACT.replace("|------|-------|--------|-------|\n", ""),
                            "separator")

    def test_escaped_pipes_and_legacy_extra_notes_are_supported(self):
        self.assert_clean(COMPACT.replace("| implementation pending |",
                                          "| run `printf x\\|y` | extra legacy note |"))

    def test_duplicate_worker_lane_fails(self):
        self.assert_claim(COMPACT + "| W2: duplicate | worker | SHAPED | note |\n",
                          "one_row_per_lane", "W2")

    def test_duplicate_named_lane_fails(self):
        self.assert_claim(COMPACT + "| W-RENAME: duplicate | worker | SHAPED | note |\n",
                          "one_row_per_lane", "W-RENAME")

    def test_phase_milestones_with_distinct_workers_are_not_duplicates(self):
        self.assert_clean(COMPACT + "| P1: another milestone | W9 worker | SHAPED | note |\n")

    def test_row_cannot_be_unassigned_and_shipped(self):
        self.assert_claim(COMPACT.replace("| W2 worker |", "| unassigned |"),
                          "no_row_says_both_unassigned_and_shipped", "W2")

    def test_shipped_status_in_owner_column_still_counts(self):
        self.assert_claim(COMPACT.replace("| unassigned | SHAPED | implementation pending |",
                                         "| SHIPPED | unassigned | implementation pending |"),
                          "no_row_says_both_unassigned_and_shipped", "W4")

    def test_stale_live_row_fails_against_history_despite_review_suffix(self):
        self.assert_claim(COMPACT.replace("| PR #12 MERGED |", "| SHAPED |"),
                          "no_unstarted_row_for_work_already_merged", "W2")

    def test_missing_merged_pr_fails(self):
        self.assert_claim(COMPACT.replace("PR #12 MERGED", "PR #120 MERGED"),
                          "every_merged_pr_the_board_cites_is_in_the_ledger", "#120")

    def test_merged_pr_groups_are_checked(self):
        self.assert_claim(COMPACT.replace("PR #12 MERGED", "PRs #12/#120 MERGED"),
                          "every_merged_pr_the_board_cites_is_in_the_ledger", "#120")

    def test_missing_merged_citations_fail_loudly(self):
        self.assert_refused(COMPACT.replace(" MERGED", " SHIPPED"),
                            "[every_merged_pr_the_board_cites_is_in_the_ledger]")

    def test_retained_prompt_must_have_a_row(self):
        self.assert_claim(LEGACY.replace("### W4 —", "### W99 —"),
                          "every_prompt_has_a_row", "W99")

    def test_retained_named_prompt_must_have_a_row(self):
        self.assert_claim(LEGACY.replace("| W-RENAME:", "| Old identity:"),
                          "every_prompt_has_a_row", "W-RENAME")

    def test_duplicate_prompts_fail(self):
        self.assert_claim(LEGACY + "\n### W2 — another prompt (RUN — record)\n",
                          "one_prompt_per_lane", "W2")

    def test_shipped_lane_cannot_offer_pasteable_prompt(self):
        self.assert_claim(LEGACY.replace("### W2 — bounded model request budgets (RUN —",
                                        "### W2 — bounded model request budgets (UNASSIGNED —"),
                          "a_shipped_lane_has_no_pasteable_prompt", "W2")

    def test_retired_prompt_must_cite_its_own_merge(self):
        self.assert_claim(LEGACY.replace("merged as #12;", "merged as #13;"),
                          "a_retired_prompt_cites_the_pull_request_its_row_cites", "W2")

    def test_finished_sheet_cannot_coexist_with_waiting_row(self):
        self.assert_claim(LEGACY.replace("| W2 worker |", "| unassigned |"),
                          "a_lane_with_a_delta_sheet_is_not_waiting", "W2")

    def test_unknown_prompt_state_fails(self):
        self.assert_refused(LEGACY.replace("(RUN —", "(WITHDRAWN —"), "says neither")

    def test_unreadable_prompt_identifier_fails(self):
        self.assert_refused(LEGACY.replace("### W-RENAME —", "### Unknown lane —"),
                            "cannot read a lane")

    def test_present_but_empty_legacy_sections_fail(self):
        self.assert_refused(COMPACT + "\n## Ready-to-paste worker prompts\n", "no prompt headings")
        self.assert_refused(COMPACT + "\n## Delta sheets from finished lanes\n", "no delta sheets")

    def test_prompts_outside_named_section_are_still_checked(self):
        self.assert_claim(COMPACT + "\n## Another section\n"
                          "\n### W2 — budgets (UNASSIGNED — ready)\n",
                          "a_shipped_lane_has_no_pasteable_prompt", "W2")

    def test_batch_run_heading_is_not_mistaken_for_a_prompt(self):
        self.assert_clean(COMPACT + "\n### Batch verification — seven lanes (RUN 3)\n")

    def entry(self, claim, lane):
        return {"claim": claim, "lanes": [lane], "raised": "2026-09-10", "owed": "Update row"}

    def test_ledger_accepts_only_the_recorded_claim_and_lane(self):
        text = COMPACT.replace("| W2 worker |", "| unassigned |")
        text = text.replace("| W3 worker |", "| unassigned |")
        text += "| W4: duplicate | worker | SHAPED | note |\n"
        claim = "no_row_says_both_unassigned_and_shipped"
        ledger = self.c.Ledger({"open": [self.entry(claim, "W2")]})
        problems, findings, _, _ = self.check(text, ledger=ledger)
        self.assertTrue(any(f.claim == claim and f.lane == "W2" for f in findings), problems)
        recorded = next(f for f in findings if f.claim == claim and f.lane == "W2")
        other_lane = next(f for f in findings if f.claim == claim and f.lane == "W3")
        other_claim = next(f for f in findings if f.claim == "one_row_per_lane")
        self.assertNotIn(str(recorded), problems)
        self.assertIn(str(other_lane), problems)
        self.assertIn(str(other_claim), problems)

    def test_stale_ledger_entries_fail(self):
        ledger = self.c.Ledger({"open": [self.entry("one_row_per_lane", "W99")]})
        problems, _, _, _ = self.check(ledger=ledger)
        self.assertTrue(any("strike it off" in p for p in problems), problems)

    def test_ledger_requires_explanation(self):
        entry = self.entry("one_row_per_lane", "W2")
        del entry["owed"]
        with self.assertRaises(self.c.Anchor):
            self.c.Ledger({"open": [entry]})

    def test_unavailable_history_is_explicit_and_does_not_clear_ledger(self):
        claim = "no_unstarted_row_for_work_already_merged"
        text = COMPACT.replace("| PR #12 MERGED |", "| SHAPED |")
        self.assert_claim(text, claim, "W2")
        ledger = self.c.Ledger({"open": [self.entry(claim, "W2")]})
        for log, shallow in ((LOG, True), ("", False)):
            with self.subTest(shallow=shallow):
                history = self.c.History(self.c.ROOT, log=log, shallow=shallow)
                self.assertFalse(history.available)
                self.assertTrue(history.why)
                problems, findings, _, skipped = self.check(text, history, ledger)
                self.assertEqual(problems, [])
                self.assertEqual(findings, [])
                self.assertEqual(len(skipped), 2)
                self.assertTrue(any(claim in s for s in skipped))

    def test_exit_code_reflects_problems(self):
        self.assertEqual(self.c.exit_code([]), 0)
        self.assertEqual(self.c.exit_code(["disagreement"]), 1)


def selftest(checker=None):
    global CHECKER
    if checker is None:
        path = pathlib.Path(__file__).with_name("check-board.py")
        spec = importlib.util.spec_from_file_location("check_board", path)
        checker = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(checker)
    CHECKER = checker
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(BoardTests)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    print(f"check-board self-test: {result.testsRun} tests, "
          f"{len(result.failures)} failures, {len(result.errors)} errors")
    return 0 if result.wasSuccessful() else 1


if __name__ == "__main__":
    raise SystemExit(selftest())
