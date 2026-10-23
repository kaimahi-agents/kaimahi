"""Release publication must wait for native try-it installation checks."""
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class TryItWorkflowTests(unittest.TestCase):
    def test_release_native_install_precedes_publication(self):
        text = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('  plugin-install:', text)
        self.assertIn('needs: [release, plugin-install]', text)
        self.assertIn('os: [ubuntu-latest, macos-latest]', text)
        self.assertIn('export KMX_TRY_IT_ARCHIVE=', text)
        self.assertIn('export KMX_TRY_IT_COPILOT=', text)
        self.assertIn('--ignore-scripts', text)
        self.assertIn('@github/copilot@1.0.102', text)
        self.assertNotIn("if: steps.what.outputs.dry_run == 'true'", text)
        publish = text.split('  publish:', 1)[1]
        self.assertIn('contents: write', publish)
        self.assertIn("needs.release.outputs.dry_run == 'false'", publish)
        self.assertIn('gh release create', publish)

    def test_pr_checks_install_on_mac_and_linux(self):
        text = (ROOT / '.github/workflows/copilot-plugin.yml').read_text()
        self.assertIn('  try-it-install:', text)
        self.assertIn('os: [ubuntu-latest, macos-latest]', text)
        self.assertIn('KMX_TRY_IT_BINARY:', text)
        self.assertIn('KMX_TRY_IT_COPILOT:', text)
        self.assertIn('--ignore-scripts', text)


if __name__ == '__main__':
    unittest.main()
