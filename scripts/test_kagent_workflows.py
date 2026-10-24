#!/usr/bin/env python3
"""Pin the hermetic/live kagent boundary and execute required-check policies."""
import json
import os
import subprocess
import unittest
from pathlib import Path

import test_orka_workflows as orka_tests
import yaml

ROOT = Path(__file__).resolve().parents[1]
FAST_GO = "go test ./internal/kmx/app -run '^(TestKagent|TestSuiteKagent|TestSuiteFailureKagent|TestReleaseLiftKagent|TestDestinationStatusKagent)' -skip '^TestLive' -count=1 -timeout=300s"


def workflow(name):
    return yaml.safe_load((ROOT / '.github/workflows' / name).read_text())


def enabled(expression, event, ref='refs/heads/main'):
    expression = expression.strip().removeprefix('${{').removesuffix('}}')
    expression = expression.replace('github.event_name', repr(event)).replace('github.ref', repr(ref))
    expression = expression.replace('!cancelled()', 'True').replace('!(', 'not (')
    return eval(expression.replace('&&', ' and ').replace('||', ' or '), {'__builtins__': {}}, {})


class KagentWorkflows(unittest.TestCase):
    def test_fast_contract_is_unconditional_hermetic_not_live_qualification(self):
        fast = workflow('ci.yml')['jobs'].get('kagent-contract', {})
        self.assertTrue(fast, 'missing independent fast kagent check')
        self.assertEqual(fast['if'], '${{ !cancelled() }}')
        self.assertEqual(fast['permissions'], {'contents': 'read'})
        self.assertEqual(fast['timeout-minutes'], 10)
        self.assertIn('not live qualification', fast['name'])
        self.assertNotIn('needs', fast)
        self.assertNotIn('secrets', fast)
        steps = fast['steps']
        self.assertTrue(all('if' not in s for s in steps))
        body = '\n'.join(s.get('run', '') for s in steps)
        self.assertIn("python3 -m unittest discover -s scripts -p 'test_kagent_lift*.py'", body)
        self.assertIn('python3 scripts/test_suite_kagent.py', body)
        self.assertIn('python3 scripts/test_kagent_workflows.py', body)
        self.assertIn(FAST_GO, body)
        for forbidden in ('bash scripts/ci/kagent-lift.sh', 'kind create', 'KMX_SUITE_KAGENT_LIVE=1', 'continue-on-error'):
            self.assertNotIn(forbidden, json.dumps(fast))

    def test_full_qualification_moves_to_nightly_manual_and_other_refs(self):
        job = workflow('ci.yml')['jobs']['e2e-kagent-lift']
        self.assertEqual(job.get('uses'), './.github/workflows/kagent-runtime.yml')
        self.assertEqual(job['permissions'], {'contents': 'read'})
        self.assertNotIn('secrets', job)
        self.assertNotIn('kagent_changed', job['if'])
        for event, ref, wanted in [('pull_request', 'refs/pull/1/merge', False),
                                   ('push', 'refs/heads/main', False),
                                   ('push', 'refs/heads/other', True),
                                   ('push', '', True), ('schedule', 'refs/heads/main', True),
                                   ('workflow_dispatch', 'refs/heads/main', True),
                                   ('unknown', 'refs/heads/main', True)]:
            with self.subTest(event=event, ref=ref):
                self.assertEqual(bool(enabled(job['if'], event, ref)), wanted)
        on = workflow('ci.yml').get('on', workflow('ci.yml').get(True))
        self.assertEqual(on['schedule'], [{'cron': '23 3 * * *'}])

    def test_reusable_full_proof_is_read_only_and_unchanged(self):
        wf = workflow('kagent-runtime.yml')
        self.assertIn('workflow_call', wf.get('on', wf.get(True)))
        job = wf['jobs']['qualify']
        self.assertNotIn('if', job)
        self.assertEqual(job['timeout-minutes'], 35)
        self.assertEqual(job['permissions'], {'contents': 'read'})
        self.assertEqual(job['outputs']['qualified_sha'], '${{ steps.provenance.outputs.sha }}')
        for forbidden in ('docs_only', 'kagent_changed', 'continue-on-error', 'secrets.', 'concurrency'):
            self.assertNotIn(forbidden, json.dumps(job))
        self.assertTrue(all(s.get('if', '') in ('', 'always()', 'failure()') for s in job['steps']))
        actions = [s['uses'] for s in job['steps'] if 'uses' in s]
        self.assertEqual(actions, ['actions/checkout@11d5960a326750d5838078e36cf38b85af677262',
                                  'actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff',
                                  'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02'])
        body = '\n'.join(s.get('run', '') for s in job['steps'])
        self.assertIn('mkdir -p bin && go build -o bin/kmx ./cmd/kmx', body)
        self.assertIn('bash scripts/ci/kagent-lift.sh', body)
        check = next(s for s in job['steps'] if s.get('id') == 'provenance')
        orka_tests.QualificationCallers().check_sha_script(check['run'], worker=True)

    def gate(self, event='pull_request', ref='refs/heads/main', full='skipped', fast='success', classifier='success'):
        agg = workflow('ci.yml')['jobs']['e2e-hello-world']
        needs = {name: {'result': 'success'} for name in agg['needs']}
        needs['classify-change'] = {'result': classifier, 'outputs': {'orka_required': 'true', 'kagent_changed': 'true'}}
        needs['e2e-kagent-lift'] = {'result': full}
        needs['kagent-contract'] = {'result': fast}
        if event == 'push' and ref == 'refs/heads/main' or event == 'schedule':
            needs['e2e-orka-runtime'] = {'result': 'skipped'}
        if event == 'schedule':
            for name in ('e2e-orka-helm', 'e2e-quickstart'):
                needs[name] = {'result': 'skipped'}
        return subprocess.run(['bash', '-e', '-c', agg['steps'][0]['run']], cwd=ROOT,
                              env={**os.environ, 'SHARDS': json.dumps(needs), 'EVENT': event, 'REF': ref},
                              capture_output=True, text=True, timeout=10)

    def test_aggregate_always_requires_fast_contract_success(self):
        agg = workflow('ci.yml')['jobs']['e2e-hello-world']
        self.assertEqual(agg['if'], 'always()')
        self.assertIn('kagent-contract', agg['needs'])
        for event, full in [('pull_request', 'skipped'), ('push', 'skipped'),
                            ('schedule', 'success'), ('workflow_dispatch', 'success')]:
            self.assertEqual(self.gate(event=event, full=full).returncode, 0)
            for fast in ('failure', 'cancelled', 'skipped', '', 'unknown'):
                self.assertNotEqual(self.gate(event=event, full=full, fast=fast).returncode, 0)
            for classifier in ('failure', 'cancelled', 'skipped', ''):
                self.assertNotEqual(self.gate(event=event, full=full, classifier=classifier).returncode, 0)

    def test_only_authorized_full_skip_passes_never_failure_or_cancellation(self):
        for event, ref in [('pull_request', 'refs/pull/1/merge'), ('push', 'refs/heads/main')]:
            self.assertEqual(self.gate(event=event, ref=ref).returncode, 0)
            for full in ('failure', 'cancelled', '', 'unknown'):
                self.assertNotEqual(self.gate(event=event, ref=ref, full=full).returncode, 0)
        for event, ref in [('schedule', 'refs/heads/main'), ('workflow_dispatch', 'refs/heads/main'),
                           ('unknown', 'refs/heads/main'), ('push', 'refs/heads/other'), ('push', '')]:
            self.assertNotEqual(self.gate(event=event, ref=ref).returncode, 0)
            self.assertEqual(self.gate(event=event, ref=ref, full='success').returncode, 0)

    def test_kagent_qualification_is_on_demand_not_a_release_gate(self):
        jobs = workflow('release.yml')['jobs']
        self.assertNotIn('kagent-qualification', jobs)
        self.assertNotIn('orka-qualification', jobs)
        self.assertNotIn('needs', jobs['release'])
        worker = workflow('runtime-qualification.yml')['jobs']['kagent-qualification']
        self.assertEqual(worker, {'permissions': {'contents': 'read'},
                                  'uses': './.github/workflows/kagent-runtime.yml'})
        proof = workflow('kagent-runtime.yml')['jobs']['qualify']
        check = next(s for s in proof['steps'] if s.get('id') == 'provenance')
        orka_tests.QualificationCallers().check_sha_script(check['run'], worker=True)
        for value in ('', 'x' * 40, 'a' * 39):
            result = subprocess.run(['bash', '-e', '-c', check['run']],
                                    env={**os.environ, 'EVENT_SHA': value},
                                    cwd=ROOT, capture_output=True)
            self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()
