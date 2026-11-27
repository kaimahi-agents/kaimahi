"""Exercise the offline demo against disposable Git repositories, without KMX/network."""

import contextlib
import fcntl
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("demo", Path(__file__).with_name("demo-project-maintenance.py"))
demo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(demo)


class ProjectMaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve() / "demo"
        with contextlib.redirect_stdout(io.StringIO()):
            demo.initialize(self.root)

    def cycle(self, mode):
        with contextlib.redirect_stdout(io.StringIO()) as output:
            report = demo.cycle(self.root, mode)
        self.assertLess(len(output.getvalue()), 1024)
        self.assertNotIn("Slow search", output.getvalue())
        return report

    def head(self, name):
        return demo.git(self.root, self.root / "worktrees" / name, "rev-parse", "HEAD").stdout.strip()

    def test_monitor_reports_activity_and_drift_without_rebasing(self):
        heads = {name: self.head(name) for name in demo.PROJECTS}
        report = self.cycle("monitor")
        self.assertEqual([r["status"] for r in report["worktrees"]], ["needs-rebase", "skipped-dirty", "needs-rebase"])
        self.assertEqual(heads, {name: self.head(name) for name in demo.PROJECTS})
        text = (self.root / "reports" / "monitor.md").read_text()
        self.assertIn("Cache dashboard queries", text)
        self.assertIn("Dashboard intermittently blank", text)
        self.assertIn("checks are failing", text)
        self.assertEqual(self.cycle("monitor")["changedProjects"], [])
        activity = json.loads((self.root / "activity.json").read_text())
        activity["beacon"]["prs"][0]["checks"] = "passing"
        (self.root / "activity.json").write_text(json.dumps(activity))
        self.assertEqual(self.cycle("monitor")["changedProjects"], ["beacon"])

    def test_maintenance_preserves_work_and_restores_conflict(self):
        heads = {name: self.head(name) for name in demo.PROJECTS}
        report = self.cycle("maintain")
        rows = {r["project"]: r for r in report["worktrees"]}
        self.assertEqual([rows[n]["status"] for n in demo.PROJECTS], ["rebased", "skipped-dirty", "conflict-aborted"])
        self.assertEqual(report["blockedWorktrees"], 2)
        self.assertNotEqual(heads["atlas"], self.head("atlas"))
        self.assertEqual(heads["beacon"], self.head("beacon"))
        self.assertEqual(heads["courier"], self.head("courier"))
        self.assertEqual((self.root / "worktrees" / "atlas" / "feature.txt").read_text(), "Local feature work\n")
        self.assertEqual((self.root / "worktrees" / "atlas" / "settings.txt").read_text(), "timeout=30\n")
        self.assertEqual((self.root / "worktrees" / "beacon" / "notes.txt").read_text(), "Uncommitted design notes: keep me.\n")
        self.assertEqual((self.root / "worktrees" / "courier" / "settings.txt").read_text(), "timeout=20\n")
        for name in ("atlas", "courier"):
            backup = demo.git(self.root, self.root / "worktrees" / name, "rev-parse", rows[name]["backupRef"]).stdout.strip()
            self.assertEqual(backup, heads[name])
        self.assertFalse(demo.inspect(self.root, "courier")["dirty"])
        updated = self.head("atlas")
        self.assertEqual(self.cycle("maintain")["worktrees"][0]["status"], "current")
        self.assertEqual(self.head("atlas"), updated)
        beacon = self.root / "worktrees" / "beacon"
        demo.git(self.root, beacon, "add", "--", "notes.txt")
        demo.git(self.root, beacon, "commit", "-m", "Preserve fixture notes")
        self.assertEqual(self.cycle("maintain")["worktrees"][1]["status"], "rebased")
        self.assertEqual((beacon / "notes.txt").read_text(), "Uncommitted design notes: keep me.\n")

    def test_existing_directory_and_invalid_activity_refuse_before_effects(self):
        with self.assertRaises(FileExistsError):
            demo.initialize(self.root)
        head = self.head("atlas")
        activity = json.loads((self.root / "activity.json").read_text())
        activity["atlas"]["command"] = "arbitrary"
        (self.root / "activity.json").write_text(json.dumps(activity))
        with self.assertRaises(ValueError):
            self.cycle("maintain")
        self.assertEqual(self.head("atlas"), head)

    def test_redirected_worktree_is_unknown_and_concurrent_cycle_refuses(self):
        worktree = self.root / "worktrees" / "atlas"
        moved = self.root / "worktrees" / "moved"
        worktree.rename(moved)
        worktree.symlink_to(moved, target_is_directory=True)
        self.assertEqual(demo.update_worktree(self.root, "atlas", "maintain")["status"], "unknown")
        with (self.root / ".cycle.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                self.cycle("maintain")


if __name__ == "__main__":
    unittest.main()
