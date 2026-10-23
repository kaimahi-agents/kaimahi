### Added

- `kmx run export FILE --out FILE` writes a new private frozen profile using the canonical agent parser. It preserves instructions, authored model and ordered examples with hashes, marks target examples not-run, and includes the existing generic Sessions chat configuration. No runtime/model/registry calls, passing evidence, packaging or deployment are implied. Native Windows private publication remains unsupported.

### Breaking

- `export` is now a helper name under `kmx run`. For a canonical source file literally named `export`, use `kmx run ./export`; live `--prompt export` remains input.
