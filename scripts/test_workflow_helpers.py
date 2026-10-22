"""Resolve only the repository's fixed, read-only reusable runtime proofs in tests."""
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[1]


def runtime_job(root=ROOT):
    jobs = yaml.safe_load((root / '.github/workflows/ci.yml').read_text())['jobs']
    caller = jobs['e2e-orka-runtime']
    if 'uses' not in caller:
        return caller
    if caller['uses'] != './.github/workflows/orka-runtime.yml':
        raise ValueError('unexpected Orka delegation')
    return yaml.safe_load((root / '.github/workflows/orka-runtime.yml').read_text())['jobs']['qualify']


def kagent_runtime_job(root=ROOT):
    caller = yaml.safe_load((root / '.github/workflows/ci.yml').read_text())['jobs']['e2e-kagent-lift']
    if 'uses' not in caller:
        return caller
    if caller['uses'] != './.github/workflows/kagent-runtime.yml':
        raise ValueError('unexpected kagent delegation')
    return yaml.safe_load((root / '.github/workflows/kagent-runtime.yml').read_text())['jobs']['qualify']
