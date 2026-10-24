#!/usr/bin/env python3
"""Execute required gates and pin separate release/runtime qualification wiring."""
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]


def workflow(name):
    return yaml.safe_load((ROOT / '.github/workflows' / name).read_text())


class RequiredGate(unittest.TestCase):
    def gate(self, event='pull_request', required='false', result='skipped', classifier='success', ref='refs/heads/main', overrides=None):
        job = workflow('ci.yml')['jobs']['e2e-hello-world']
        needs = {name: {'result': 'success'} for name in job['needs']}
        needs['classify-change'] = {'result': classifier, 'outputs': {
            'orka_required': required, 'kagent_changed': 'true'}}
        needs['e2e-orka-runtime']['result'] = result
        if event == 'schedule':
            for name in ('e2e-quickstart', 'e2e-orka-helm'):
                needs[name]['result'] = 'skipped'
        needs.update(overrides or {})
        return subprocess.run(['bash', '-e', '-c', job['steps'][0]['run']], cwd=ROOT,
                              env={**os.environ, 'EVENT': event, 'REF': ref, 'SHARDS': json.dumps(needs)},
                              text=True, capture_output=True, timeout=10)

    def test_only_explicit_safe_pr_skip_reports_green(self):
        self.assertEqual(self.gate().returncode, 0)
        for required in ('true', '', 'unknown', 'False'):
            with self.subTest(required=required):
                self.assertNotEqual(self.gate(required=required).returncode, 0)
        for classifier in ('failure', 'cancelled', 'skipped'):
            self.assertNotEqual(self.gate(classifier=classifier).returncode, 0)

    def test_relevant_failure_cancellation_or_missing_proof_never_passes(self):
        for result in ('failure', 'cancelled', 'unknown', 'skipped'):
            self.assertNotEqual(self.gate(required='true', result=result).returncode, 0)
        self.assertEqual(self.gate(required='true', result='success').returncode, 0)

    def test_only_main_push_moves_to_the_new_nightly(self):
        self.assertEqual(self.gate(event='push').returncode, 0)
        self.assertNotEqual(self.gate(event='push', classifier='failure').returncode, 0)
        self.assertNotEqual(self.gate(event='push', ref='refs/heads/other').returncode, 0)
        self.assertNotEqual(self.gate(event='push', ref='').returncode, 0)
        for result in ('failure', 'cancelled', 'unknown', ''):
            self.assertNotEqual(self.gate(event='push', result=result).returncode, 0)
        for other in ('kagent-contract', 'e2e-orka-helm', 'e2e-quickstart'):
            self.assertNotEqual(self.gate(event='push', overrides={other: {'result': 'skipped'}}).returncode, 0)
        for event in ('workflow_dispatch', 'unknown'):
            self.assertNotEqual(self.gate(event=event).returncode, 0)
            self.assertEqual(self.gate(event=event, result='success').returncode, 0)
        self.assertEqual(self.gate(event='schedule').returncode, 0)


class QualificationCallers(unittest.TestCase):
    def test_pr_worker_is_local_read_only_and_default_runs_unknown_paths(self):
        job = workflow('ci.yml')['jobs']['e2e-orka-runtime']
        self.assertEqual(job.get('uses'), './.github/workflows/orka-runtime.yml')
        self.assertEqual(job['permissions'], {'contents': 'read'})
        self.assertEqual(job['needs'], ['classify-change'])
        self.assertNotIn('secrets', job)
        expr = job['if'].strip().removeprefix('${{').removesuffix('}}')
        for event, required, ref, expected in [
                ('pull_request', 'false', 'refs/pull/1/merge', False),
                ('pull_request', 'true', 'refs/pull/1/merge', True),
                ('pull_request', '', 'refs/pull/1/merge', True),
                ('pull_request', 'unknown', 'refs/pull/1/merge', True),
                ('push', 'false', 'refs/heads/main', False),
                ('push', 'false', 'refs/heads/other', True),
                ('push', 'false', '', True),
                ('workflow_dispatch', 'false', 'refs/heads/main', True),
                ('unknown', 'false', 'refs/heads/main', True),
                ('schedule', 'true', 'refs/heads/main', False)]:
            expression = expr.replace('github.event_name', repr(event)).replace(
                'needs.classify-change.outputs.orka_required', repr(required)).replace(
                'github.ref', repr(ref)).replace(
                '!cancelled()', 'True').replace('!(', 'not (').replace('&&', ' and ').replace('||', ' or ')
            self.assertEqual(bool(eval(expression, {'__builtins__': {}}, {})), expected)

    def test_full_worker_has_no_path_or_schedule_shortcut(self):
        wf = workflow('orka-runtime.yml')
        self.assertIn('workflow_call', wf.get('on', wf.get(True)))
        job = wf['jobs']['qualify']
        self.assertEqual(job['permissions'], {'contents': 'read'})
        self.assertEqual(job['timeout-minutes'], 45)
        self.assertNotIn('if', job)
        self.assertEqual(job['outputs']['qualified_sha'], '${{ steps.provenance.outputs.sha }}')
        text = json.dumps(job)
        for forbidden in ('docs_only', 'classify-change', 'continue-on-error', 'secrets.', 'concurrency'):
            self.assertNotIn(forbidden, text)
        body = '\n'.join(s.get('run', '') for s in job['steps'])
        for retained in ('suite-native.py kind-kmx-orka kind-kmx-orka-promote',
                         'TestRelease(UpdatePinnedRV|StatePinnedCAS)', 'TestLiveInspectQuickstartToolEvents',
                         'Orka says hello.', 'KMX_ORKA_PROMOTION_CREATED=1'):
            self.assertIn(retained, body)

    def test_nightly_always_qualifies_and_reporting_is_schedule_main_only(self):
        wf = workflow('orka-nightly.yml')
        on = wf.get('on', wf.get(True))
        self.assertEqual(on['schedule'], [{'cron': '23 3 * * *'}])
        self.assertIn('workflow_dispatch', on)
        runtime = wf['jobs']['qualify']
        self.assertEqual(runtime['uses'], './.github/workflows/orka-runtime.yml')
        self.assertNotIn('if', runtime)
        gate = wf['jobs']['qualification']
        self.assertEqual(gate['if'], 'always()')
        self.assertEqual(gate['needs'], ['qualify'])
        for outcome, code in [('success', 0), ('failure', 1), ('cancelled', 1), ('skipped', 1), ('', 1)]:
            with tempfile.NamedTemporaryFile() as summary:
                result = subprocess.run(['bash', '-e', '-c', gate['steps'][0]['run']],
                    env={**os.environ, 'RESULT': outcome, 'SHA': 'a'*40,
                         'GITHUB_STEP_SUMMARY': summary.name, 'GITHUB_SERVER_URL': 'https://github.com',
                         'GITHUB_REPOSITORY': 'kaimahi-agents/kaimahi', 'GITHUB_RUN_ID': '123'}, capture_output=True)
                self.assertEqual(result.returncode, code)
        reporter = wf['jobs']['report']
        self.assertEqual(reporter['permissions'], {'contents': 'read', 'actions': 'read', 'issues': 'write'})
        self.assertIn("github.event_name == 'schedule'", reporter['if'])
        self.assertIn("github.ref == 'refs/heads/main'", reporter['if'])
        self.assertIn('always()', reporter['if'])
        self.assertFalse(reporter['concurrency']['cancel-in-progress'])
        self.assertNotEqual(reporter['concurrency']['group'], runtime.get('concurrency'))

    def test_on_demand_runtime_jobs_keep_full_read_only_exact_commit_workers(self):
        wf = workflow('runtime-qualification.yml')
        self.assertEqual(wf.get('on', wf.get(True)), {'workflow_dispatch': None})
        self.assertEqual(wf['permissions'], {'contents': 'read'})
        self.assertEqual(set(wf['jobs']), {'orka-qualification', 'kagent-qualification'})
        for runtime in ('orka', 'kagent'):
            self.assertEqual(wf['jobs'][runtime + '-qualification'], {
                'permissions': {'contents': 'read'},
                'uses': './.github/workflows/' + runtime + '-runtime.yml'})
            steps = workflow(runtime + '-runtime.yml')['jobs']['qualify']['steps']
            checkout = next(s for s in steps if s.get('uses', '').startswith('actions/checkout@'))
            self.assertEqual(checkout['with']['ref'], '${{ github.sha }}')
            check = next(s for s in steps if s.get('id') == 'provenance')
            self.check_sha_script(check['run'], worker=True)

    def test_release_starts_immediately_and_publish_requires_shipped_path_contracts(self):
        jobs = workflow('release.yml')['jobs']
        self.assertEqual(set(jobs), {'release', 'release-contracts', 'plugin-install', 'publish'})
        self.assertNotIn('needs', jobs['release'])
        self.assertNotIn('if', jobs['release'])
        self.assertNotIn('qualified_sha', json.dumps(jobs))
        self.assertEqual(jobs['plugin-install']['needs'], 'release')
        self.assertEqual(jobs['plugin-install']['strategy']['matrix']['os'], ['ubuntu-latest', 'macos-latest'])
        self.assertEqual(set(jobs['publish']['needs']), {'release', 'plugin-install', 'release-contracts'})
        self.assertEqual(jobs['publish']['if'], "needs.release.outputs.dry_run == 'false'")
        contracts = jobs['release-contracts']
        self.assertEqual(contracts['permissions'], {'contents': 'read'})
        self.assertNotIn('needs', contracts)
        self.assertNotIn('if', contracts)
        self.assertTrue(all('if' not in s and 'continue-on-error' not in s for s in contracts['steps']))
        body = '\n'.join(s.get('run', '') for s in contracts['steps'])
        plugin = workflow('copilot-plugin.yml')['jobs']['native-package']['steps']
        for step in plugin:
            if 'run' in step:
                self.assertIn(step['run'], body)
        self.assertIn('go test ./internal/kmx/localpreview -count=1', body)
        self.assertIn('Runtime qualification:', '\n'.join(s.get('run', '') for s in jobs['release']['steps']))

    def test_release_notes_scope_keeps_named_and_synthetic_failure_rules(self):
        step = next(s for s in workflow('release.yml')['jobs']['release']['steps']
                    if s.get('name') == 'Release notes exist for this version')
        for synthetic, unreleased, version, expected in [
                ('false', '', 'v1.0.0', 0), ('true', '- next\n', 'unused', 0),
                ('true', '', 'unused', 0), ('false', '', 'v9.9.9', 3)]:
            with self.subTest(synthetic=synthetic, unreleased=unreleased, version=version), tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                (root / 'scripts').mkdir()
                shutil.copy(ROOT / 'scripts/release-notes.py', root / 'scripts/release-notes.py')
                (root / 'CHANGELOG.md').write_text('# Changelog\n\n## Unreleased\n\n' + unreleased + '\n## v1.0.0\n\n- shipped\n')
                notes = root / 'notes.md'
                result = subprocess.run(['bash', '-e', '-c', step['run']], cwd=root,
                    env={**os.environ, 'VERSION': version, 'SYNTHETIC': synthetic,
                         'NOTES': str(notes), 'RUNNER_TEMP': folder}, text=True, capture_output=True, timeout=10, check=False)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertEqual(notes.read_text().count('Runtime qualification:'), 1 if expected == 0 else 0)
                if expected:
                    self.assertIn('CHANGELOG.md has no section', result.stderr)

    def run_hygiene_check(self, name, mutate=None):
        step = next(s for s in workflow('ci.yml')['jobs']['hygiene']['steps'] if s.get('name') == name)
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            shutil.copytree(ROOT / '.github/workflows', root / '.github/workflows')
            for path in ('scripts', '.goreleaser.yaml', 'CHANGELOG.md'):
                (root / path).symlink_to(ROOT / path)
            if mutate:
                mutate(root)
            return subprocess.run(['bash', '-e', '-c', step['run']], cwd=root,
                                  env=os.environ, text=True, capture_output=True, timeout=30, check=False)

    def test_cluster_self_check_rejects_changed_manual_runtime_callers(self):
        name = 'Cluster routing, reusable Orka proof and docs-only guards'
        self.assertEqual(self.run_hygiene_check(name).returncode, 0)
        for mutation in ('missing', 'conditional', 'trigger', 'ref'):
            def mutate(root, mutation=mutation):
                path = root / '.github/workflows/runtime-qualification.yml'
                wf = yaml.safe_load(path.read_text())
                if mutation == 'missing':
                    del wf['jobs']['kagent-qualification']
                elif mutation == 'conditional':
                    wf['jobs']['orka-qualification']['if'] = 'false'
                elif mutation == 'trigger':
                    wf[True] = {'push': None}
                else:
                    path = root / '.github/workflows/orka-runtime.yml'
                    wf = yaml.safe_load(path.read_text())
                    wf['jobs']['qualify']['steps'][0]['with']['ref'] = 'main'
                path.write_text(yaml.safe_dump(wf))
            with self.subTest(mutation=mutation):
                result = self.run_hygiene_check(name, mutate)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('runtime qualification', result.stdout)

    def test_release_self_check_rejects_runtime_dependencies_and_missing_publish_gates(self):
        name = 'The unreleased section exists, and the release job stays keyless'
        self.assertEqual(self.run_hygiene_check(name).returncode, 0)
        for mutation in ('runtime', 'build-needs', 'contracts', 'plugin'):
            def mutate(root, mutation=mutation):
                path = root / '.github/workflows/release.yml'
                text = path.read_text()
                if mutation == 'runtime':
                    text = text.replace('jobs:\n', 'jobs:\n  orka-qualification:\n    permissions:\n      contents: read\n    uses: ./.github/workflows/orka-runtime.yml\n', 1)
                elif mutation == 'build-needs':
                    text = text.replace('  release:\n', '  release:\n    needs: [release-contracts]\n', 1)
                else:
                    gate = 'release-contracts' if mutation == 'contracts' else 'plugin-install'
                    text = text.replace('    needs: [release, plugin-install, release-contracts]',
                                        '    needs: [' + ', '.join(g for g in ('release', 'plugin-install', 'release-contracts') if g != gate) + ']')
                path.write_text(text)
            with self.subTest(mutation=mutation):
                result = self.run_hygiene_check(name, mutate)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('release.yml must build immediately' if mutation in ('runtime', 'build-needs')
                              else 'release.yml must wait for native install checks and hermetic contracts', result.stdout)

    def check_sha_script(self, program, worker=False):
        with tempfile.TemporaryDirectory() as folder:
            def git(*args):
                return subprocess.check_output(['git', '-C', folder, *args], text=True).strip()
            git('init', '-q'); git('config', 'user.email', 'test@example.invalid'); git('config', 'user.name', 'Test')
            git('commit', '--allow-empty', '-qm', 'fixture')
            sha = git('rev-parse', 'HEAD')
            git('tag', 'v1.0.0'); git('tag', '-am', 'annotated', 'v1.0.1')
            for event_sha in (sha, git('rev-parse', 'v1.0.0'), git('rev-parse', 'v1.0.1')):
                for valid in (True, False):
                    with tempfile.NamedTemporaryFile() as output:
                        env = {**os.environ, 'EVENT_SHA': event_sha, 'GITHUB_OUTPUT': output.name,
                               'QUALIFIED_SHA': sha if valid else 'b'*40}
                        if worker and not valid:
                            env['EVENT_SHA'] = 'b'*40
                        result = subprocess.run(['bash', '-e', '-c', program], cwd=folder,
                                                env=env, capture_output=True)
                        self.assertEqual(result.returncode == 0, valid)
                        if worker and valid:
                            self.assertEqual(Path(output.name).read_text(), f'sha={sha}\n')


if __name__ == '__main__':
    unittest.main()
