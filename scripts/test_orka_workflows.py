#!/usr/bin/env python3
"""Execute the required gate and pin full nightly/release qualification wiring."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

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
        for other in ('e2e-kagent-lift', 'e2e-orka-helm', 'e2e-quickstart'):
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

    def test_release_and_dry_run_require_fresh_read_only_same_sha_proof(self):
        wf = workflow('release.yml')
        qualification = wf['jobs'].get('orka-qualification', {})
        self.assertEqual(qualification.get('uses'), './.github/workflows/orka-runtime.yml')
        self.assertEqual(qualification['permissions'], {'contents': 'read'})
        self.assertNotIn('if', qualification)
        release = wf['jobs']['release']
        self.assertEqual(release['needs'], ['orka-qualification'])
        check = next(s for s in release['steps'] if s.get('name') == 'Release commit has fresh Orka qualification')
        self.assertEqual(check['env']['QUALIFIED_SHA'], '${{ needs.orka-qualification.outputs.qualified_sha }}')
        self.check_sha_script(check['run'])
        worker_check = next(s for s in workflow('orka-runtime.yml')['jobs']['qualify']['steps']
                            if s.get('id') == 'provenance')
        self.check_sha_script(worker_check['run'], worker=True)

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
