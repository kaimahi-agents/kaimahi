#!/usr/bin/env python3
"""Fragment lifecycle tests use independent temporary files and real Git repositories."""
import contextlib
import importlib.util
import io
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("check-changelog-fragments.py")
SPEC = importlib.util.spec_from_file_location("changelog_fragments", SCRIPT)
helper = importlib.util.module_from_spec(SPEC) if SCRIPT.exists() else None
if helper is not None:
    SPEC.loader.exec_module(helper)

HISTORY = b"## v0.1.0 \xe2\x80\x94 2026-01-01\r\n\r\n- history  \r\nlast line"
PREAMBLE = b"# Changelog\r\n\r\nKeep this preamble.  \r\n\r\n"
CHANGELOG = PREAMBLE + b"## Unreleased\r\n\r\n### Fixed\r\n\r\n- old fix\r\n\r\n" + HISTORY


class FragmentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="kmx-fragments-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "changes").mkdir()
        self.put("changes/README.md", "guidance, not a note")
        self.put("CHANGELOG.md", CHANGELOG)
        self.assertIsNotNone(helper, "fragment helper has not been implemented")

    def put(self, path, content):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content.encode("utf-8") if isinstance(content, str) else content)

    def note(self, name="627.md", text="### Fixed\n\n- fresh fix\n"):
        self.put("changes/" + name, text)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], stderr=subprocess.STDOUT).decode().strip()

    def init(self):
        self.git("init", "-q")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "user.name", "Fixture")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "core.hooksPath", str(self.root / "no-hooks"))
        return self.commit()

    def commit(self):
        self.git("add", "--all")
        self.git("commit", "-qm", "fixture", "--allow-empty")
        return self.git("rev-parse", "HEAD")

    def test_valid_multicategory_and_readme_excluded(self):
        self.note(text="### Changed\n\n- change\n\n### Fixed\n\n- fix\n")
        self.assertEqual(helper.read_fragments(self.root), {
            "changes/627.md": {"Changed": "- change", "Fixed": "- fix"}})

    def test_invalid_notes_refuse_including_heading_inside_fence(self):
        for content in ("", "   ", "### Fixed\n", "### Surprise\n- no\n",
                        "- no category", "### Fixed\n- ok\n### Added\n",
                        "### Fixed\n- ok\n```\n## v9.0.0\n```\n",
                        "### Fixed\n- one\n### Fixed\n- two\n", b"\xff"):
            with self.subTest(content=content):
                self.note(text=content)
                with self.assertRaises(ValueError):
                    helper.read_fragments(self.root)

    def test_flat_regular_safe_inputs_only(self):
        for name in ("nested/627.md", "bad name.md", "bad\nname.md", "UPPER.md", "627.txt"):
            with self.subTest(name=name):
                self.note(name)
                with self.assertRaises(ValueError):
                    helper.read_fragments(self.root)
                target = self.root / "changes" / name
                target.unlink()
                if target.parent != self.root / "changes":
                    target.parent.rmdir()
        self.put("outside.md", "### Fixed\n- outside\n")
        (self.root / "changes/627.md").symlink_to(self.root / "outside.md")
        with self.assertRaises(ValueError):
            helper.read_fragments(self.root)
        (self.root / "changes/627.md").unlink()
        (self.root / "changes/README.md").unlink()
        (self.root / "changes/README.md").symlink_to(self.root / "outside.md")
        with self.assertRaises(ValueError):
            helper.read_fragments(self.root)

    def test_symlink_changes_directory_refused(self):
        (self.root / "changes/README.md").unlink()
        (self.root / "changes").rmdir()
        (self.root / "outside").mkdir()
        (self.root / "changes").symlink_to(self.root / "outside", target_is_directory=True)
        with self.assertRaises(ValueError):
            helper.read_fragments(self.root)

    def test_closed_exemptions(self):
        exempt = ["docs/new.js", "README.md", "deep/notes.md", "cmd/x_test.go",
                  "testdata/x.go", "internal/a/testdata/file.bin", "scripts/test_new.py",
                  "scripts/new-test.py", "scripts/new-test.sh"]
        product = [".github/workflows/ci.yml", "scripts/tool.py", "embed.go", "internal/a/prompt.txt",
                   "scripts/ci/test_new.py", "scripts/new-test.js", "cmd/deleted.go", "assets/icon.png"]
        for path in exempt:
            with self.subTest(path=path):
                self.assertTrue(helper.exempt(path))
        for path in product:
            with self.subTest(path=path):
                self.assertFalse(helper.exempt(path))

    def test_product_requires_added_note_not_old_pending_or_uncommitted(self):
        self.note("old.md")
        base = self.init()
        self.put("internal/source.go", "package internal\n")
        head = self.commit()
        self.note()
        with self.assertRaisesRegex(ValueError, "new.*fragment"):
            helper.check_diff(self.root, base, head)
        head = self.commit()
        helper.check_diff(self.root, base, head)

    def test_product_cannot_modify_delete_or_rename_base_note(self):
        self.note("old.md")
        base = self.init()
        for operation in ("modify", "delete", "rename"):
            with self.subTest(operation=operation):
                self.git("reset", "--hard", base)
                self.put("app.go", "package app\n")
                if operation == "modify":
                    self.note("old.md", "### Fixed\n- stolen note\n")
                elif operation == "delete":
                    (self.root / "changes/old.md").unlink()
                else:
                    (self.root / "changes/old.md").rename(self.root / "changes/new.md")
                self.note()
                with self.assertRaisesRegex(ValueError, "existing fragment"):
                    helper.check_diff(self.root, base, self.commit())

    def test_docs_release_prep_can_delete_included_notes(self):
        self.note()
        base = self.init()
        (self.root / "changes/627.md").unlink()
        self.put("CHANGELOG.md", "# Changelog\n\n## Unreleased\n\n## v0.2.0\n\n- fresh fix\n")
        helper.check_diff(self.root, base, self.commit())

    def test_git_deletions_and_renames_are_not_docs_only(self):
        self.put("old.go", "package app\n")
        base = self.init()
        (self.root / "old.go").rename(self.root / "documentation.md")
        with self.assertRaisesRegex(ValueError, "new.*fragment"):
            helper.check_diff(self.root, base, self.commit())
        self.note()
        helper.check_diff(self.root, base, self.commit())

    def test_git_nul_paths_and_deleted_product_require_note(self):
        self.put("odd\n\tname.go", "package app\n")
        base = self.init()
        (self.root / "odd\n\tname.go").unlink()
        head = self.commit()
        with self.assertRaisesRegex(ValueError, "new.*fragment"):
            helper.check_diff(self.root, base, head)
        self.note()
        helper.check_diff(self.root, base, self.commit())

    def test_git_validates_committed_fragments_and_symlink_modes(self):
        base = self.init()
        self.put("app.go", "package app\n")
        self.note(text="### Unknown\n- invalid\n")
        head = self.commit()
        self.note()
        with self.assertRaises(ValueError):
            helper.check_diff(self.root, base, head)
        (self.root / "changes/627.md").unlink()
        (self.root / "changes/627.md").symlink_to("README.md")
        with self.assertRaises(ValueError):
            helper.check_diff(self.root, base, self.commit())

    def test_ambiguous_refs_refuse_even_when_git_warnings_are_disabled(self):
        base = self.init()
        self.put("app.go", "package app\n")
        self.note()
        head = self.commit()
        self.git("tag", "shared", base)
        self.git("branch", "shared", head)
        self.git("config", "core.warnAmbiguousRefs", "false")
        for left, right in (("shared", head), (base, "shared")):
            with self.subTest(base=left, head=right):
                with self.assertRaisesRegex(ValueError, "ambiguous"):
                    helper.check_diff(self.root, left, right)
        helper.check_diff(self.root, "refs/tags/shared", head)
        helper.check_diff(self.root, base, "refs/heads/shared")

    def test_unavailable_or_option_refs_fail_closed(self):
        head = self.init()
        for ref in ("missing", "--all", "--output=stolen", "HEAD:CHANGELOG.md", "HEAD\x00bad"):
            with self.subTest(ref=ref):
                with self.assertRaises(ValueError):
                    helper.check_diff(self.root, ref, head)
                with self.assertRaises(ValueError):
                    helper.check_diff(self.root, head, ref)
        self.assertFalse((self.root / "stolen").exists())

    def test_render_category_merge_order_and_byte_preservation(self):
        self.note("623.md", "### Fixed\n- six\n\n### Breaking\n- break\n")
        self.note("1000.md", "### Upgrading\n- upgrade\n### Added\n- add\n### Fixed\n- thousand\n### Changed\n- change\n")
        before = {p: p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        got = helper.render(self.root, "v0.2.0", "2026-06-11")
        expected = PREAMBLE + (
            "## Unreleased\r\n\r\n## v0.2.0 — 2026-06-11\r\n\r\n"
            "### Added\r\n\r\n- add\r\n\r\n### Changed\r\n\r\n- change\r\n\r\n"
            "### Fixed\r\n\r\n- old fix\r\n\r\n- thousand\r\n\r\n- six\r\n\r\n"
            "### Breaking\r\n\r\n- break\r\n\r\n### Upgrading\r\n\r\n- upgrade\r\n\r\n"
        ).encode() + HISTORY
        self.assertEqual(got, expected)
        self.assertEqual(before, {p: p.read_bytes() for p in before})

    def test_render_rejects_bad_version_date_duplicate_and_bad_input(self):
        self.note()
        for version, date in (("0.2.0", "2026-06-11"), ("v0.2.0-rc.1", "2026-06-11"),
                              ("v01.2.0", "2026-06-11"), ("v0.2.0", "2026-02-30"),
                              ("v0.2.0", "20260611"), ("v0.1.0", "2026-06-11")):
            with self.subTest(version=version, date=date):
                with self.assertRaises(ValueError):
                    helper.render(self.root, version, date)
        self.note(text="### Nope\n- bad\n")
        with self.assertRaises(ValueError):
            helper.render(self.root, "v0.2.0", "2026-06-11")
        self.note()
        self.put("CHANGELOG.md", "# Changelog\n## Unreleased\nunknown prose\n")
        with self.assertRaises(ValueError):
            helper.render(self.root, "v0.2.0", "2026-06-11")

    def test_render_preserves_indented_blocks_and_horizontal_whitespace(self):
        self.put("CHANGELOG.md", PREAMBLE + b"## Unreleased\r\n\r\n### Fixed\r\n\r\n"
                 b"    old first\r\n    old second  \r\n\r\n" + HISTORY)
        self.note(text="### Fixed\n\n    new first\n    new second  \n")
        got = helper.render(self.root, "v0.2.0", "2026-06-11")
        for block in (b"    old first\r\n    old second  \r\n",
                      b"    new first\r\n    new second  \r\n"):
            with self.subTest(block=block):
                self.assertIn(block, got)
        self.note(text="### Fixed\n \t \n")
        with self.assertRaisesRegex(ValueError, "empty"):
            helper.read_fragments(self.root)

    def test_empty_release_refused_but_old_unreleased_alone_can_render(self):
        self.assertIn(b"- old fix", helper.render(self.root, "v0.2.0", "2026-06-11"))
        self.put("CHANGELOG.md", "# Changelog\n\n## Unreleased\n\n")
        with self.assertRaises(ValueError):
            helper.render(self.root, "v0.2.0", "2026-06-11")

    def test_independent_pr_merge_orders_render_identically(self):
        base = self.init()
        self.git("checkout", "-qb", "pr-1000")
        self.note("1000.md", "### Fixed\n- thousand\n")
        first = self.commit()
        self.git("checkout", "-qb", "pr-623", base)
        self.note("623.md", "### Fixed\n- six\n")
        second = self.commit()
        candidates = []
        for branch, left, right in (("merge-a", first, second), ("merge-b", second, first)):
            self.git("checkout", "-qb", branch, left)
            self.git("merge", "--no-edit", right)
            self.assertEqual((self.root / "CHANGELOG.md").read_bytes(), CHANGELOG)
            candidates.append(helper.render(self.root, "v0.2.0", "2026-06-11"))
        self.assertEqual(candidates[0], candidates[1])
        self.assertLess(candidates[0].index(b"- thousand"), candidates[0].index(b"- six"))

    def test_list_exact_cleanup_and_new_release_cycle(self):
        self.note("1000.md")
        self.note("623.md", "### Added\n- six\n")
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(helper.main(["--list"], self.root), 0)
        self.assertEqual(out.getvalue(), "changes/1000.md\nchanges/623.md\n")
        candidate = helper.render(self.root, "v0.2.0", "2026-06-11")
        self.note("incoming.md", "### Fixed\n- incoming\n")
        self.put("CHANGELOG.md", candidate)
        for path in out.getvalue().splitlines():
            (self.root / path).unlink()
        self.assertTrue((self.root / "changes/README.md").is_file())
        next_candidate = helper.render(self.root, "v0.3.0", "2026-06-12")
        self.assertEqual(next_candidate.count(b"- fresh fix"), 1)
        self.assertIn(b"- incoming", next_candidate)
        self.assertTrue(next_candidate.endswith(candidate[candidate.index(b"## v0.2.0"):]))

    def test_cli_argument_errors_and_validation_before_output(self):
        for args in (["--base", "HEAD"], ["--head", "HEAD"], ["--render", "v0.2.0"],
                     ["--date", "2026-06-11"], ["--list", "--render", "v0.2.0", "--date", "2026-06-11"],
                     ["--selftest", "--list"]):
            with self.subTest(args=args), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as got:
                    helper.main(args, self.root)
                self.assertEqual(got.exception.code, 2)
        self.note(text="### Unknown\n- bad\n")
        for args in ([], ["--list"], ["--render", "v0.2.0", "--date", "2026-06-11"]):
            out = io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(helper.main(args, self.root), 1)
            self.assertEqual(out.getvalue(), "")

    def cli(self, *args):
        scripts = self.root / "scripts"
        scripts.mkdir(exist_ok=True)
        shutil.copyfile(SCRIPT, scripts / SCRIPT.name)
        return subprocess.run([sys.executable, str(scripts / SCRIPT.name), *args],
                              cwd="/", capture_output=True)

    def test_cli_cannot_ignore_diff_failure(self):
        base = self.init()
        self.put("app.go", "package app\n")
        head = self.commit()
        result = self.cli("--base", base, "--head", head)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, b"")
        self.assertIn(b"new added valid fragment", result.stderr)
        self.note()
        head = self.commit()
        result = self.cli("--base", base, "--head", head)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b"1 pending fragment(s) valid", result.stdout)

    def test_cli_render_bytes_and_invalid_input_no_partial_output(self):
        self.note()
        result = self.cli("--render", "v0.2.0", "--date", "2026-06-11")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(result.stdout.startswith(PREAMBLE + b"## Unreleased\r\n\r\n"))
        self.assertIn("## v0.2.0 — 2026-06-11\r\n".encode(), result.stdout)
        self.assertTrue(result.stdout.endswith(HISTORY))
        self.note(text="### Unknown\n- bad\n")
        result = self.cli("--render", "v0.2.0", "--date", "2026-06-11")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, b"")

    def test_explicit_empty_cli_values_refuse_not_skip_validation(self):
        for args in (("--base=", "--head="), ("--render=", "--date=")):
            with self.subTest(args=args):
                result = self.cli(*args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, b"")

    def test_cli_root_is_script_location_not_cwd(self):
        scripts = self.root / "scripts"
        scripts.mkdir()
        shutil.copyfile(SCRIPT, scripts / SCRIPT.name)
        self.note()
        result = subprocess.run([sys.executable, str(scripts / SCRIPT.name), "--list"],
                                cwd="/", capture_output=True, text=True)
        self.assertEqual((result.returncode, result.stdout, result.stderr), (0, "changes/627.md\n", ""))


if __name__ == "__main__":
    unittest.main()
