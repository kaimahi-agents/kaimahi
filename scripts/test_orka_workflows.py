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
    def gate(self, event='pull_request', classifier='success', ref='refs/heads/main', overrides=None):
        job = workflow('ci.yml')['jobs']['e2e-hello-world']
        needs = {name: {'result': 'success'} for name in job['needs']}
        needs['classify-change'] = {'result': classifier}
        if event == 'pull_request' or event == 'push' and ref == 'refs/heads/main':
            needs['e2e-kagent-lift']['result'] = 'skipped'
        if event == 'schedule':
            for name in ('e2e-quickstart', 'e2e-orka-helm'):
                needs[name]['result'] = 'skipped'
        needs.update(overrides or {})
        return subprocess.run(['bash', '-e', '-c', job['steps'][0]['run']], cwd=ROOT,
                              env={**os.environ, 'EVENT': event, 'REF': ref, 'SHARDS': json.dumps(needs)},
                              text=True, capture_output=True, timeout=10)

    def test_retained_shards_report_without_runtime_qualification(self):
        for event in ('pull_request', 'push', 'workflow_dispatch', 'schedule', 'unknown'):
            with self.subTest(event=event):
                self.assertEqual(self.gate(event=event).returncode, 0)
                for classifier in ('failure', 'cancelled', 'skipped'):
                    self.assertNotEqual(self.gate(event=event, classifier=classifier).returncode, 0)

    def test_retained_failures_and_unexpected_dependencies_never_pass(self):
        for job in ('kagent-contract', 'e2e-orka-helm', 'e2e-quickstart'):
            for result in ('failure', 'cancelled', 'unknown', 'skipped'):
                with self.subTest(job=job, result=result):
                    self.assertNotEqual(self.gate(overrides={job: {'result': result}}).returncode, 0)
        self.assertNotEqual(self.gate(overrides={'e2e-orka-runtime': {'result': 'success'}}).returncode, 0)


class QualificationCallers(unittest.TestCase):
    def test_stock_helm_crds_use_nonforcing_server_side_apply(self):
        step = next(s for s in workflow('ci.yml')['jobs']['e2e-orka-helm']['steps']
                    if s.get('name') == 'Install SHA-256 verified stock Orka harness-v2 chart')
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            binary = root / 'bin'
            binary.mkdir()
            calls = root / 'calls.jsonl'
            fake = '''#!/usr/bin/env python3
import json, os, pathlib, sys
name, args = pathlib.Path(sys.argv[0]).name, sys.argv[1:]
with open(os.environ['CALLS'], 'a') as stream:
    stream.write(json.dumps([name, args]) + '\\n')
if name == 'tar':
    crds = pathlib.Path(args[args.index('-C') + 1]) / 'orka/crds'
    crds.mkdir(parents=True)
    for resource in ('agent', 'task'):
        (crds / (resource + '-customresourcedefinition.yaml')).write_text('kind: CustomResourceDefinition\\n')
if name == 'helm' and 'get' in args:
    print('{"controller":{"mode":"harness-v2"}}')
'''
            for name in ('curl', 'sha256sum', 'tar', 'kubectl', 'helm'):
                path = binary / name
                path.write_text(fake)
                path.chmod(0o700)
            script = step['run'].replace('/tmp/orka-helm', str(root / 'orka-helm'))
            result = subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', script],
                env={**os.environ, 'PATH': str(binary) + os.pathsep + os.defpath,
                     'CALLS': str(calls)}, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            recorded = [json.loads(line) for line in calls.read_text().splitlines()]
            applies = [args for name, args in recorded if name == 'kubectl' and 'apply' in args]
            self.assertEqual(applies, [
                ['--context', 'kind-helm-release-orka', 'apply', '--server-side',
                 '--field-manager=kmx', '-f',
                 str(root / 'orka-helm-chart/orka/crds/agent-customresourcedefinition.yaml')],
                ['--context', 'kind-helm-release-orka', 'apply', '--server-side',
                 '--field-manager=kmx', '-f',
                 str(root / 'orka-helm-chart/orka/crds/task-customresourcedefinition.yaml')]])
            self.assertFalse(any('--force-conflicts' in args for _, args in recorded))

    def test_pr_and_push_ci_never_call_the_runtime_worker(self):
        jobs = workflow('ci.yml')['jobs']
        self.assertNotIn('e2e-orka-runtime', jobs)
        self.assertFalse(any(job.get('uses', '').endswith('/orka-runtime.yml') for job in jobs.values()))
        self.assertNotIn('e2e-orka-runtime', jobs['e2e-hello-world']['needs'])
        action = yaml.safe_load((ROOT / '.github/actions/classify-change/action.yml').read_text())
        self.assertNotIn('orka_required', action['outputs'])
        self.assertNotIn('orka_required', jobs['classify-change']['outputs'])

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
        self.assertEqual(set(jobs), {'release', 'release-contracts', 'plugin-install',
                                     'release-windows-amd64', 'publish'})
        self.assertNotIn('needs', jobs['release'])
        self.assertNotIn('if', jobs['release'])
        self.assertNotIn('qualified_sha', json.dumps(jobs))
        self.assertEqual(jobs['plugin-install']['needs'], 'release')
        self.assertEqual(jobs['plugin-install']['strategy']['matrix']['os'], ['ubuntu-latest', 'macos-latest'])
        windows = jobs['release-windows-amd64']
        self.assertEqual(windows['needs'], 'release')
        self.assertEqual(windows['runs-on'], 'windows-latest')
        self.assertFalse(any(str(step.get('uses', '')).startswith('actions/setup-go@')
                             for step in windows['steps']))
        self.assertTrue(all('continue-on-error' not in step for step in windows['steps']))
        download = next(step for step in windows['steps']
                        if str(step.get('uses', '')).startswith('actions/download-artifact@'))
        self.assertEqual(download['with']['name'], 'kmx-${{ needs.release.outputs.version }}')
        self.assertEqual(download['with']['path'], '${{ runner.temp }}/checked-release')
        windows_body = '\n'.join(step.get('run', '') for step in windows['steps'])
        self.assertIn('Get-AuthenticodeSignature', windows_body)
        self.assertIn('Zone.Identifier', windows_body)
        self.assertIn('SmartScreen: not evaluated', windows_body)
        self.assertIn("@('Valid', 'NotSigned')", windows_body)
        self.assertNotIn('go version', windows_body)
        self.assertNotIn('go env', windows_body)
        self.assertIn("FileSystem -cne 'NTFS'", windows_body)
        self.assertIn("DriveType.ToString() -cne 'Fixed'", windows_body)
        self.assertIn('Get-Partition -DriveLetter', windows_body)
        self.assertIn('windows-cli-day-zero.ps1', windows_body)
        self.assertIn('windows-orka-inspection.ps1', windows_body)
        self.assertIn('kubectl.exe.sha256', windows_body)
        self.assertIn('ProtocolFixtureMode:$false', windows_body)
        inspection = (ROOT / 'scripts/ci/windows-orka-inspection.ps1').read_text()
        self.assertIn('[Net.Sockets.TcpListener]', inspection)
        self.assertIn('Get-NetTCPConnection', inspection)
        self.assertIn('OwningProcess', inspection)
        self.assertIn('-LocalPort $clientEndpoint.Port', inspection)
        self.assertIn('-RemotePort $serverEndpoint.Port', inspection)
        self.assertIn('request owner was not the selected kubectl executable', inspection)
        self.assertIn('request owner did not match the selected kubectl digest', inspection)
        self.assertIn('Exact staged Windows amd64 remote Orka inspection qualification passed.', inspection)
        self.assertNotIn('Exact staged Windows amd64 remote Orka inspection boundary passed.', inspection)
        remote_step = next(step for step in windows['steps']
                           if 'windows-orka-inspection.ps1' in step.get('run', ''))
        self.assertEqual(remote_step['env']['KUBECTL_SHA256'],
                         '4721b614a67bb4932a0369e61f4a323d8c6ca00943d3a2ff14837c124da06f0e')
        self.assertIn("$ErrorActionPreference = 'Stop'", windows_body)
        helper_step = next(step for step in windows['steps']
                           if 'windows-cli-day-zero.ps1' in step.get('run', ''))
        self.assertEqual(helper_step['env']['RELEASE_VERSION'], '${{ needs.release.outputs.version }}')
        self.assertIn('kmx-windows-amd64.exe', windows_body)
        self.assertIn('kmx-windows-arm64.exe', windows_body)
        self.assertIn('$rows.Count -ne 6', windows_body)
        self.assertIn('$stagedBinaries.Count -ne 6', windows_body)
        trust_upload = next(step for step in windows['steps']
                            if step.get('name') == 'Upload Windows executable trust observation')
        self.assertEqual(trust_upload['if'], "steps.authenticode.outputs.recorded == 'true'")
        self.assertEqual(trust_upload['with']['if-no-files-found'], 'error')
        self.assertEqual(set(jobs['publish']['needs']), {'release', 'plugin-install',
                                                         'release-contracts', 'release-windows-amd64'})
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
        release_body = '\n'.join(s.get('run', '') for s in jobs['release']['steps'])
        self.assertIn('Runtime qualification:', release_body)
        self.assertIn('--require-windows', release_body)
        for asset in ('kmx-windows-amd64.exe', 'kmx-windows-arm64.exe'):
            self.assertIn(asset, release_body)
            self.assertIn(asset, '\n'.join(s.get('run', '') for s in jobs['publish']['steps']))
        goreleaser = yaml.safe_load((ROOT / '.goreleaser.yaml').read_text())
        build = goreleaser['builds'][0]
        self.assertEqual(build['goos'], ['linux', 'darwin', 'windows'])
        self.assertEqual(build['goarch'], ['amd64', 'arm64'])

    def test_release_declares_exact_six_core_and_four_unix_plugin_assets(self):
        text = (ROOT / '.github/workflows/release.yml').read_text()
        core = {
            'kmx-linux-amd64', 'kmx-linux-arm64',
            'kmx-darwin-amd64', 'kmx-darwin-arm64',
            'kmx-windows-amd64.exe', 'kmx-windows-arm64.exe',
        }
        plugin = {
            'kmx-agent-builder-linux-amd64.tar.gz',
            'kmx-agent-builder-linux-arm64.tar.gz',
            'kmx-agent-builder-darwin-amd64.tar.gz',
            'kmx-agent-builder-darwin-arm64.tar.gz',
        }
        publish = text.split('  publish:', 1)[1]
        self.assertEqual({name for name in core if f'"$artifact_dir/{name}"' in publish}, core)
        self.assertEqual({name for name in plugin if f'"$artifact_dir/{name}"' in publish}, plugin)
        self.assertNotIn('kmx-agent-builder-windows-', text)

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
                notes_text = notes.read_text()
                self.assertEqual(notes_text.count('Runtime qualification:'), 1 if expected == 0 else 0)
                if expected == 0:
                    self.assertIn('Native Windows qualification covers amd64 day-zero Run/Test/offline Explain', notes_text)
                    self.assertIn('Windows arm64 is checksum, metadata and cross-build evidence only', notes_text)
                if expected:
                    self.assertIn('CHANGELOG.md has no section', result.stderr)

    def run_hygiene_check(self, name, mutate=None):
        step = next(s for s in workflow('ci.yml')['jobs']['hygiene-checks']['steps'] if s.get('name') == name)
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
        for mutation in ('runtime', 'build-needs', 'contracts', 'plugin', 'windows', 'windows-setup-go'):
            def mutate(root, mutation=mutation):
                path = root / '.github/workflows/release.yml'
                text = path.read_text()
                if mutation == 'runtime':
                    text = text.replace('jobs:\n', 'jobs:\n  orka-qualification:\n    permissions:\n      contents: read\n    uses: ./.github/workflows/orka-runtime.yml\n', 1)
                elif mutation == 'build-needs':
                    text = text.replace('  release:\n', '  release:\n    needs: [release-contracts]\n', 1)
                elif mutation == 'windows-setup-go':
                    marker = '  release-windows-amd64:\n'
                    before, after = text.split(marker, 1)
                    after = after.replace('    steps:\n', '    steps:\n      - uses: actions/setup-go@v5\n', 1)
                    text = before + marker + after
                else:
                    gates = ('release', 'plugin-install', 'release-contracts', 'release-windows-amd64')
                    gate = {'contracts': 'release-contracts', 'plugin': 'plugin-install',
                            'windows': 'release-windows-amd64'}[mutation]
                    text = text.replace('    needs: [' + ', '.join(gates) + ']',
                                        '    needs: [' + ', '.join(g for g in gates if g != gate) + ']')
                path.write_text(text)
            with self.subTest(mutation=mutation):
                result = self.run_hygiene_check(name, mutate)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('release.yml must build immediately' if mutation in ('runtime', 'build-needs')
                              else 'release.yml must natively qualify' if mutation == 'windows-setup-go'
                              else 'release.yml must wait for native install, Windows day-zero and hermetic contracts', result.stdout)

    def check_sha_script(self, program, worker=False):
        with tempfile.TemporaryDirectory() as folder:
            def git(*args):
                return subprocess.check_output(['git', '-C', folder, *args], text=True).strip()
            git('init', '-q'); git('config', 'user.email', 'test@example.invalid'); git('config', 'user.name', 'Test')
            git('commit', '--allow-empty', '--no-gpg-sign', '-qm', 'fixture')
            sha = git('rev-parse', 'HEAD')
            git('tag', '--no-sign', 'v1.0.0'); git('tag', '--no-sign', '-am', 'annotated', 'v1.0.1')
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
