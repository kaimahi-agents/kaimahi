# AgentSuite consumer fixtures

These kmx-owned snapshots keep consumer tests offline and independent of the
Go module cache. They are copied byte-for-byte from `agentsuite/testdata` in
[kaimahi at e48f063f422bd38b7f92173351defd21bfcda5cc](https://github.com/kaimahi-agents/kaimahi/tree/e48f063f422bd38b7f92173351defd21bfcda5cc/agentsuite/testdata).
The [standalone suite](https://github.com/agent-primitives/suite) keeps the portable
contract and conformance tests; kmx owns only these consumer-test inputs.

`provenance.json` records the frozen source, per-file SHA256 and Git file mode.
The same fixtures are published in **v0.1.0** of the standalone module, at
commit `6281ce09a3d7f4f2fc4b3a556d602b07eeab7ffa`. The provenance manifest records
that release alongside their original source.

Run `python3 scripts/check-agentsuite-fixtures.py --selftest` and
`python3 scripts/check-agentsuite-fixtures.py` from the repository root.
Optionally compare an upstream suite checkout with
`python3 scripts/check-agentsuite-fixtures.py --upstream /absolute/path/to/suite`.
This verifies the recorded files against that checkout, not tag publication.
Refresh snapshots and provenance together only after reviewing upstream changes.
The internal builder's incident-analyst fixture remains separate and unchanged.
